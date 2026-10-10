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

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// realHome is HOME before any test replaces it, read once at package start:
// it holds the schema the platform last served, which every Driftfile is
// validated against. CI fetches the live schema there before the suite runs.
var realHome = os.Getenv("HOME")

// withRealSchema gives the test a scratch HOME holding the platform's schema,
// so a Driftfile 2 is validated exactly as it is on a laptop that has met the
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

// write2 lays out a Driftfile 2 project in a temp dir: the file, and every
// directory or file it names (a path ending in / is a directory).
func write2(t *testing.T, body string, paths ...string) string {
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

const head2 = "drift {\n  format = \"2.0\"\n}\n"

// ─── detection ──────────────────────────────────────────────────────────────

// A file is format 2 by its first tokens, past comments. Every YAML Driftfile
// begins with a `key:` pair and is not, and that control is what stops the
// detector from reading everything as HCL.
func TestIsFormat2_ReadsTheFirstTokens(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{head2, true},
		{"# a comment\n\n// another\ndrift {\n}\n", true},
		{"drift {\n  format = \"2.0\"", true}, // broken HCL is still format 2, and is reported as HCL
		{"slice: demo\n", false},
		{"drift: {format: 2}\n", false},
		{"name: demo\natomic:\n  pulses: []\n", false},
		{"slice \"demo\" {}\ndrift {}\n", false},
	} {
		if got := IsFormat2([]byte(tc.text)); got != tc.want {
			t.Errorf("IsFormat2(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// The YAML commands refuse a format-2 file by saying what it is, rather than
// reporting a YAML error about a file that was never YAML.
func TestParseDriftfile_NamesAFormat2File(t *testing.T) {
	path := write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\n")
	_, err := ParseDriftfile(path)
	if err == nil || !strings.Contains(err.Error(), "Driftfile 2") || !strings.Contains(err.Error(), "drift plan") {
		t.Fatalf("ParseDriftfile on a format-2 file returned %v, want a refusal naming `drift plan`", err)
	}
}

// ─── the fixture, compiled ──────────────────────────────────────────────────

// compiledGolden is what the fxevm fixture compiles to, in a form that does
// not depend on the machine: directories relative to the Driftfile.
type compiledGolden struct {
	Billing   string         `json:"billing"`
	Document  Node           `json:"document"`
	Slots     []goldenSpec   `json:"slots"`
	Shape     SliceConfig    `json:"shape"`
	WriteOnly []WriteOnlyRef `json:"write_only"`
}

type goldenSpec struct {
	Name, Handler, Element, Dir, Auth, Response string
	Concurrent                                  bool
}

// The fxevm testnode, compiled. The golden is the CONTRACT between the new
// format and everything downstream: the document every 1.x reader consumes and
// the shape the platform prices. Regenerate with DRIFT_UPDATE_GOLDEN=1 and
// read the diff.
func TestCompile2_FxevmGolden(t *testing.T) {
	withRealSchema(t)
	base, _ := filepath.Abs(filepath.Join("testdata", "driftfile2", "fxevm"))
	c, err := CompileDriftfile2(filepath.Join(base, "Driftfile"))
	if err != nil {
		t.Fatalf("the fxevm fixture does not compile:\n%v", err)
	}

	g := compiledGolden{Billing: c.Billing, Document: c.Manifest.Doc(), Shape: c.Shape, WriteOnly: c.WriteOnly}
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

// The compiled document is the 1.x document: the same readers get the same
// answers from it. Asserted on the readers rather than on the golden, so a
// golden regenerated carelessly cannot hide a document the deploy reads wrong.
func TestCompile2_TheDocumentReadsAsAYAMLDriftfileDoes(t *testing.T) {
	withRealSchema(t)
	c, err := CompileDriftfile2(filepath.Join("testdata", "driftfile2", "fxevm", "Driftfile"))
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

// The two formats deploy the same thing. Driftfile.yaml is the testnode's YAML
// 1.x file, and every slot it declares must come out of the Driftfile 2 with
// the same spec and the same declaration digest: the digest is what decides
// whether a slot's deploy is skipped, so a difference here would redeploy, or
// fail to redeploy, a slot whose file did not change.
func TestCompile2_DeploysWhatTheSameYAMLDriftfileDeploys(t *testing.T) {
	withRealSchema(t)
	t.Setenv("EXPLORER_PASS", "x")
	base := filepath.Join("testdata", "driftfile2", "fxevm")
	yaml, err := ParseDriftfile(filepath.Join(base, "Driftfile.yaml"))
	if err != nil {
		t.Fatalf("the YAML twin does not parse: %v", err)
	}
	c, err := CompileDriftfile2(filepath.Join(base, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}

	bySlot := func(m *Manifest) map[string]string {
		atomic_cmd.SetDeclaredSchedules(declaredSchedules(m))
		out := map[string]string{}
		for _, s := range AtomicSpecs(m) {
			out[s.Name] = s.Dir + " " + atomic_cmd.DeclarationDigest(s)
		}
		return out
	}
	from1, from2 := bySlot(yaml), bySlot(c.Manifest)
	if len(from1) != 4 {
		t.Fatalf("the YAML twin declares %d slots, want 4", len(from1))
	}
	for name, want := range from1 {
		if got := from2[name]; got != want {
			t.Errorf("slot %s: the Driftfile 2 compiles to %q, the YAML file to %q", name, got, want)
		}
	}
	for name := range from2 {
		if _, ok := from1[name]; !ok {
			t.Errorf("the Driftfile 2 declares %s, which the YAML file does not", name)
		}
	}
	if a, b := yaml.Slice().Sub("edge"), c.Manifest.Slice().Sub("edge"); !jsonEqual(a, b) {
		t.Errorf("edge differs:\n yaml %v\n hcl  %v", a, b)
	}
	if a, b := yaml.Slice().Get("backbone", "sql"), c.Manifest.Slice().Get("backbone", "sql"); a == nil || !jsonEqual(a, b) {
		t.Errorf("sql differs:\n yaml %v\n hcl  %v", a, b)
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
func TestCompile2_RefusesAWriteOnlyValueThatIsNotAReference(t *testing.T) {
	withRealSchema(t)
	for _, tc := range []struct{ name, body string }{
		{"secret literal", `secret "STRIPE_KEY" { value = "sk_live_123" }`},
		{"secret from var", "variable \"k\" { default = \"x\" }\nsecret \"STRIPE_KEY\" { value = var.k }"},
		{"api key literal", "element \"api\" {\n  pulse \"POST /x\" {\n    handler = \"PostX\"\n    memory = \"32MiB\"\n    auth { api_key = \"abc\" }\n  }\n}"},
		{"site password literal", "edge {\n  site_auth {\n    user \"a\" { password = \"hunter2\" }\n  }\n}"},
		{"site password from file", "edge {\n  site_auth {\n    user \"a\" { password = file(\"pw\") }\n  }\n}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\n"+tc.body+"\n", "atomic/api/", "pw")
			_, err := CompileDriftfile2(path)
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
func TestCompile2_AWriteOnlyReferenceCompilesWithoutItsValue(t *testing.T) {
	withRealSchema(t)
	t.Setenv("STRIPE_KEY", "sk_live_do_not_print")
	path := write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\nsecret \"STRIPE_KEY\" { value = env(\"STRIPE_KEY\") }\n")
	c, err := CompileDriftfile2(path)
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
func TestCompile2_ExpressionsAreSmall(t *testing.T) {
	withRealSchema(t)
	t.Setenv("RPC_HANDLER", "PostRPC")
	body := head2 + `
variable "rpc_memory" { default = "48MiB" }
slice "demo" { billing = "monthly" }
element "api" {
  pulse "POST /rpc" {
    handler = env("RPC_HANDLER")
    memory  = var.rpc_memory
    env     = { GENESIS = sha256(file("genesis.sql")), NOTE = "literal" }
  }
}
`
	path := write2(t, body, "atomic/api/", "genesis.sql")
	c, err := CompileDriftfile2(path)
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
	// write2 gives every file the content "x\n".
	sum := sha256.Sum256([]byte("x\n"))
	if got, want := spec.Env["GENESIS"], hex.EncodeToString(sum[:]); got != want {
		t.Errorf("sha256(file()) = %q, want %q", got, want)
	}
	if spec.Env["NOTE"] != "literal" {
		t.Errorf("a literal env value = %q", spec.Env["NOTE"])
	}

	// Anything else is not a function this format has.
	for _, expr := range []string{`upper("x")`, `timestamp()`, `lookup({}, "a", "b")`} {
		path := write2(t, head2+"slice \"demo\" {\n  billing = "+expr+"\n}\n")
		_, err := CompileDriftfile2(path)
		if err == nil || !strings.Contains(err.Error(), "no function named") {
			t.Errorf("%s compiled, or failed for another reason: %v", expr, err)
		}
	}
}

// An env() that names an unset variable is refused at its position, and says
// where a value can come from.
func TestCompile2_AnUnsetEnvIsRefusedWhereItIsRead(t *testing.T) {
	withRealSchema(t)
	path := write2(t, head2+"slice \"demo\" {\n  billing = env(\"DRIFT_TEST_SURELY_UNSET\")\n}\n")
	_, err := CompileDriftfile2(path)
	if err == nil || !strings.Contains(err.Error(), "Driftfile:5:") || !strings.Contains(err.Error(), "--var") {
		t.Fatalf("got %v, want a refusal at line 5 naming --var", err)
	}
}

// ─── the platform's grid ────────────────────────────────────────────────────

func TestCompile2_HoldsBookingsToThePlatformsGrid(t *testing.T) {
	withRealSchema(t)
	pulse := func(mem string) string {
		return "element \"api\" {\n  pulse \"POST /x\" {\n    handler = \"PostX\"\n    memory = \"" + mem + "\"\n  }\n}\n"
	}
	field := func(mem string) string {
		return "element \"api\" {\n  field \"worker\" {\n    handler = \"Worker\"\n    memory = \"" + mem + "\"\n  }\n}\n"
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"above 256MiB", pulse("300MiB"), "Memory out of range"},
		{"below 8MiB", pulse("4MiB"), "Memory out of range"},
		{"MB is not a unit here", pulse("64MB"), "Not a size"},
		{"not whole MiB", pulse("1536KiB"), "Not whole MiB"},
		{"a Field below the compiled floor", field("8MiB"), "Below the compiled floor"},
		{"no memory at all", "element \"api\" {\n  pulse \"POST /x\" { handler = \"PostX\" }\n}\n", "Missing memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\n"+tc.body, "atomic/api/")
			_, err := CompileDriftfile2(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}

	// The controls, so the refusals above cannot be passing for one reason.
	for _, ok := range []string{pulse("8MiB"), pulse("256MiB"), field("16MiB")} {
		path := write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\n"+ok, "atomic/api/")
		if _, err := CompileDriftfile2(path); err != nil {
			t.Errorf("a booking on the grid was refused: %v", err)
		}
	}
}

func TestCompile2_RefusesMoreSlotsThanASliceHolds(t *testing.T) {
	withRealSchema(t)
	var b strings.Builder
	b.WriteString(head2 + "slice \"demo\" { billing = \"monthly\" }\nelement \"api\" {\n")
	for i := 0; i < 65; i++ {
		b.WriteString("  pulse \"GET /r" + strconv.Itoa(i) + "\" {\n    handler = \"H\"\n    memory = \"8MiB\"\n  }\n")
	}
	b.WriteString("}\n")
	_, err := CompileDriftfile2(write2(t, b.String(), "atomic/api/"))
	if err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("65 slots: got %v, want a refusal naming 64", err)
	}
}

// A refusal the platform's schema makes is moved back to where the file wrote
// it: an uppercase route fails the schema's route pattern, and is reported at
// the pulse's label.
func TestCompile2_ASchemaRefusalCarriesTheFilesPosition(t *testing.T) {
	withRealSchema(t)
	body := head2 + "slice \"demo\" { billing = \"monthly\" }\n" +
		"element \"api\" {\n" +
		"  pulse \"POST /Rpc\" {\n" + // line 6
		"    handler = \"PostRPC\"\n    memory = \"32MiB\"\n  }\n}\n"
	_, err := CompileDriftfile2(write2(t, body, "atomic/api/"))
	if err == nil {
		t.Fatal("an uppercase route compiled; the platform's route pattern refuses it")
	}
	if !strings.Contains(err.Error(), "Driftfile:6:") || !strings.Contains(err.Error(), "route") {
		t.Errorf("the refusal is not at the pulse's line, or does not name the route: %v", err)
	}
}

// ─── what the file leaves out ───────────────────────────────────────────────

// The smallest shape that runs what the file names: every limit 0 (priced at
// nothing, enforced as the free figure), the slot and database counts derived,
// scheduled jobs counted from the crons.
func TestCompile2_WhatTheFileLeavesOutIsTheSmallestShape(t *testing.T) {
	withRealSchema(t)
	body := head2 + `slice "demo" { billing = "monthly" }
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
sql "one" { size = "10MiB" }
sql "two" { size = "10MiB" }
queue "jobs" {}
`
	c, err := CompileDriftfile2(write2(t, body, "atomic/api/"))
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
	short := strings.Replace(body, `slice "demo" { billing = "monthly" }`,
		"slice \"demo\" {\n  billing = \"monthly\"\n  limits { scheduled_jobs = 1 }\n}", 1)
	if _, err := CompileDriftfile2(write2(t, short, "atomic/api/")); err == nil || !strings.Contains(err.Error(), "Too few scheduled jobs") {
		t.Errorf("one scheduled job for two crons: got %v", err)
	}
}

// billing = "free" is the free slice: what the file leaves out is the free
// preset's figure, and a shape outside the free envelope is refused offline,
// naming the bound it crosses.
func TestCompile2_FreeBillingIsHeldToTheFreeEnvelope(t *testing.T) {
	withRealSchema(t)
	fits := head2 + "slice \"demo\" { billing = \"free\" }\n" +
		"element \"api\" {\n  pulse \"GET /a\" {\n    handler = \"A\"\n    memory = \"16MiB\"\n  }\n}\n"
	c, err := CompileDriftfile2(write2(t, fits, "atomic/api/"))
	if err != nil {
		t.Fatalf("a shape inside the free slice was refused: %v", err)
	}
	if c.Shape.Atomic.MaxNumberOfRequestsPerMinute != 60 || c.Shape.Canvas.TotalMaxSizeInBytes != 50*bytesPerMiB {
		t.Errorf("an omitted limit on a free slice is not the preset's: rpm %d, canvas %d",
			c.Shape.Atomic.MaxNumberOfRequestsPerMinute, c.Shape.Canvas.TotalMaxSizeInBytes)
	}

	for _, tc := range []struct{ name, extra, want string }{
		{"a limit above the grant", "", "limits.requests_per_minute is 61"},
		{"too much memory", "", "book 96 MiB in total"},
		{"a second database", "sql \"a\" { size = \"5MiB\" }\nsql \"b\" { size = \"5MiB\" }\n", "2 sql databases"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fits + tc.extra
			switch tc.name {
			case "a limit above the grant":
				body = strings.Replace(body, `slice "demo" { billing = "free" }`,
					"slice \"demo\" {\n  billing = \"free\"\n  limits { requests_per_minute = 61 }\n}", 1)
			case "too much memory":
				body = strings.Replace(body, `memory = "16MiB"`, `memory = "96MiB"`, 1)
			}
			_, err := CompileDriftfile2(write2(t, body, "atomic/api/"))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Not the free slice") {
				t.Fatalf("got %v, want a free-envelope refusal naming %q", err, tc.want)
			}
		})
	}
}

// edge.rate_limit and limits.requests_per_minute are one field of the shape,
// so the file may write it once.
func TestCompile2_TheRateLimitHasOneHome(t *testing.T) {
	withRealSchema(t)
	body := head2 + "slice \"demo\" {\n  billing = \"monthly\"\n  limits { requests_per_minute = 600 }\n}\nedge { rate_limit = \"10/s\" }\n"
	if _, err := CompileDriftfile2(write2(t, body)); err == nil || !strings.Contains(err.Error(), "one place") {
		t.Fatalf("both spellings compiled: %v", err)
	}
	c, err := CompileDriftfile2(write2(t, head2+"slice \"demo\" { billing = \"monthly\" }\nedge { rate_limit = \"10/s\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Shape.Atomic.MaxNumberOfRequestsPerMinute != 600 {
		t.Errorf("edge.rate_limit 10/s compiled to %d requests a minute, want 600", c.Shape.Atomic.MaxNumberOfRequestsPerMinute)
	}
}

// Environments are parsed and validated now; what they do comes later.
func TestCompile2_EnvironmentsAreValidated(t *testing.T) {
	withRealSchema(t)
	body := head2 + "variable \"m\" { default = \"32MiB\" }\nslice \"demo\" { billing = \"monthly\" }\n" +
		"environment \"staging\" {\n  slice = \"demo-staging\"\n  billing = \"free\"\n  vars = { m = \"16MiB\" }\n}\n"
	c, err := CompileDriftfile2(write2(t, body))
	if err != nil || len(c.Environments) != 1 {
		t.Fatalf("a valid environment: %v, %v", c, err)
	}
	bad := strings.Replace(body, "vars = { m = ", "vars = { nope = ", 1)
	if _, err := CompileDriftfile2(write2(t, bad)); err == nil || !strings.Contains(err.Error(), "Undeclared variable") {
		t.Errorf("an environment setting an undeclared variable: %v", err)
	}
}
