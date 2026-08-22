// references.go — does the slice actually hold what the manifest names?
//
// The Driftfile references resources by NAME because the code does:
// `backbone.nosql: [{name: sessions}]` exists because something calls
// `NoSQL.Write("sessions", …)`. A write to a collection or bucket the slice
// does not declare is refused by the slice with a 400, which lands at RUNTIME,
// on a live slice, after the upload. This turns that into a refusal the
// developer sees on their own terminal, before anything is sent.
//
// It reports every miss at once. A check that stops at the first one turns a
// single fix into as many deploy attempts as there are missing names.
package project

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// CheckSliceReferences refuses a manifest naming a function, collection, bucket
// or database that live does not declare.
//
// Comparison is EXACT, never case-insensitive. The runtime finds a function's
// pool by exact string lookup against a lowercase-method key, so a declared name
// that matches only in case is a pool no invocation ever reaches — calling it
// "declared" would hide the exact failure this check exists to surface.
//
// An EMPTY declared set for a class skips that class. That is the same escape
// hatch the slice reads as "no declaration model in play", and the same reason a
// slice predating per-function memory stays deployable: it declares none, and
// refusing those would strand every one of them on its next deploy.
//
// Three classes are deliberately NOT checked, because the slice's config carries
// no names for them — canvas sites (a total size), queues (a per-queue depth)
// and secrets (a count). The refusal names the classes it checked rather than
// claiming to be complete.
func CheckSliceReferences(m *Manifest, live *LiveSlice) error {
	if live == nil {
		return fmt.Errorf("slice %q does not exist — create it first with `drift slice create %s`, then deploy into it",
			m.Name(), m.Name())
	}

	declaredFunctions := make(map[string]bool, len(live.Config.Atomic.Functions))
	for _, f := range live.Config.Atomic.Functions {
		declaredFunctions[f.Name] = true
	}

	var missing []referenceMiss
	missing = append(missing, missesIn("function", namedFunctions(m), declaredFunctions)...)
	missing = append(missing, missingSecrets(m)...)
	missing = append(missing, missesIn("nosql collection",
		entryNames(m, "slot", "backbone", "nosql"), keySet(live.Config.Backbone.NoSQL.Collections))...)
	missing = append(missing, missesIn("blob bucket",
		entryNames(m, "name", "backbone", "blobs"), keySet(live.Config.Backbone.Blobs.Buckets))...)
	missing = append(missing, missesIn("sql database",
		entryNames(m, "name", "backbone", "sql"), keySet(live.Config.Backbone.SQL.Databases))...)

	if len(missing) == 0 {
		return nil
	}
	return referenceError(m.Name(), missing)
}

// missingSecrets reports every secret a function declares that will not exist
// when it runs.
//
// The runner fetches exactly the names a function declares and SILENTLY SKIPS
// any the slice does not hold, so a typo is an absent environment variable at
// the first invocation and no error anywhere.
//
// The referee is the UNION of two sets, and it has to be:
//
//   - the slice's live secrets, because one set once with `drift backbone secret
//     set` and never named in a manifest is an ordinary pattern, not a fault;
//   - this manifest's own `backbone.secrets`, because those are not on the slice
//     yet at check time — applyBackbone and applyAtomic run concurrently, so the
//     live list alone would refuse a project that sets its secrets in the very
//     deploy being checked.
//
// Only names are read. `list` is called, never `get`, and no value is fetched,
// printed or compared.
func missingSecrets(m *Manifest) []referenceMiss {
	allowed := map[string]bool{}
	for name := range m.Slice().StrMap("backbone", "secrets") {
		allowed[name] = true
	}
	live, err := fetchLiveSecretNames()
	if err != nil {
		// Unreadable is not absent. Refusing here would fail a deploy over a
		// transient list call, and the runtime behaviour this check exists to
		// pre-empt is unchanged by skipping it.
		fmt.Printf("  %s couldn't check the secrets your functions declare: %v\n",
			common.Hint("·"), err)
		return nil
	}
	for _, name := range live {
		allowed[name] = true
	}

	var out []referenceMiss
	seen := map[string]bool{}
	for _, s := range FunctionSpecs(m) {
		for _, name := range s.Secrets {
			key := s.Name + "\x00" + name
			if name == "" || allowed[name] || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, referenceMiss{
				Class: "secret",
				Name:  name,
				// Secrets are the one class the slice form does not own, so the
				// shared closing line would send the reader to the wrong place.
				Remedy: fmt.Sprintf("declared by %s — set it with `drift backbone secret set %s=…`, "+
					"or add it to this file's `backbone.secrets`", s.Name, name),
			})
		}
	}
	return out
}

// fetchLiveSecretNames lists the secret NAMES the slice holds. Names only —
// this path must never reach for a value.
func fetchLiveSecretNames() ([]string, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/backbone/secret/list", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list secrets")
	if err != nil {
		return nil, err
	}
	// The route has answered both a bare array of names and a list of objects
	// carrying one; accept either rather than pinning to the shape of the day.
	var names []string
	if err := json.Unmarshal(body, &names); err == nil {
		return names, nil
	}
	var objs []struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(body, &objs); err != nil {
		return nil, fmt.Errorf("read the slice's secret list: %w", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		if o.Name != "" {
			out = append(out, o.Name)
		} else if o.Key != "" {
			out = append(out, o.Key)
		}
	}
	return out, nil
}

// referenceMiss is one name the manifest uses and the slice does not have.
// NearMiss carries a declared name differing only in case, which is the failure
// most worth naming outright: it looks declared and reaches nothing.
type referenceMiss struct {
	Class    string
	Name     string
	NearMiss string
	// Remedy overrides the block's shared closing advice for a class the
	// slice form does not own.
	Remedy string
}

// missesIn reports the referenced names absent from declared, preserving the
// order the document lists them in. An empty declared set means the class is not
// under a declaration model and nothing is missing.
func missesIn(class string, referenced []string, declared map[string]bool) []referenceMiss {
	if len(declared) == 0 {
		return nil
	}
	lowered := make(map[string]string, len(declared))
	for name := range declared {
		lowered[strings.ToLower(name)] = name
	}

	var out []referenceMiss
	seen := map[string]bool{}
	for _, name := range referenced {
		if name == "" || declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, referenceMiss{Class: class, Name: name, NearMiss: lowered[strings.ToLower(name)]})
	}
	return out
}

// namedFunctions reads function identities through FunctionSpecs rather than off
// the raw node, so the later route+method split moves one join site and leaves
// this file alone.
func namedFunctions(m *Manifest) []string {
	specs := FunctionSpecs(m)
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// entryNames collects one class's declared identifiers. The key is a parameter
// because the classes do not share one: a NoSQL entry names the `slot` it seeds,
// while a bucket and a database still carry `name`.
func entryNames(m *Manifest, key string, path ...string) []string {
	entries := m.Slice().Entries(key, path...)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSpace(e.Str(key)))
	}
	return out
}

func keySet(m map[string]int) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// referenceError renders every miss in one block, grouped by class in the order
// they were checked, so one pass over the resize form fixes the whole list.
func referenceError(sliceName string, missing []referenceMiss) error {
	byClass := map[string][]referenceMiss{}
	var order []string
	for _, miss := range missing {
		if _, seen := byClass[miss.Class]; !seen {
			order = append(order, miss.Class)
		}
		byClass[miss.Class] = append(byClass[miss.Class], miss)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "your Driftfile names %s that slice %q does not have:",
		plural(len(missing), "a resource", "resources"), sliceName)
	for _, class := range order {
		items := byClass[class]
		sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		for _, miss := range items {
			fmt.Fprintf(&b, "\n  - %s %q", miss.Class, miss.Name)
			if miss.Remedy != "" {
				fmt.Fprintf(&b, " — %s", miss.Remedy)
			}
			if miss.NearMiss != "" {
				fmt.Fprintf(&b, " — the slice declares %q, which differs only in case; "+
					"the runtime matches this name exactly, so %q reaches nothing",
					miss.NearMiss, miss.NearMiss)
			}
		}
	}
	// The command that declares them, named with the slice already in it. This
	// message is read by someone who knows what they meant to declare and needs
	// the one place to declare it, so it states the whole command rather than
	// the verb.
	fmt.Fprintf(&b, "\n\nAdd them with `drift slice resize %s`, then deploy again. "+
		"Checked: functions, nosql collections, blob buckets, sql databases and secrets.",
		sliceName)
	return fmt.Errorf("%s", b.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ReportOrphanedFunctions prints every function deployed on the slice that the
// manifest no longer names.
//
// A rename produces an orphan, not a move: the slot is matched on (name,
// method), so the old record keeps its own and nothing on the apply path
// removes it. It keeps serving — the slice re-registers every slot directory it
// finds at boot — it keeps consuming one of the function slots the slice was
// sold, and because the new config names only the new key, it falls to the
// shared pool with no error, no log line and no metric.
//
// IT REPORTS. It does not refuse and it does not delete. Nothing anywhere
// records which project owns a function, so once a slice reference is a plain
// name two Driftfiles can legitimately share one slice — and a reconcile that
// cannot tell "not mine" from "deleted" must do neither. Refusing would make the
// second project undeployable; deleting would delete the first project's work.
func ReportOrphanedFunctions(m *Manifest, deployed []atomic_cmd.DeployedFunction, out io.Writer) {
	named := map[string]bool{}
	for _, s := range FunctionSpecs(m) {
		named[atomic_cmd.DeployedKey(specMethodAndPath(s.Name))] = true
	}

	var orphans []atomic_cmd.DeployedFunction
	for _, d := range deployed {
		if !named[d.Key] {
			orphans = append(orphans, d)
		}
	}
	if len(orphans) == 0 {
		return
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Key < orphans[j].Key })

	fmt.Fprintf(out, "\n  %s %s deployed on this slice that your Driftfile no longer names:\n",
		common.Hint("·"), plural(len(orphans), "One function is", "Functions are"))
	for _, o := range orphans {
		fmt.Fprintf(out, "      %s — still serving, still holding one of the slice's function slots\n", o.Key)
		fmt.Fprintf(out, "        free it with: drift atomic delete %s\n", o.Name)
	}
	fmt.Fprintf(out, "    Nothing was removed: a slice can serve more than one project, and a\n"+
		"    deploy cannot tell a function that is not yours from one you deleted.\n")
}

// specMethodAndPath splits a manifest function identity back into the method and
// path a deployed record reports, so both sides build their key with one
// function. A queue handler's identity has no HTTP method and keys on its name.
func specMethodAndPath(name string) (string, string) {
	method, path, found := strings.Cut(name, ":")
	if !found {
		return "", name
	}
	return method, path
}
