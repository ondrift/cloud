package project

// manifest.go is the parsed Driftfile, as every reader downstream holds it.
//
// The Driftfile is HCL (driftfile.go reads it, compile.go compiles it), and
// what it compiles to is a DOCUMENT: the slice's resources as the platform's
// schema describes them, with `hooks` and `tests` beside them. A Manifest is
// that document, validated, plus where it came from.
//
// ParseDriftfile is the one way to get one. It compiles the file for one of
// its environments (or its default slice), runs the passes that ask this
// machine what the schema cannot (finishParse), and resolves every `$NAME`
// secret reference from the environment, because what reads a Manifest sends
// those values.
//
// What the parser does NOT do:
//
//   - Reach over the network. Live-slice diffing and pricing happen in
//     `drift plan` and `drift apply`.
//   - Apply defaults for envelope knobs. Omitted knobs are passed through as
//     zero values; the platform resolves them.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ─── The Driftfile, as the document the platform defines ─────────────

// Manifest is a parsed Driftfile: the validated DOCUMENT plus where it came from.
//
// There are no Go types for `atomic`, `backbone`, `canvas` or anything else the
// document contains. A struct would be a second definition of what the platform
// already publishes as its schema, so the CLI holds the document as it is and
// reads the handful of values it acts on locally (a directory to upload, a
// secret to send) through total accessors.
type Manifest struct {
	// doc is the whole compiled, schema-validated document.
	doc Node

	// slice is the resource shape: the document root minus the siblings that
	// are about deployment rather than shape, with every backend's Functions and
	// Workers flattened into the two lists every reader consumes.
	slice Node

	// baseDir is the Driftfile's directory; relative paths resolve against it.
	baseDir string
}

// sliceSiblings are the root keys that are NOT part of the resource shape.
var sliceSiblings = []string{"hooks", "tests"}

// Doc is the whole document.
func (m *Manifest) Doc() Node { return m.doc }

// Slice is the resource shape. Accessors are total, which is safe because the
// schema has already run.
func (m *Manifest) Slice() Node { return m.slice }

// sliceKey is the root key naming the slice this file is applied to.
const sliceKey = "slice"

// Name is the slice this file is applied to: the slice block's, or the
// selected environment's.
func (m *Manifest) Name() string { return m.slice.Str(sliceKey) }

// BaseDir is the directory the Driftfile was read from.
func (m *Manifest) BaseDir() string { return m.baseDir }

// PreDeploy / PostDeploy are the local shell commands run around a deploy.
func (m *Manifest) PreDeploy() []string  { return m.doc.Strings("hooks", "pre_deploy") }
func (m *Manifest) PostDeploy() []string { return m.doc.Strings("hooks", "post_deploy") }

// E2ETests are the commands `drift file test` runs against a local instance.
func (m *Manifest) E2ETests() []string { return m.doc.Strings("tests", "e2e") }

type ParseErrors []string

func (p ParseErrors) Error() string {
	if len(p) == 1 {
		return p[0]
	}
	return fmt.Sprintf("%d validation errors:\n  - %s", len(p), strings.Join(p, "\n  - "))
}

// ParseDriftfile compiles the Driftfile at path for its default slice and
// resolves its secret references. Relative paths resolve against the file's
// directory.
func ParseDriftfile(path string) (*Manifest, error) {
	return ParseDriftfileEnv(path, "")
}

// ParseDriftfileEnv is ParseDriftfile for one of the file's environments: its
// variables take that environment's values, and the manifest names that
// environment's slice. env "" is the default slice.
func ParseDriftfileEnv(path, env string) (*Manifest, error) {
	c, err := CompileDriftfileEnv(path, env)
	if err != nil {
		return nil, err
	}
	// Resolving a reference is not validation: it substitutes a value from the
	// deployer's environment, and reports the ones that are not set, because a
	// secret silently becoming the literal "$VAR" is a credential that is wrong
	// in a way nothing downstream can detect.
	if err := resolveSecretEnvRefs(c.Manifest); err != nil {
		return nil, err
	}
	return c.Manifest, nil
}

// finishParse runs every pass that follows schema validation on a compiled
// document: the backends flattened, each Function's identity composed, the slots
// checked against one another, and the questions only this machine can
// answer.
func finishParse(m *Manifest) error {
	m.slice = m.doc.Clone()
	for _, sibling := range sliceSiblings {
		delete(m.slice, sibling)
	}

	// Every backend's Functions and Workers, flattened into the two lists every
	// reader consumes. See backends.go.
	if err := normaliseBackends(m.slice); err != nil {
		return err
	}

	// A Function's identity, its route and method, composed into the one string
	// the platform books against.
	normaliseFunctionIdentities(m.doc)
	normaliseFunctionIdentities(m.slice)

	// Two slots on one booking key, or a Worker named like a Function route,
	// anywhere in the slice. Refused here, before anything is built.
	if err := checkSlotIdentities(m.slice); err != nil {
		return err
	}

	// The one class of check the schema CANNOT own: whether the files a valid
	// document names hold what they must. A schema describes the document; only
	// the laptop can open a seed file.
	if errs := checkLocalPaths(m); len(errs) > 0 {
		return errs
	}

	// The other class the schema cannot own: a reference from one part of the
	// document to another. A queue-triggered Function names a queue, and only a
	// `queue` block declares one.
	//
	// A warning, not a refusal: the slice creates an undeclared queue on its
	// first push, so the Function does fire and stopping the deploy would be
	// wrong. See queuerefs.go.
	for _, w := range checkQueueReferences(m) {
		fmt.Fprintf(os.Stderr, "  %s %s\n", common.Hint("·"), w)
	}
	return nil
}

// checkLocalPaths verifies that a NoSQL seed holds records that load. The
// compiler refuses a missing path at the attribute that names it; whether a
// seed's records can be ingested is the one question only reading the seed
// answers.
//
// The distinction to hold on to when adding anything here: this may ask the
// FILESYSTEM a question. It may never ask whether a value is well-formed: that
// is the schema's.
func checkLocalPaths(m *Manifest) ParseErrors {
	var errs ParseErrors
	for _, c := range m.slice.Entries("slot", "backbone", "nosql") {
		seed := c.Str("seed")
		if seed == "" {
			continue
		}
		seedPath := resolveBaseDir(m, seed)
		if _, err := os.Stat(seedPath); err != nil {
			errs = append(errs, fmt.Sprintf("nosql %q: seed file not found at %s", c.Str("slot"), seedPath))
			continue
		}
		errs = append(errs, validateJSONLSeed(c.Str("slot"), seedPath)...)
	}

	sort.Strings(errs) // a stable message order is testable
	return errs
}

// validateJSONLSeed reads a seed file and reports the records that cannot be
// loaded. Filesystem-only: the schema knows the seed is a path string, and
// nothing more can be known about it without opening the file.
func validateJSONLSeed(collection, path string) []string {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return []string{fmt.Sprintf("nosql %q seed: read %s: %v", collection, path, err)}
	}

	var errs []string
	for i, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: invalid JSON: %v", collection, path, i+1, err))
			continue
		}
		id, ok := doc["_id"]
		if !ok {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: missing _id", collection, path, i+1))
			continue
		}
		// Treat empty string and nil as "missing".
		if s, isStr := id.(string); (isStr && s == "") || id == nil {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: empty _id", collection, path, i+1))
		}
	}
	return errs
}

// ─── What runs before the compile ───────────────────────────────────

// ParseHooks reads a Driftfile's hooks without compiling the rest, so a
// pre_deploy build can make what the compile then checks for: a site's folder,
// a seed file.
func ParseHooks(path string) (pre, post []string, err error) {
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	file, ev, err := readDriftfile(path, data)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range file.Hooks {
		run, ok := ev.str(h.Run, "hook: run")
		if !ok {
			continue
		}
		switch h.Phase {
		case "pre_deploy":
			pre = append(pre, run)
		case "post_deploy":
			post = append(post, run)
		}
	}
	if ev.diags.HasErrors() {
		return nil, nil, diagError(ev.diags)
	}
	return pre, post, nil
}

// ParseTests reads a Driftfile's test commands without compiling the rest, so
// `drift file test` can say there are none before it builds anything.
func ParseTests(path string) ([]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	file, ev, err := readDriftfile(path, data)
	if err != nil {
		return nil, err
	}
	var runs []string
	for _, t := range file.Tests {
		if run, ok := ev.str(t.Run, fmt.Sprintf("test %q: run", t.Name)); ok {
			runs = append(runs, run)
		}
	}
	if ev.diags.HasErrors() {
		return nil, diagError(ev.diags)
	}
	return runs, nil
}

// ParseProjectName reads ONLY the name of the slice a Driftfile is applied to,
// for its default slice or one environment: no evaluation, no schema, no
// secrets, so `drift file stop` and `logs` find the container without the
// project's secrets being set.
//
// An environment's slice is the one its block names, when that is written as a
// plain string, else `<slice>-<env>`, exactly as the compiler names it.
func ParseProjectName(path, env string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if isYAML(data) {
		return "", refuseYAML(path)
	}
	name := filepath.Base(path)
	f, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return "", diagError(diags)
	}
	blocks := f.Body.(*hclsyntax.Body).Blocks

	base := ""
	for _, b := range blocks {
		if b.Type == "slice" && len(b.Labels) == 1 {
			base = b.Labels[0]
			break
		}
	}
	if base == "" {
		return "", fmt.Errorf(`%s names no slice: add slice "<name>" {}`, name)
	}
	if env == "" {
		return base, nil
	}

	for _, b := range blocks {
		if b.Type != "environment" || len(b.Labels) != 1 || b.Labels[0] != env {
			continue
		}
		attr, ok := b.Body.Attributes["slice"]
		if !ok {
			return deriveSliceName(base, env), nil
		}
		v, d := attr.Expr.Value(nil)
		if d.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
			return "", fmt.Errorf("environment %q names its slice with an expression, which is only read by a full parse; "+
				"write it as a string", env)
		}
		return v.AsString(), nil
	}
	return "", fmt.Errorf("there is no environment %q in %s", env, name)
}

// SchemaAvailable reports whether this machine holds the platform's Driftfile
// schema, so a caller that must not silently validate nothing can say so.
func SchemaAvailable() bool {
	sch, err := loadSchema()
	return err == nil && sch != nil
}

// deriveSliceName maps (slice name, environment) to the environment's slice.
// The production environment owns the bare name; every other environment gets
// a `-<env>` suffix, so its slice is a distinct, separately-billed instance.
func deriveSliceName(base, env string) string {
	if env == "" || env == "prod" || env == "production" {
		return base
	}
	return base + "-" + env
}

// ─── Secret reference resolution ────────────────────────────────────

// resolveSecretEnvRefs replaces each `$NAME` secret reference in the slice
// with the value of NAME in the environment, so what reads the Manifest holds
// what the platform should store. Every reference that is not set is reported
// at once.
func resolveSecretEnvRefs(m *Manifest) error {
	secrets := m.Slice().Sub("backbone", "secrets")
	if len(secrets) == 0 {
		return nil
	}
	missing := []string{}
	for k, v := range secrets.StrMap() {
		if !strings.HasPrefix(v, "$") {
			continue
		}
		envName := strings.TrimPrefix(v, "$")
		envVal, ok := os.LookupEnv(envName)
		if !ok {
			missing = append(missing, fmt.Sprintf("secret %q: environment variable %s is not set", k, envName))
			continue
		}
		secrets[k] = envVal
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return ParseErrors(missing)
	}
	return nil
}

// resolveBaseDir resolves a possibly-relative path against the
// Driftfile's directory, leaving absolute paths untouched.
func resolveBaseDir(m *Manifest, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	if m.baseDir == "" {
		return rel
	}
	return filepath.Join(m.baseDir, rel)
}

// ResolvePath is the exported sibling of resolveBaseDir, used by the
// run driver after parse to find files referenced by the manifest.
func (m *Manifest) ResolvePath(rel string) string { return resolveBaseDir(m, rel) }
