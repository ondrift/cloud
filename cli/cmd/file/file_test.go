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

// The scaffold tests below round-trip through the real parser, and the parser's
// only authority is the schema the platform serves. With none cached, that parse
// checks nothing and the round trip proves nothing — this package went green on a
// fresh CI runner for exactly that reason while `cmd/project` failed loudly.
//
// Same guard as `cmd/project`, for the same reason: make the absence loud rather
// than let it read as a pass (#CLI-STANDARDUSAGE-ERF1CV).
func TestSchemaMustBePresentOrTheScaffoldTestsProveNothing(t *testing.T) {
	if !project.SchemaAvailable() {
		t.Fatal("no Driftfile schema on this machine, so the scaffold round trips below " +
			"would pass without validating anything.\n" +
			"Fetch it once and it stays: `drift slice list` while online, or\n" +
			"  mkdir -p ~/.drift && curl -fsS https://api.ondrift.eu/driftfile/schema -o ~/.drift/driftfile.schema.json")
	}
}

// ─── new ────────────────────────────────────────────────────────────────────

// The scaffold's whole claim is that it produces a file that DEPLOYS, not one
// that merely parses. So the test is a round trip through the real parser rather
// than a string comparison: if `new` ever emits the technically-minimal file
// again, the user's first command after `drift file new` is a validation error,
// which is the failure this command exists to prevent.
func TestNew_ScaffoldPassesTheRealParser(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(starterDriftfile("demo", "./site")), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := project.ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the scaffold does not survive the parser it is meant to precede:\n%v", err)
	}
	if m.Name() != "demo" {
		t.Errorf("name = %q, want demo", m.Name())
	}
}

// A function's `memory` is mandatory: it is that function's own admission pool
// and what it is billed at, so there is no default to fall back to and a
// Driftfile declaring a function without one is refused. The scaffold has to
// carry it, so its presence is asserted rather than left to whoever next edits
// the template.
// Asserted through the PARSER, on the BASE slice, not by searching the text.
// A string match for "memory: 32MB" passes on the staging override alone —
// the scaffold writes it twice — so it would still be green with the base knob
// deleted, which is the exact regression this is here to catch. Proven by
// deleting that line and watching this fail.
func TestNew_ScaffoldFillsTheRequiredInPracticeKnobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(starterDriftfile("demo", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := project.ParseDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	fns := m.Slice().Nodes("atomic", "functions")
	if len(fns) == 0 {
		t.Fatal("the scaffold declares no functions — `functions: []` is valid and " +
			"teaches nothing; a first edit should be changing a real entry")
	}
	for i, fn := range fns {
		if fn.Str("memory") == "" {
			t.Errorf("scaffolded function %d (%q) books no memory — there is no default, "+
				"so this is rejected on the user's first deploy", i, fn.Str("name"))
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
// else. This is why fmt walks the yaml.Node tree instead of re-marshalling a
// struct — a struct round trip drops every comment in the file.
func TestFmt_PreservesComments(t *testing.T) {
	src := []byte(`# top of file
name: demo
# about atomic
atomic:
  function_timeout: 30s # inline note
`)
	out, err := formatDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# top of file", "# about atomic", "# inline note"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("comment %q was dropped:\n%s", want, out)
		}
	}
}

func TestFmt_OrdersKeysCanonically(t *testing.T) {
	src := []byte(`environments:
  prod: {}
canvas: ./site
atomic:
  functions: []
  function_timeout: 30s
name: demo
`)
	out, err := formatDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// Top level: name first, then the sections, then the deployment siblings.
	assertOrder(t, got, "name:", "atomic:", "canvas:", "environments:")
	// And one level down, so a section's diff is stable too.
	assertOrder(t, got, "function_timeout:", "functions:")
}

// A key this CLI has never heard of must survive formatting. Dropping it would
// silently delete part of the user's file, and rejecting it would make `fmt`
// useless against any Driftfile written for a newer platform than this binary.
func TestFmt_KeepsUnknownKeys(t *testing.T) {
	src := []byte("name: demo\nsome_future_field: 42\n")
	out, err := formatDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "some_future_field: 42") {
		t.Errorf("an unrecognised key was dropped by formatting:\n%s", out)
	}
}

// Formatting twice must equal formatting once, or `--write` in a pre-commit hook
// produces a diff on every run.
func TestFmt_IsIdempotent(t *testing.T) {
	src := []byte("environments:\n  prod: {}\nname: demo\natomic:\n  functions: []\n  function_timeout: 30s\n")
	once, err := formatDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := formatDriftfile(once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("fmt is not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

// The short forms are the spec's, and a user chose them. Expanding them during a
// formatting pass turns "tidy my file" into "restructure my file" and produces
// exactly the large diff fmt exists to prevent.
func TestFmt_DoesNotExpandShorthands(t *testing.T) {
	src := []byte("name: demo\ncanvas: ./site\n")
	out, err := formatDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "canvas: ./site") {
		t.Errorf("the canvas short form was rewritten:\n%s", out)
	}
}

func assertOrder(t *testing.T, doc string, needles ...string) {
	t.Helper()
	prev := -1
	for _, n := range needles {
		at := strings.Index(doc, n)
		if at < 0 {
			t.Fatalf("%q missing from:\n%s", n, doc)
		}
		if at < prev {
			t.Errorf("%q appears before the key that should precede it:\n%s", n, doc)
		}
		prev = at
	}
}

// ─── lint ───────────────────────────────────────────────────────────────────

// `lint` claims it validates "exactly as a deploy would", and a deploy also
// binds every declared handler to a callable. A Driftfile that lints green and
// then fails at deploy is the one thing a CI gate exists to prevent, so the
// handler check belongs here and not only in the deploy path.
func TestLint_RefusesAHandlerThatIsNotThere(t *testing.T) {
	dir := lintFixture(t, "GetPong") // the source declares GetPing

	err := getLintCmd().RunE(nil, []string{dir})
	if err == nil {
		t.Fatal("a handler with no callable behind it must fail lint")
	}
	// The message has to name the function AND list what is actually there —
	// "not found" is not actionable when the handler looks right.
	for _, want := range []string{"get:ping", "GetPong", "GetPing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("lint error should mention %q, got:\n%v", want, err)
		}
	}
}

func TestLint_PassesWhenTheHandlerResolves(t *testing.T) {
	dir := lintFixture(t, "GetPing")

	if err := getLintCmd().RunE(nil, []string{dir}); err != nil {
		t.Fatalf("a Driftfile whose handler resolves must lint clean, got:\n%v", err)
	}
}

// A Driftfile is legitimately linted on its own — handed over for review, or
// sitting in a directory whose source has not been checked out. With no source
// tree to check against, the handler check is skipped rather than reported:
// calling that file invalid would refuse one that is fine.
func TestLint_SkipsTheHandlerCheckWithNoSourceTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"),
		[]byte(lintDriftfile("GetAnything")), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := getLintCmd().RunE(nil, []string{dir}); err != nil {
		t.Fatalf("a Driftfile with no source beside it must still lint, got:\n%v", err)
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

// The one-direction rule, at the command. A machine holding an OLDER format
// than this binary implements is not a problem — this binary understands
// everything that format can express — and saying anything would train people
// to ignore the notice that matters.
func TestLint_SaysNothingWhenThisBinaryIsAheadOfTheHeldFormat(t *testing.T) {
	dir := lintFixture(t, "GetPing")
	withCachedFormat(t, "1.0.0")

	var notices bytes.Buffer
	restore := redirectStderr(t, &notices)
	err := getLintCmd().RunE(nil, []string{dir})
	restore()

	if err != nil {
		t.Fatalf("an older held format must lint clean: %v", err)
	}
	if strings.Contains(notices.String(), "drift upgrade") {
		t.Errorf("nothing should be said when this binary is ahead, got:\n%s", notices.String())
	}
}

// withCachedFormat points this machine's schema cache at a temporary HOME whose
// schema declares `version`.
//
// The document is permissive on purpose. Which schema the PARSE uses depends on
// whether this package's compiled-schema memo is already warm, and these tests
// are about the version comparison rather than about validation — so the
// fixture Driftfile is one the real schema accepts too, and this copy accepts
// anything, leaving both paths agreeing.
func withCachedFormat(t *testing.T, version string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"$schema":"http://json-schema.org/draft-07/schema#","version":"` + version +
		`","type":"object","additionalProperties":true}`
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"),
		[]byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
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

// 16MB, not 8: the fixture's source is Go, and a compiled function books at
// least the compiled floor the schema publishes. At 8MB these tests would be
// asserting a lint result on a Driftfile the platform refuses, which is the
// opposite of what they exist to pin.
func lintDriftfile(handler string) string {
	return "name: lintdemo\natomic:\n  functions:\n    - name: get:ping\n      handler: " +
		handler + "\n      memory: 16MB\n"
}

// lintFixture writes a one-function project whose source declares GetPing, and
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
