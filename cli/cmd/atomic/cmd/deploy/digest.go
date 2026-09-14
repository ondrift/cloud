// digest.go — content-addressed fingerprinting for atomic functions.
//
// `drift file apply` skips functions whose source hasn't changed. To do
// that safely it needs a stable fingerprint of a function's deploy inputs that
// it can compare against the digest the platform recorded at the last deploy.
//
// The digest is computed entirely client-side; the platform stores and returns
// it as an opaque token (see core/common/db/atomic.go). It has two halves, and
// what is recorded against a function is both:
//
//   - the BUILD digest — handler code, trigger comments, dependency manifests,
//     plus the element grouping. FunctionDigest and ElementDigest.
//   - the DECLARATION digest — everything the Driftfile entry tells the
//     operator: the gate, the secrets, the env, the reply shape, the stream mode
//     and the declared `cron:`. DeclarationDigest.
//
// # Why the declaration is in here, when it once deliberately was not
//
// This file used to say: "It deliberately does NOT hash the Driftfile entry.
// Changing a function's gate or its secrets changes what the operator is told,
// not what is built, and both are sent on every deploy — so a manifest-only edit
// must not force a rebuild of code that did not change."
//
// The reasoning is right and the premise is false. A function whose digest
// matches is SKIPPED — `elementUnchanged` drops the whole element before
// anything is staged, built or uploaded — so there is no deploy on which to send
// the gate. Changing `auth: none` to `auth: apikey` and re-applying therefore
// left the function open, with the deploy printing `(unchanged)` beside it. The
// same held for secrets, env, the response shape, and the `cron:` a schedule is
// declared by.
//
// Hashing the declaration costs what the old comment wanted to avoid — a
// manifest-only edit now rebuilds code that did not change — and that is the
// cheaper mistake by a wide margin. One wasted build against a change that
// silently does not happen.
package atomic_cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// digestVersion is mixed into every digest. Bump it whenever the algorithm
// changes, so a changed algorithm can never read as "unchanged" against
// digests written by an older CLI — every function re-deploys exactly once.
//
// v2 folds the DECLARATION in beside the source. Under v1 a manifest-only change
// — a gate, a secret, a `cron:` — read as unchanged and was skipped, so it never
// reached the operator at all.
const digestVersion = "drift-fn-digest-v2"

// digestSkipDirs are build/runtime artefact directories that must never
// influence a digest: they're regenerated on every build (or by local `drift
// atomic run`) and would otherwise make an unchanged function read as changed.
var digestSkipDirs = map[string]struct{}{
	"node_modules": {},
	"target":       {},
	"__pycache__":  {},
	"dist":         {},
	"build":        {},
	".venv":        {},
	"venv":         {},
	"vendor":       {},
}

// FunctionDigest returns a deterministic, mtime-independent fingerprint of the
// function at dir: every non-hidden source file (relative path + executable bit
// + content, in sorted order) plus the element. Identical trees always hash to
// the same value; any meaningful change flips it.
func FunctionDigest(dir, element string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve function dir: %w", err)
	}

	var rels []string
	walkErr := filepath.Walk(absDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(absDir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if info.IsDir() {
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if _, skip := digestSkipDirs[base]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(base, ".") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk function dir: %w", walkErr)
	}
	sort.Strings(rels)

	h := sha256.New()
	// Version + element header. NUL delimiters everywhere so distinct inputs
	// can never concatenate into the same byte stream.
	fmt.Fprintf(h, "%s\x00element=%s\x00", digestVersion, element)

	for _, rel := range rels {
		full := filepath.Join(absDir, rel)
		info, serr := os.Stat(full)
		if serr != nil {
			return "", serr
		}
		mode := "0"
		if info.Mode()&0o111 != 0 {
			mode = "1" // executable bit can change runtime behaviour
		}
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), mode)
		f, oerr := os.Open(full) // #nosec G304 — hashing the user's own source by design
		if oerr != nil {
			return "", oerr
		}
		if _, cerr := io.Copy(h, f); cerr != nil {
			f.Close() // #nosec G104 -- discarded: copy already failed, this is cleanup
			return "", cerr
		}
		f.Close() // #nosec G104 -- discarded return is intentional; read path, nothing to flush
		fmt.Fprint(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ElementDigest fingerprints an element's package using only the TOP-LEVEL
// non-hidden files in dir — never subdirectories. For a Default element at
// atomic/, the subdirectories are OTHER elements, not part of this package, so
// folding them in would spuriously rebuild the Default when a sibling element
// changes. This matches the build's flat copy (copyGoSourceFiles). The digest
// is element-granular: any source change reflips it, so the whole element
// redeploys (its functions all share the same compiled package). `name` is
// mixed in exactly like FunctionDigest's element label.
func ElementDigest(dir, name string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve element dir: %w", err)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return "", fmt.Errorf("read element dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	h := sha256.New()
	fmt.Fprintf(h, "%s\x00element=%s\x00", digestVersion, name)
	for _, n := range names {
		full := filepath.Join(absDir, n)
		info, serr := os.Stat(full)
		if serr != nil {
			return "", serr
		}
		mode := "0"
		if info.Mode()&0o111 != 0 {
			mode = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00", n, mode)
		f, oerr := os.Open(full) // #nosec G304 — hashing the user's own source by design
		if oerr != nil {
			return "", oerr
		}
		if _, cerr := io.Copy(h, f); cerr != nil {
			f.Close() // #nosec G104
			return "", cerr
		}
		f.Close() // #nosec G104
		fmt.Fprint(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DeclarationDigest fingerprints everything a Driftfile entry TELLS THE
// OPERATOR, as opposed to everything it takes to build the function.
//
// The field list is `operatorSink`'s metadata minus what the build already
// covers: it is the gate, the reply shape, the stream mode, the secrets, the
// env, the element, and the schedule the manifest declares. Get this list out of
// step with what is actually sent and the failure is silent in one direction
// only — a field left out here is a field whose change is skipped.
//
// `dir` is deliberately absent. It says where the source is on THIS machine and
// is never sent, so folding it in would make the same project redeploy for every
// developer who checked it out somewhere else.
//
// Maps and slices are written in sorted order so the same declaration always
// hashes the same way. `secrets` is a set in meaning, and a reordered list that
// forced a redeploy would be a digest reporting a change nobody made.
func DeclarationDigest(spec FunctionSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00decl\x00", digestVersion)

	for _, kv := range [][2]string{
		{"name", spec.Name},
		{"element", spec.Element},
		{"auth", spec.Auth},
		{"stream", spec.Stream},
		{"response", spec.Response},
		{"handler", spec.Handler},
		// The declared schedule, read from the registry the manifest published.
		// It is the one field here that does not live on the spec — see
		// schedules.go for why it travels as a package-level map.
		{"cron", DeclaredScheduleFor(spec.Name)},
	} {
		fmt.Fprintf(h, "%s\x00%s\x00", kv[0], kv[1])
	}

	secrets := append([]string(nil), spec.Secrets...)
	sort.Strings(secrets)
	for _, s := range secrets {
		fmt.Fprintf(h, "secret\x00%s\x00", s)
	}

	envKeys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env\x00%s\x00%s\x00", k, spec.Env[k])
	}

	return hex.EncodeToString(h.Sum(nil))
}

// DeployDigest is what gets RECORDED against a deployed function, and what a
// later deploy compares to decide whether to skip it: the build digest and the
// declaration digest together.
//
// ONE function computes it, called from both sides. The record side and the
// compare side disagreeing by one field is a function that either never skips or
// never redeploys, and both look like the tool working.
func DeployDigest(build string, spec FunctionSpec) string {
	if build == "" {
		// An unknown build digest must never become a matchable value. The empty
		// string is what every caller already treats as "not skippable".
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", digestVersion, build, DeclarationDigest(spec))
	return hex.EncodeToString(h.Sum(nil))
}

// deployedAtomic is the subset of an /ops/atomic/list record needed to match a
// local function against its last-deployed digest.
type deployedAtomic struct {
	FunctionName string `json:"function_name"`
	Method       string `json:"method"`
	Digest       string `json:"digest"`
}

// deployedKey is the identity a deployed record is filed under in the digest
// map — it must equal ElementFunc.DeployKey() for the local function. For HTTP
// that's "<method>:<path>" (so get:x and post:x are distinct); a queue handler's
// stored method is "queue", so its key stays the queue name (the function_name).
func deployedKey(method, functionName string) string {
	if method != "" && method != "queue" {
		return method + ":" + functionName
	}
	return functionName
}

// deployedDigestsTimeout bounds the best-effort "what's already deployed?"
// pre-check. It exists only to SKIP unchanged functions, so it must never
// cost more than it saves: if the list endpoint is slow, we give up quickly
// and deploy everything (the caller treats any error as "skip nothing")
// rather than stalling the whole deploy on the default 30s client timeout.
const deployedDigestsTimeout = 8 * time.Second

// DeployedFunction is one function the slice currently serves, as its slot
// record reports it. Key is the identity the Driftfile uses and the slice books
// against, so a caller compares it against a manifest without rebuilding it.
type DeployedFunction struct {
	Key    string
	Name   string
	Method string
	Digest string
}

// DeployedFunctions returns every function deployed on the active slice.
//
// It is the one decode of `/ops/atomic/list`, and `DeployedDigests` derives from
// it — two consumers, one fetch, one notion of what "deployed" means. Unlike the
// digest map it keeps records carrying no digest, because a function deployed by
// an older CLI is still occupying a slot and still serving.
func DeployedFunctions() ([]DeployedFunction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deployedDigestsTimeout)
	defer cancel()

	resp, err := common.DoRequestWithContext(ctx, http.MethodGet, common.APIBaseURL+"/ops/atomic/list", nil)
	if err != nil {
		return nil, common.TransportError("list atomic functions", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "list atomic functions")
	if err != nil {
		return nil, err
	}

	var records []deployedAtomic
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("list atomic functions: unexpected response (%w)", err)
	}

	out := make([]DeployedFunction, 0, len(records))
	for _, r := range records {
		if r.FunctionName == "" {
			continue // a pre-warmed slot holding nothing
		}
		out = append(out, DeployedFunction{
			Key:    deployedKey(r.Method, r.FunctionName),
			Name:   r.FunctionName,
			Method: r.Method,
			Digest: r.Digest,
		})
	}
	return out, nil
}

// DeployedDigests returns function_name -> last-deployed source digest for the
// active slice. `drift file apply` uses it to skip functions whose source
// is unchanged. Records with no recorded digest (deployed by an older CLI, or
// after a rollback / snapshot restore) are omitted, so they always redeploy.
func DeployedDigests() (map[string]string, error) {
	fns, err := DeployedFunctions()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fns))
	for _, f := range fns {
		if f.Digest != "" {
			out[f.Key] = f.Digest
		}
	}
	return out, nil
}

// DeployedKey is the key a function's last-deployed digest is recorded under —
// the same `method:name` string the Driftfile writes and the slice books
// against, rebuilt from what a deployed record reports.
func DeployedKey(method, name string) string { return deployedKey(method, name) }
