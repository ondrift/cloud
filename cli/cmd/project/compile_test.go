package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// realHome is HOME before any test replaces it, read once at package start:
// it holds the schema the platform last served, which every Driftfile is
// validated against. CI fetches the live schema there before the suite runs.
var realHome = os.Getenv("HOME")

// withRealSchema gives the test a scratch HOME holding the platform's schema,
// so a Driftfile is validated exactly as it is on a laptop that has met the
// api. TestSchemaMustBePresentOrTheSuiteIsTheatre makes its absence loud.
func withRealSchema(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(realHome, ".drift", "driftfile.schema.json"))
	if err != nil {
		t.Fatalf("no platform schema at ~/.drift/driftfile.schema.json (run any online drift command once): %v", err)
	}
	home := os.Getenv("HOME")
	if home == realHome {
		home = t.TempDir()
		t.Setenv("HOME", home)
	}
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })
}

// writeDriftfile lays out a Driftfile project in a temp dir: the file, and every
// directory or file it names (a path ending in / is a directory).
func writeDriftfile(t *testing.T, body string, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// planManifest parses body as a Driftfile laid out by writeDriftfile, with the given
// directories and files beside it.
func planManifest(t *testing.T, body string, paths ...string) *Manifest {
	t.Helper()
	m, err := ParseDriftfile(writeDriftfile(t, body, paths...))
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}

// ─── a YAML Driftfile ───────────────────────────────────────────────────────

// A YAML Driftfile is recognised by its first tokens, past comments: a `key:`
// pair, or a `---` marker. The HCL controls, a file that begins with a block,
// an attribute, a comment or a syntax error, are what stop the check from
// reading every file as YAML.
func TestIsYAML_ReadsTheFirstTokens(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"slice: demo\n", true},
		{"# a comment\n\nname: demo\natomic:\n  pulses: []\n", true},
		{"---\nslice: demo\n", true},
		{"slice \"demo\" {}\n", false},
		{"# a comment\n// another\nslice \"demo\" {\n}\n", false},
		{"variable \"m\" { default = \"32MiB\" }\n", false},
		{"slice \"demo\" {\n  labels = ", false}, // broken HCL is HCL, reported at its line
		{"", false},
	} {
		if got := isYAML([]byte(tc.text)); got != tc.want {
			t.Errorf("isYAML(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// Every reader refuses a YAML Driftfile with the one message saying the format
// changed, rather than an HCL syntax error about a colon.
func TestEveryReaderRefusesAYAMLDriftfileTheSameWay(t *testing.T) {
	withRealSchema(t)
	path := writeDriftfile(t, "slice: demo\natomic:\n  pulses: []\n")
	readers := map[string]func() error{
		"compile": func() error { _, err := CompileDriftfile(path); return err },
		"parse":   func() error { _, err := ParseDriftfile(path); return err },
		"hooks":   func() error { _, _, err := ParseHooks(path); return err },
		"tests":   func() error { _, err := ParseTests(path); return err },
		"name":    func() error { _, err := ParseProjectName(path, ""); return err },
	}
	for name, read := range readers {
		err := read()
		if err == nil || !strings.Contains(err.Error(), "written in YAML") ||
			!strings.Contains(err.Error(), "the Driftfile is HCL now") || !strings.Contains(err.Error(), "drift file new") {
			t.Errorf("%s: got %v, want the refusal saying the Driftfile is HCL now", name, err)
		}
	}

	// The control: the same readers accept the same slice written in HCL.
	hcl := writeDriftfile(t, "slice \"demo\" {}\n")
	for name, read := range map[string]func() error{
		"compile": func() error { _, err := CompileDriftfile(hcl); return err },
		"name":    func() error { _, err := ParseProjectName(hcl, ""); return err },
	} {
		if err := read(); err != nil {
			t.Errorf("%s refused an HCL file: %v", name, err)
		}
	}
}

// A file with no slice block says what one looks like, at the top of the file.
func TestCompile_AFileWithNoSliceBlockIsRefusedAtItsStart(t *testing.T) {
	withRealSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, "backbone {\n  queue \"jobs\" {}\n}\n"))
	if err == nil || !strings.Contains(err.Error(), "Driftfile:1:1: Missing slice block") {
		t.Fatalf("got %v, want a refusal at 1:1 naming the slice block", err)
	}
}

// The element named "default" is ./atomic itself, where `drift atomic new`
// writes without --element; any other element is ./atomic/<name>. Its slots
// carry no element, as the build expects of the default one.
func TestCompile_TheDefaultElementIsTheAtomicFolderItself(t *testing.T) {
	withRealSchema(t)
	body := "slice \"demo\" {}\n" +
		"atomic {\n  element \"default\" {\n    pulse \"GET /hello\" {\n      handler = \"GetHello\"\n      memory = \"32MiB\"\n    }\n  }\n}\n"
	path := writeDriftfile(t, body, "atomic/")
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatalf("the default element in ./atomic was refused: %v", err)
	}
	spec := FunctionSpecs(c.Manifest)[0]
	if spec.Element != "" || spec.Dir != filepath.Join(filepath.Dir(path), "atomic") {
		t.Errorf("the default element's pulse is in element %q, dir %s; want no element and ./atomic", spec.Element, spec.Dir)
	}

	// The control: a named element is ./atomic/<name>, so ./atomic alone does
	// not hold it.
	named := strings.Replace(body, `element "default"`, `element "api"`, 1)
	if _, err := CompileDriftfile(writeDriftfile(t, named, "atomic/")); err == nil || !strings.Contains(err.Error(), "atomic/api is not a directory") {
		t.Errorf("a named element without its folder: got %v", err)
	}
}

// A pillar's block written at the top of the file is refused naming the
// container it belongs in, at the block's own position.
func TestCompile_APillarBlockAtTheTopIsRefusedNamingItsContainer(t *testing.T) {
	withRealSchema(t)
	for _, tc := range []struct{ block, pillar string }{
		{"element \"api\" {}", "atomic"},
		{"sql \"db\" {}", "backbone"},
		{"nosql \"docs\" {}", "backbone"},
		{"blobs \"files\" {}", "backbone"},
		{"queue \"jobs\" {}", "backbone"},
		{"cache \"k\" {}", "backbone"},
		{"secret \"S\" {}", "backbone"},
		{"realtime {}", "backbone"},
		{"site \"/\" {}", "canvas"},
	} {
		_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n"+tc.block+"\n"))
		want := "inside the " + tc.pillar + " block"
		if err == nil || !strings.Contains(err.Error(), "Driftfile:2:1") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s at the top: got %v, want a refusal at 2:1 saying %q", tc.block, err, want)
		}
	}
}

// Two blocks of one pillar are one too many: the container is the pillar.
func TestCompile_AContainerIsWrittenOnce(t *testing.T) {
	withRealSchema(t)
	for _, name := range []string{"atomic", "backbone", "canvas"} {
		body := "slice \"demo\" {}\n" + name + " {}\n" + name + " {}\n"
		_, err := CompileDriftfile(writeDriftfile(t, body))
		if err == nil || !strings.Contains(err.Error(), "Driftfile:3:1") || !strings.Contains(err.Error(), "has one "+name+" block") {
			t.Errorf("two %s blocks: got %v, want a refusal at 3:1", name, err)
		}
	}
}

// A `drift {}` block is not part of the format: the file states nothing about
// its format, and an unknown block is refused like any other.
func TestCompile_ADriftBlockIsNotPartOfTheFormat(t *testing.T) {
	withRealSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, "drift {\n  format = \"2.0\"\n}\nslice \"demo\" {}\n"))
	if err == nil || !strings.Contains(err.Error(), "Driftfile:1:1") || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("got %v, want the drift block refused at 1:1", err)
	}
}

// ─── the fixture, compiled ──────────────────────────────────────────────────

// compiledGolden is what the fxevm fixture compiles to, in a form that does
// not depend on the machine: directories relative to the Driftfile.
type compiledGolden struct {
	FitsFree  bool           `json:"fitsFree"`
	Document  Node           `json:"document"`
	Slots     []goldenSpec   `json:"slots"`
	Shape     SliceConfig    `json:"shape"`
	WriteOnly []WriteOnlyRef `json:"write_only"`
}

type goldenSpec struct {
	Name, Handler, Element, Dir, Auth, Response string
	Concurrent                                  bool
}

// The fxevm testnode, compiled. The golden is the CONTRACT between the format
// and everything downstream: the document every reader consumes and the shape
// the platform prices. Regenerate with DRIFT_UPDATE_GOLDEN=1 and read the diff.
func TestCompile_FxevmGolden(t *testing.T) {
	withRealSchema(t)
	base, _ := filepath.Abs(filepath.Join("testdata", "fxevm"))
	c, err := CompileDriftfile(filepath.Join(base, "Driftfile"))
	if err != nil {
		t.Fatalf("the fxevm fixture does not compile:\n%v", err)
	}

	g := compiledGolden{FitsFree: c.FitsFree, Document: c.Manifest.Doc(), Shape: c.Shape, WriteOnly: c.WriteOnly}
	for _, s := range AtomicSpecs(c.Manifest) {
		rel, _ := filepath.Rel(base, s.Dir)
		g.Slots = append(g.Slots, goldenSpec{s.Name, s.Handler, s.Element, rel, s.Auth, s.Response, s.Concurrent})
	}
	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(base, "compiled.golden.json")
	if os.Getenv("DRIFT_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, append(got, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read the golden (DRIFT_UPDATE_GOLDEN=1 writes it): %v", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(got)) {
		t.Errorf("the fxevm fixture compiles to something else now.\n--- got ---\n%s\n--- want (%s) ---\n%s", got, golden, want)
	}
}

// The compiled document gives every reader the answer the file means.
// Asserted on the readers rather than on the golden, so a golden regenerated
// carelessly cannot hide a document the deploy reads wrong.
func TestCompile_TheReadersReadWhatTheFileSays(t *testing.T) {
	withRealSchema(t)
	c, err := CompileDriftfile(filepath.Join("testdata", "fxevm", "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Manifest
	if m.Name() != "fxevm-testnode" {
		t.Errorf("slice name = %q", m.Name())
	}
	names := map[string]string{}
	for _, s := range AtomicSpecs(m) {
		names[s.Name] = s.Element + "/" + s.Handler + "/" + s.Auth
	}
	want := map[string]string{
		"post:rpc":         "evm/PostRPC/",
		"post:admin/reset": "evm/PostReset/apikey",
		"get:bench":        "bench/GetBench/",
		"field:sequencer":  "evm/Sequencer/",
	}
	for k, v := range want {
		if names[k] != v {
			t.Errorf("slot %s = %q, want %q (all: %v)", k, names[k], v, names)
		}
	}
	if got := m.Slice().Sub("edge", "site_auth").Strings("paths"); len(got) != 1 || got[0] != "*" {
		t.Errorf("site_auth paths = %v, want [*]", got)
	}
	users := m.Slice().Sub("edge", "site_auth").Nodes("users")
	if len(users) != 1 || users[0].Str("password") != "$EXPLORER_PASS" {
		t.Errorf("site_auth users = %v, want one user whose password is the reference $EXPLORER_PASS", users)
	}
	if got := DeclaredQueues(m); len(got) != 1 || got[0] != "txs" {
		t.Errorf("queues = %v", got)
	}
	if got := m.E2ETests(); len(got) != 1 {
		t.Errorf("tests = %v", got)
	}
}

// ParseDriftfile is the compile with every secret reference resolved, because
// what reads a Manifest sends those values. CompileDriftfile never reads one.
func TestParseDriftfile_ResolvesTheReferencesTheCompileKeeps(t *testing.T) {
	withRealSchema(t)
	t.Setenv("STRIPE_KEY", "sk_test_value")
	path := writeDriftfile(t, "slice \"demo\" {}\nbackbone {\n  secret \"STRIPE_KEY\" { value = env(\"STRIPE_KEY\") }\n}\n")
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Slice().Str("backbone", "secrets", "STRIPE_KEY"); got != "sk_test_value" {
		t.Errorf("ParseDriftfile left the secret as %q, want its value", got)
	}
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Manifest.Slice().Str("backbone", "secrets", "STRIPE_KEY"); got != "$STRIPE_KEY" {
		t.Errorf("the compile read the secret's value: %q", got)
	}
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// ─── write-only values ──────────────────────────────────────────────────────

// A literal where a secret, an API key or a site password belongs is a
// credential committed with the file, and is refused at its position. var.x
// and file() put the value in the repository too, and are refused the same.
func TestCompile_RefusesAWriteOnlyValueThatIsNotAReference(t *testing.T) {
	withRealSchema(t)
	for _, tc := range []struct{ name, body string }{
		{"secret literal", "backbone {\n  secret \"STRIPE_KEY\" { value = \"sk_live_123\" }\n}"},
		{"secret from var", "variable \"k\" { default = \"x\" }\nbackbone {\n  secret \"STRIPE_KEY\" { value = var.k }\n}"},
		{"api key literal", "atomic {\n  element \"api\" {\n    pulse \"POST /x\" {\n      handler = \"PostX\"\n      memory = \"32MiB\"\n      auth { api_key = \"abc\" }\n    }\n  }\n}"},
		{"site password literal", "edge {\n  site_auth {\n    paths = [\"*\"]\n    user \"a\" { password = \"hunter2\" }\n  }\n}"},
		{"site password from file", "edge {\n  site_auth {\n    paths = [\"*\"]\n    user \"a\" { password = file(\"pw\") }\n  }\n}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeDriftfile(t, "slice \"demo\" {}\n"+tc.body+"\n", "atomic/api/", "pw")
			_, err := CompileDriftfile(path)
			if err == nil {
				t.Fatal("a write-only value written as something other than env(\"NAME\") compiled")
			}
			if !strings.Contains(err.Error(), "A secret is a reference") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
			if !regexp.MustCompile(`Driftfile:\d+:\d+: `).MatchString(err.Error()) {
				t.Errorf("the refusal carries no file:line:column: %v", err)
			}
		})
	}
}

// The control: env("NAME") compiles, to a REFERENCE. The value is never read
// into the document, so it cannot reach any output.
func TestCompile_AWriteOnlyReferenceCompilesWithoutItsValue(t *testing.T) {
	withRealSchema(t)
	t.Setenv("STRIPE_KEY", "sk_live_do_not_print")
	path := writeDriftfile(t, "slice \"demo\" {}\nbackbone {\n  secret \"STRIPE_KEY\" { value = env(\"STRIPE_KEY\") }\n}\n")
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Manifest.Slice().Str("backbone", "secrets", "STRIPE_KEY"); got != "$STRIPE_KEY" {
		t.Errorf("the document carries %q for the secret, want the reference $STRIPE_KEY", got)
	}
	raw, _ := json.Marshal(struct {
		D Node
		S SliceConfig
	}{c.Manifest.Doc(), c.Shape})
	if strings.Contains(string(raw), "sk_live_do_not_print") {
		t.Error("a secret's VALUE reached a compiled payload")
	}
}

// ─── expressions ────────────────────────────────────────────────────────────

// Literals, var.*, env(), file() and sha256(), and nothing else.
func TestCompile_ExpressionsAreSmall(t *testing.T) {
	withRealSchema(t)
	t.Setenv("RPC_HANDLER", "PostRPC")
	body := `
variable "rpc_memory" { default = "48MiB" }
slice "demo" {}
atomic {
  element "api" {
    pulse "POST /rpc" {
      handler = env("RPC_HANDLER")
      memory  = var.rpc_memory
      env     = { GENESIS = sha256(file("genesis.sql")), NOTE = "literal" }
    }
  }
}
`
	path := writeDriftfile(t, body, "atomic/api/", "genesis.sql")
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f := c.Shape.Atomic.Functions; len(f) != 1 || f[0].MemoryBytes != 48*bytesPerMiB {
		t.Errorf("var.rpc_memory did not reach the booking: %+v", f)
	}
	spec := FunctionSpecs(c.Manifest)[0]
	if spec.Handler != "PostRPC" {
		t.Errorf("env() handler = %q", spec.Handler)
	}
	// writeDriftfilegives every file the content "x\n".
	sum := sha256.Sum256([]byte("x\n"))
	if got, want := spec.Env["GENESIS"], hex.EncodeToString(sum[:]); got != want {
		t.Errorf("sha256(file()) = %q, want %q", got, want)
	}
	if spec.Env["NOTE"] != "literal" {
		t.Errorf("a literal env value = %q", spec.Env["NOTE"])
	}

	// Anything else is not a function this format has.
	for _, expr := range []string{`upper("x")`, `timestamp()`, `lookup({}, "a", "b")`} {
		path := writeDriftfile(t, "slice \"demo\" {\n  labels = { a = "+expr+" }\n}\n")
		_, err := CompileDriftfile(path)
		if err == nil || !strings.Contains(err.Error(), "no function named") {
			t.Errorf("%s compiled, or failed for another reason: %v", expr, err)
		}
	}
}

// An env() that names an unset variable is refused at its position, and says
// where a value can come from.
func TestCompile_AnUnsetEnvIsRefusedWhereItIsRead(t *testing.T) {
	withRealSchema(t)
	path := writeDriftfile(t, "slice \"demo\" {\n  labels = { a = env(\"DRIFT_TEST_SURELY_UNSET\") }\n}\n")
	_, err := CompileDriftfile(path)
	if err == nil || !strings.Contains(err.Error(), "Driftfile:2:") || !strings.Contains(err.Error(), "--var") {
		t.Fatalf("got %v, want a refusal at line 2 naming --var", err)
	}
}

// ─── the platform's grid ────────────────────────────────────────────────────

func TestCompile_HoldsBookingsToThePlatformsGrid(t *testing.T) {
	withRealSchema(t)
	pulse := func(mem string) string {
		return "atomic {\n  element \"api\" {\n    pulse \"POST /x\" {\n      handler = \"PostX\"\n      memory = \"" + mem + "\"\n    }\n  }\n}\n"
	}
	field := func(mem string) string {
		return "atomic {\n  element \"api\" {\n    field \"worker\" {\n      handler = \"Worker\"\n      memory = \"" + mem + "\"\n    }\n  }\n}\n"
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"above 256MiB", pulse("300MiB"), "Memory out of range"},
		{"below 8MiB", pulse("4MiB"), "Memory out of range"},
		{"MB is not a unit here", pulse("64MB"), "Not a size"},
		{"not whole MiB", pulse("1536KiB"), "Not whole MiB"},
		{"a Field below the compiled floor", field("8MiB"), "Below the compiled floor"},
		{"no memory at all", "atomic {\n  element \"api\" {\n    pulse \"POST /x\" { handler = \"PostX\" }\n  }\n}\n", "Missing memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeDriftfile(t, "slice \"demo\" {}\n"+tc.body, "atomic/api/")
			_, err := CompileDriftfile(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}

	// The controls, so the refusals above cannot be passing for one reason.
	for _, ok := range []string{pulse("8MiB"), pulse("256MiB"), field("16MiB")} {
		path := writeDriftfile(t, "slice \"demo\" {}\n"+ok, "atomic/api/")
		if _, err := CompileDriftfile(path); err != nil {
			t.Errorf("a booking on the grid was refused: %v", err)
		}
	}
}

func TestCompile_RefusesMoreSlotsThanASliceHolds(t *testing.T) {
	withRealSchema(t)
	var b strings.Builder
	b.WriteString("slice \"demo\" {}\natomic {\n  element \"api\" {\n")
	for i := 0; i < 65; i++ {
		b.WriteString("    pulse \"GET /r" + strconv.Itoa(i) + "\" {\n      handler = \"H\"\n      memory = \"8MiB\"\n    }\n")
	}
	b.WriteString("  }\n}\n")
	_, err := CompileDriftfile(writeDriftfile(t, b.String(), "atomic/api/"))
	if err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("65 slots: got %v, want a refusal naming 64", err)
	}
}

// A refusal the platform's schema makes is moved back to where the file wrote
// it: an uppercase route fails the schema's route pattern, and is reported at
// the pulse's label.
func TestCompile_ASchemaRefusalCarriesTheFilesPosition(t *testing.T) {
	withRealSchema(t)
	body := "slice \"demo\" {}\n" +
		"atomic {\n  element \"api\" {\n" +
		"    pulse \"POST /Rpc\" {\n" + // line 4
		"      handler = \"PostRPC\"\n      memory = \"32MiB\"\n    }\n  }\n}\n"
	_, err := CompileDriftfile(writeDriftfile(t, body, "atomic/api/"))
	if err == nil {
		t.Fatal("an uppercase route compiled; the platform's route pattern refuses it")
	}
	if !strings.Contains(err.Error(), "Driftfile:4:") || !strings.Contains(err.Error(), "route") {
		t.Errorf("the refusal is not at the pulse's line, or does not name the route: %v", err)
	}
}

// ─── what the file leaves out ───────────────────────────────────────────────

// The smallest shape that runs what the file names: every limit 0 (priced at
// nothing, enforced as the free figure), the slot and database counts derived,
// scheduled jobs counted from the crons.
func TestCompile_WhatTheFileLeavesOutIsTheSmallestShape(t *testing.T) {
	withRealSchema(t)
	body := `slice "demo" {}
atomic {
  element "api" {
    pulse "GET /a" {
      handler = "A"
      memory  = "16MiB"
      cron    = "0 * * * *"
    }
    pulse "GET /b" {
      handler = "B"
      memory  = "16MiB"
      cron    = "30 * * * *"
    }
  }
}
backbone {
  sql "one" { size = "10MiB" }
  sql "two" { size = "10MiB" }
  queue "jobs" {}
}
`
	c, err := CompileDriftfile(writeDriftfile(t, body, "atomic/api/"))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Shape
	if s.Atomic.MaxNumberOfFunctions != 2 || s.Backbone.SQL.MaxDatabases != 2 || s.Atomic.MaxNumberOfScheduledJobs != 2 {
		t.Errorf("derived counts = slots %d, databases %d, scheduled jobs %d; want 2, 2, 2",
			s.Atomic.MaxNumberOfFunctions, s.Backbone.SQL.MaxDatabases, s.Atomic.MaxNumberOfScheduledJobs)
	}
	for _, l := range shapeLimits {
		if l.name == "scheduled_jobs" {
			continue
		}
		if v := *l.at(&s); v != 0 {
			t.Errorf("limits.%s defaults to %d, want 0", l.name, v)
		}
	}
	if d, ok := s.Backbone.Queues.Queues["jobs"]; !ok || d != 0 {
		t.Errorf("queue jobs = %d (declared %v), want declared at 0, the slice's queue depth", d, ok)
	}

	// Fewer scheduled jobs than crons is refused.
	short := strings.Replace(body, `slice "demo" {}`,
		"slice \"demo\" {\n  limits { scheduled_jobs = 1 }\n}", 1)
	if _, err := CompileDriftfile(writeDriftfile(t, short, "atomic/api/")); err == nil || !strings.Contains(err.Error(), "Too few scheduled jobs") {
		t.Errorf("one scheduled job for two crons: got %v", err)
	}
}

// A shape inside the free grant has FitsFree and a FreeShape carrying the free
// preset's figures for what the file leaves out; a shape outside it has
// FitsFree false and a FreeMisfit naming the bound it crosses.
func TestCompile_FreeFitIsHeldToTheFreeEnvelope(t *testing.T) {
	withRealSchema(t)
	fits := "slice \"demo\" {}\n" +
		"atomic {\n  element \"api\" {\n    pulse \"GET /a\" {\n      handler = \"A\"\n      memory = \"16MiB\"\n    }\n  }\n}\n"
	c, err := CompileDriftfile(writeDriftfile(t, fits, "atomic/api/"))
	if err != nil {
		t.Fatalf("a shape inside the free slice was refused: %v", err)
	}
	if !c.FitsFree || c.FreeMisfit != nil {
		t.Errorf("a shape inside the free slice: FitsFree %v, FreeMisfit %v", c.FitsFree, c.FreeMisfit)
	}
	if c.FreeShape.Atomic.MaxNumberOfRequestsPerMinute != 60 || c.FreeShape.Canvas.TotalMaxSizeInBytes != 50*bytesPerMiB {
		t.Errorf("an omitted limit on a free slice is not the preset's: rpm %d, canvas %d",
			c.FreeShape.Atomic.MaxNumberOfRequestsPerMinute, c.FreeShape.Canvas.TotalMaxSizeInBytes)
	}

	for _, tc := range []struct{ name, extra, want string }{
		{"a limit above the grant", "", "limits.requests_per_minute is 61"},
		{"too much memory", "", "book 96 MiB in total"},
		{"a second database", "backbone {\n  sql \"a\" { size = \"5MiB\" }\n  sql \"b\" { size = \"5MiB\" }\n}\n", "2 sql databases"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fits + tc.extra
			switch tc.name {
			case "a limit above the grant":
				body = strings.Replace(body, `slice "demo" {}`,
					"slice \"demo\" {\n  limits { requests_per_minute = 61 }\n}", 1)
			case "too much memory":
				body = strings.Replace(body, `memory = "16MiB"`, `memory = "96MiB"`, 1)
			}
			c, err := CompileDriftfile(writeDriftfile(t, body, "atomic/api/"))
			if err != nil {
				t.Fatalf("a shape outside the free slice is a priced slice, not a refusal: %v", err)
			}
			if c.FitsFree || c.FreeMisfit == nil || !strings.Contains(c.FreeMisfit.Error(), tc.want) {
				t.Fatalf("FitsFree %v, FreeMisfit %v, want a misfit naming %q", c.FitsFree, c.FreeMisfit, tc.want)
			}
		})
	}
}

// The billing key is gone from the slice block and from an environment block:
// a file that still sets it is refused, with the sentence saying to delete it.
func TestCompile_TheBillingKeyIsRefused(t *testing.T) {
	withRealSchema(t)
	for name, body := range map[string]string{
		"slice":       "slice \"demo\" {\n  billing = \"monthly\"\n}\n",
		"environment": "slice \"demo\" {}\nenvironment \"staging\" {\n  slice = \"demo-staging\"\n  billing = \"monthly\"\n}\n",
	} {
		_, err := CompileDriftfile(writeDriftfile(t, body))
		if err == nil || !strings.Contains(err.Error(), "billing is gone") || !strings.Contains(err.Error(), "no longer says how a slice is paid for") {
			t.Errorf("%s: got %v, want the billing-is-gone refusal", name, err)
		}
	}
}

// edge.rate_limit and limits.requests_per_minute are one field of the shape,
// so the file may write it once.
func TestCompile_TheRateLimitHasOneHome(t *testing.T) {
	withRealSchema(t)
	body := "slice \"demo\" {\n  limits { requests_per_minute = 600 }\n}\nedge { rate_limit = \"10/s\" }\n"
	if _, err := CompileDriftfile(writeDriftfile(t, body)); err == nil || !strings.Contains(err.Error(), "one place") {
		t.Fatalf("both spellings compiled: %v", err)
	}
	c, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\nedge { rate_limit = \"10/s\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Shape.Atomic.MaxNumberOfRequestsPerMinute != 600 {
		t.Errorf("edge.rate_limit 10/s compiled to %d requests a minute, want 600", c.Shape.Atomic.MaxNumberOfRequestsPerMinute)
	}
}

// Every environment block is validated, whichever one the file is compiled for.
func TestCompile_EnvironmentsAreValidated(t *testing.T) {
	withRealSchema(t)
	body := "variable \"m\" { default = \"32MiB\" }\nslice \"demo\" {}\n" +
		"environment \"staging\" {\n  slice = \"demo-staging\"\n  vars = { m = \"16MiB\" }\n}\n"
	c, err := CompileDriftfile(writeDriftfile(t, body))
	if err != nil || len(c.Environments) != 1 {
		t.Fatalf("a valid environment: %v, %v", c, err)
	}
	bad := strings.Replace(body, "vars = { m = ", "vars = { nope = ", 1)
	if _, err := CompileDriftfile(writeDriftfile(t, bad)); err == nil || !strings.Contains(err.Error(), "Undeclared variable") {
		t.Errorf("an environment setting an undeclared variable: %v", err)
	}
}
