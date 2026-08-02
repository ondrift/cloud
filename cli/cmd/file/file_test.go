package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/cmd/project"
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

// function_memory is REQUIRED in practice — a Driftfile declaring a function
// without it is rejected on create and resize, though the spec calls it optional.
// The scaffold exists to close that gap, so its presence is asserted rather than
// left to whoever next edits the template.
// Asserted through the PARSER, on the BASE slice, not by searching the text.
// A string match for "function_memory:" passes on the staging override alone —
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
	if m.Slice().Str("atomic", "function_memory") == "" {
		t.Error("the scaffold's BASE slice omits function_memory — a Driftfile that " +
			"declares a function without it is rejected on create and resize with " +
			"'function_memory must be between 32MB and 256MB (got 0MB)', which is the " +
			"first thing the user would hit")
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
  function_memory: 128MB # inline note
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
  function_memory: 128MB
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
	assertOrder(t, got, "function_memory:", "functions:")
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
	src := []byte("environments:\n  prod: {}\nname: demo\natomic:\n  functions: []\n  function_memory: 128MB\n")
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
