package file

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/cmd/project"
)

// The fixtures `drift file migrate` is proven against live beside the engine,
// in cmd/project, with the schema fixture of the format this binary implements.
const migrateFixtures = "../project/testdata/migrate"

// withServedSchema isolates HOME and gives it the schema fixture, so these
// tests never read whatever schema this machine has cached.
func withServedSchema(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(migrateFixtures, "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// driftfileFrom copies a fixture into a fresh directory as its Driftfile.
func driftfileFrom(t *testing.T, fixture string) (path string, src []byte) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(migrateFixtures, fixture))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "Driftfile")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, src
}

// Without --write: the diff on stdout, the file untouched, and exit 0.
func TestMigrate_ShowsTheDiffAndWritesNothing(t *testing.T) {
	withServedSchema(t)
	path, src := driftfileFrom(t, "atomic_egress.yaml")
	name := strings.TrimPrefix(filepath.ToSlash(shortPath(path)), "/")

	var out, errOut bytes.Buffer
	if err := runMigrate(&out, &errOut, path, false); err != nil {
		t.Fatalf("pending changes must not fail the command: %v", err)
	}

	// Built line by line: a context line for a blank line is a single space,
	// which an editor trimming trailing whitespace would silently remove from
	// a raw string.
	want := strings.Join([]string{
		"--- a/" + name,
		"+++ b/" + name,
		"@@ -1,15 +1,17 @@",
		" slice: shop",
		" ",
		" atomic:",
		"+  pulses:",
		"+    - route: ping",
		"+      method: get",
		"+      handler: GetPing",
		"+",
		"+edge:",
		"   # Outbound calls go to Stripe only.",
		"   egress:",
		"     mode: allowlist",
		"     hosts:",
		"       - api.stripe.com # payments",
		"-  pulses:",
		"-    - route: ping",
		"-      method: get",
		"-      handler: GetPing",
		" ",
		" backbone:",
		"   queues: [orders]",
	}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("diff:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, src) {
		t.Errorf("the file changed without --write:\n%s", got)
	}
	for _, w := range []string{"atomic.egress → edge.egress", "nothing written", "drift file migrate --write"} {
		if !strings.Contains(errOut.String(), w) {
			t.Errorf("the report does not say %q:\n%s", w, errOut.String())
		}
	}
}

// With --write: the file becomes the golden output, byte for byte.
func TestMigrate_WriteWritesTheMigratedFile(t *testing.T) {
	withServedSchema(t)
	path, _ := driftfileFrom(t, "realistic.yaml")
	want, err := os.ReadFile(filepath.Join(migrateFixtures, "realistic.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runMigrate(&out, &errOut, path, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Errorf("the written file is not the golden output:\n%s", got)
	}
	if !strings.Contains(out.String(), "11 keys rewritten") ||
		!strings.Contains(out.String(), "atomic.functions → atomic.pulses") {
		t.Errorf("the result does not say what was rewritten:\n%s", out.String())
	}
	// The keys it could not move are named, never dropped in silence.
	if !strings.Contains(errOut.String(), "atomic.pulses[].memory") ||
		!strings.Contains(errOut.String(), "drift slice resize") {
		t.Errorf("the kept keys are not reported with what to do instead:\n%s", errOut.String())
	}

	// And the second run finds nothing left.
	out.Reset()
	if err := runMigrate(&out, &errOut, path, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to change") {
		t.Errorf("a migrated file still has changes pending:\n%s", out.String())
	}
}

// A current file says so, exits 0, and is not touched, with or without --write.
func TestMigrate_ACurrentFileReportsSo(t *testing.T) {
	withServedSchema(t)
	for _, write := range []bool{false, true} {
		path, src := driftfileFrom(t, "already_current.yaml")
		var out, errOut bytes.Buffer
		if err := runMigrate(&out, &errOut, path, write); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "is current; nothing to change") {
			t.Errorf("write=%v: a current file did not say so:\n%s", write, out.String())
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, src) {
			t.Errorf("write=%v: a current file was rewritten", write)
		}
	}
}

// A file migrate refuses is left exactly as it was, even with --write.
func TestMigrate_ARefusalLeavesTheFileUntouched(t *testing.T) {
	withServedSchema(t)
	src := []byte("slice: shop\natomic:\n  rate_limit: 600/min\n  functions:\n" +
		"    - {route: ping, method: get, handler: GetPing}\nedge:\n  rate_limit: 60/min\n")
	path := filepath.Join(t.TempDir(), "Driftfile")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := runMigrate(&out, &errOut, path, true)
	var refusal *project.MigrationRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("both spellings were not refused: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, src) {
		t.Errorf("a refused file was changed:\n%s", got)
	}
}

// No schema on this machine: refuse, as lint does, rather than claim the
// result validates.
func TestMigrate_RefusesWithNoSchema(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := driftfileFrom(t, "slice_name.yaml")

	err := runMigrate(&bytes.Buffer{}, &bytes.Buffer{}, path, false)
	if !errors.Is(err, project.ErrNoSchema) {
		t.Fatalf("want ErrNoSchema, got %v", err)
	}
}

// A file the format refuses is reported, not rewritten.
func TestMigrate_RefusesAnInvalidFile(t *testing.T) {
	withServedSchema(t)
	src := []byte("slice: shop\nnot_a_key: true\n")
	path := filepath.Join(t.TempDir(), "Driftfile")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runMigrate(&bytes.Buffer{}, &bytes.Buffer{}, path, true)
	if err == nil || !strings.Contains(err.Error(), "drift file lint") {
		t.Fatalf("an invalid file was not refused with a pointer to lint: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, src) {
		t.Errorf("an invalid file was changed:\n%s", got)
	}
}

// It is a `drift file` verb, three words, with --write.
func TestMigrate_IsAFileVerb(t *testing.T) {
	cmd := findVerb(GetCmd(), "migrate")
	if cmd.Flags().Lookup("write") == nil {
		t.Error("drift file migrate has no --write flag")
	}
	if got := cmd.CommandPath(); got != "file migrate" {
		t.Errorf("command path = %q, want it under the file noun", got)
	}
}
