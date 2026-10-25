package file

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
)

// realHome is HOME before any test replaces it: it holds the schema the
// platform last served. CI fetches the live schema there before the suite runs.
var realHome = os.Getenv("HOME")

// realSchema is the platform's schema as this machine holds it.
func realSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(realHome, ".drift", "driftfile.schema.json"))
	if err != nil {
		t.Fatalf("no platform schema at ~/.drift/driftfile.schema.json (run any online drift command once): %v", err)
	}
	return raw
}

// withServedSchema gives the test a scratch HOME holding the platform's schema,
// so these tests never read a schema another test left behind.
func withServedSchema(t *testing.T) {
	t.Helper()
	withSchemaBytes(t, realSchema(t))
}

func withSchemaBytes(t *testing.T, raw []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The scaffold tests below round-trip through the real compiler, which holds
// the document to the schema the platform serves. With none cached, that check
// validates nothing and the round trip proves nothing.
func TestSchemaMustBePresentOrTheScaffoldTestsProveNothing(t *testing.T) {
	if !project.SchemaAvailable() {
		t.Fatal("no Driftfile schema on this machine, so the scaffold round trips below " +
			"would pass without validating anything.\n" +
			"Fetch it once and it stays: `drift slice list` while online, or\n" +
			"  mkdir -p ~/.drift && curl -fsS https://api.ondrift.eu/driftfile/schema -o ~/.drift/driftfile.schema.json")
	}
}

// ─── new ────────────────────────────────────────────────────────────────────

// starterProject writes the starter beside the folders it names, as a project
// has them once `drift atomic new` has written the pulse's source.
func starterProject(t *testing.T, canvasDir string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"atomic", "site"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(starterDriftfile("demo", canvasDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The scaffold's whole claim is that it produces a file that DEPLOYS, not one
// that merely parses. So the test is a round trip through the real compiler
// rather than a string comparison: the free billing it writes is held to the
// free slice, and its one pulse is booked.
func TestNew_ScaffoldCompiles(t *testing.T) {
	withServedSchema(t)
	for _, canvas := range []string{"", "site"} {
		c, err := project.CompileDriftfile(starterProject(t, canvas))
		if err != nil {
			t.Fatalf("the scaffold (canvas %q) does not survive the compiler it is meant to precede:\n%v", canvas, err)
		}
		if c.Manifest.Name() != "demo" || c.Billing != "free" {
			t.Errorf("slice %q on %q billing, want demo on free", c.Manifest.Name(), c.Billing)
		}
		specs := project.FunctionSpecs(c.Manifest)
		if len(specs) != 1 || specs[0].Name != starterMethod+":"+starterRoute || specs[0].Handler == "" {
			t.Errorf("the scaffold declares %+v, want one booked pulse with a handler", specs)
		}
		if f := c.Shape.Atomic.Functions; len(f) != 1 || f[0].MemoryBytes != 32<<20 {
			t.Errorf("the scaffold books %+v, want its pulse at %s", f, starterMemory)
		}
		if sites := c.Manifest.Slice().Entries("dir", "canvas", "sites"); (canvas != "") != (len(sites) == 1) {
			t.Errorf("canvas %q produced sites %+v", canvas, sites)
		}
	}
}

func TestNew_RejectsNamesThePlatformWould(t *testing.T) {
	for _, bad := range []string{"", "-lead", "trail-", "UPPER", "has space", strings.Repeat("x", 33)} {
		if nameLooksValid(bad) {
			t.Errorf("%q accepted, but the platform's identifier rule rejects it", bad)
		}
	}
	for _, ok := range []string{"demo", "my-app", "a1", "myapp-staging"} {
		if !nameLooksValid(ok) {
			t.Errorf("%q rejected, but it is a legal name", ok)
		}
	}
}

// ─── fmt ────────────────────────────────────────────────────────────────────

// The one that matters most. A formatter that eats comments is worse than no
// formatter: the loss is silent, and it is noticed later, in a diff, by someone
// else.
func TestFmt_PreservesCommentsAndOrder(t *testing.T) {
	src := []byte(`# top of file
slice "demo" {
  # how it is paid for
  billing = "monthly" # inline note
}
queue "b" {}
queue "a" {}
`)
	out, err := formatDriftfile("Driftfile", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# top of file", "# how it is paid for", "# inline note"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("comment %q was dropped:\n%s", want, out)
		}
	}
	if strings.Index(string(out), `queue "b"`) > strings.Index(string(out), `queue "a"`) {
		t.Errorf("fmt reordered the blocks; the order is the person's:\n%s", out)
	}
}

// The canonical layout: two-space indentation and the `=` of neighbouring
// attributes aligned.
func TestFmt_LaysOutCanonically(t *testing.T) {
	src := []byte("element \"api\" {\n    pulse \"GET /a\" {\n  handler = \"A\"\n      memory = \"16MiB\"\n    }\n}\n")
	out, err := formatDriftfile("Driftfile", src)
	if err != nil {
		t.Fatal(err)
	}
	want := "element \"api\" {\n  pulse \"GET /a\" {\n    handler = \"A\"\n    memory  = \"16MiB\"\n  }\n}\n"
	if string(out) != want {
		t.Errorf("fmt:\n got:\n%s\nwant:\n%s", out, want)
	}
}

// Formatting twice must equal formatting once, or `--write` in a pre-commit hook
// produces a diff on every run.
func TestFmt_IsIdempotent(t *testing.T) {
	src := []byte("slice \"demo\" {\nbilling=\"monthly\"\n  labels = { team = \"a\", tier=\"b\" }\n}\n")
	once, err := formatDriftfile("Driftfile", src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := formatDriftfile("Driftfile", once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("fmt is not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

// A file that does not parse, or a YAML one, is refused rather than laid out:
// formatting broken tokens would move the error the person is about to read.
func TestFmt_RefusesWhatItCannotRead(t *testing.T) {
	if _, err := formatDriftfile("Driftfile", []byte("slice: demo\n")); err == nil || !strings.Contains(err.Error(), "the Driftfile is HCL now") {
		t.Errorf("a YAML Driftfile: got %v", err)
	}
	if _, err := formatDriftfile("Driftfile", []byte("slice \"demo\" {\n  billing = \n")); err == nil {
		t.Error("a broken file was formatted")
	}
}

// ─── lint ───────────────────────────────────────────────────────────────────

// `lint` claims it validates exactly as `drift plan` would, and a plan also
// binds every declared handler to a callable. A Driftfile that lints green and
// then fails at apply is the one thing a CI gate exists to prevent.
func TestLint_RefusesAHandlerThatIsNotThere(t *testing.T) {
	withServedSchema(t)
	dir := lintFixture(t, "GetPong") // the source declares GetPing

	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil {
		t.Fatal("a handler with no callable behind it must fail lint")
	}
	// The message has to name the pulse AND list what is actually there —
	// "not found" is not actionable when the handler looks right.
	for _, want := range []string{"get:ping", "GetPong", "GetPing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("lint error should mention %q, got:\n%v", want, err)
		}
	}
}

func TestLint_PassesWhenTheHandlerResolves(t *testing.T) {
	withServedSchema(t)
	dir := lintFixture(t, "GetPing")

	var out bytes.Buffer
	restore := redirectStdout(t, &out)
	err := getLintCmd().RunE(nil, []string{dir})
	restore()
	if err != nil {
		t.Fatalf("a Driftfile whose handler resolves must lint clean, got:\n%v", err)
	}
	if !strings.Contains(out.String(), `is valid (slice "lintdemo")`) {
		t.Errorf("lint does not say what it validated:\n%s", out.String())
	}
}

// lint needs no secret's value: a CI gate runs without them, and the file
// holds only references.
func TestLint_NeedsNoSecretsValue(t *testing.T) {
	withServedSchema(t)
	dir := lintFixture(t, "GetPing")
	src := lintDriftfile("GetPing") + "backbone {\n  secret \"STRIPE_KEY\" { value = env(\"DRIFT_TEST_SURELY_UNSET\") }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := getLintCmd().RunE(nil, []string{dir}); err != nil {
		t.Fatalf("lint asked for a secret's value: %v", err)
	}
}

// A YAML Driftfile is refused as every reader refuses one.
func TestLint_RefusesAYAMLDriftfile(t *testing.T) {
	withServedSchema(t)
	dir := lintFixture(t, "GetPing")
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte("slice: lintdemo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil || !strings.Contains(err.Error(), "the Driftfile is HCL now") {
		t.Fatalf("a YAML Driftfile: got %v", err)
	}
}

// An element whose folder is not on this machine is what `drift plan` would
// refuse, so lint refuses it too, at the element.
func TestLint_RefusesAnElementWithNoFolder(t *testing.T) {
	withServedSchema(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(lintDriftfile("GetPing")), 0o600); err != nil {
		t.Fatal(err)
	}
	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil || !strings.Contains(err.Error(), "atomic is not a directory") {
		t.Fatalf("an element with no folder: got %v", err)
	}
}

// ─── lint: the format this binary implements vs the one it holds ────────────

// The refusal rung, reached through the command rather than the grading
// function, because the wiring is the half that can be absent: a gate nobody
// calls grades nothing.
//
// The exit is what a CI job reads, so it is asserted as an error and not as a
// message on a run that still succeeded.
func TestLint_RefusesAFormatThisBinaryCannotRead(t *testing.T) {
	dir := lintFixture(t, "GetPing")
	withCachedFormat(t, "99.0.0")

	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil {
		t.Fatal("a served major ahead of this binary must fail lint — every field error " +
			"it could print instead would describe the symptom")
	}
	for _, want := range []string{"99.0.0", common.ImplementedDriftfileFormat, "drift upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, got:\n%v", want, err)
		}
	}
}

// The warning rung: this binary is behind but can still read, so the file is
// linted and the skew is said out loud. Exit zero, because the Driftfile is
// valid and a CI gate must not fail on the platform having moved.
func TestLint_WarnsButPassesWhenTheServedMinorIsAhead(t *testing.T) {
	dir := lintFixture(t, "GetPing")
	withCachedFormat(t, bumpMinor(common.ImplementedDriftfileFormat))

	var notices bytes.Buffer
	restore := redirectStderr(t, &notices)
	err := getLintCmd().RunE(nil, []string{dir})
	restore()

	if err != nil {
		t.Fatalf("a minor skew must not fail a valid Driftfile: %v", err)
	}
	if !strings.Contains(notices.String(), "drift upgrade") {
		t.Errorf("the skew must be announced on stderr, got:\n%s", notices.String())
	}
}

// withCachedFormat points this machine's schema cache at a temporary HOME whose
// schema declares `version` and accepts any document: these tests are about the
// version comparison, not about validation.
func withCachedFormat(t *testing.T, version string) {
	t.Helper()
	withSchemaBytes(t, []byte(`{"$schema":"http://json-schema.org/draft-07/schema#","version":"`+version+
		`","type":"object","additionalProperties":true}`))
}

// redirectStderr captures what the command writes to os.Stderr. The notice goes
// there rather than to the command's own writer, so reading it back means
// replacing the file descriptor's destination for the duration of the call.
func redirectStderr(t *testing.T, into *bytes.Buffer) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w

	done := make(chan struct{})
	go func() {
		_, _ = into.ReadFrom(r)
		close(done)
	}()

	return func() {
		os.Stderr = prev
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}

// bumpMinor derives a version one minor ahead, so these tests move with the
// implemented constant instead of pinning a number that goes stale the first
// time the format changes.
func bumpMinor(v string) string {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return v
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return v
	}
	return parts[0] + "." + strconv.Itoa(minor+1) + ".0"
}

// 16MiB, not 8: the fixture's source is Go, and a compiled pulse books at least
// the compiled floor the schema publishes. At 8MiB these tests would be
// asserting a lint result on a Driftfile the platform refuses.
func lintDriftfile(handler string) string {
	return "slice \"lintdemo\" { billing = \"monthly\" }\n" +
		"atomic {\n  element \"default\" {\n    pulse \"GET /ping\" {\n      handler = \"" + handler + "\"\n      memory = \"16MiB\"\n    }\n  }\n}\n"
}

// lintFixture writes a one-pulse project whose source declares GetPing, and
// whose Driftfile names whichever handler the caller asks for.
func lintFixture(t *testing.T, handler string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "atomic"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"),
		[]byte(lintDriftfile(handler)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "atomic", "ping.go"),
		[]byte("package main\n\nfunc GetPing(req any) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
