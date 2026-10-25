package file

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// explainOut runs `drift file explain` with args and returns what it printed.
func explainOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := getExplainCmd()
	cmd.SetOut(&out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// Bare explain lists the blocks a Driftfile can hold, and needs no schema and
// no network: the format is this binary's own.
func TestExplain_ListsTheBlocksOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no cached schema anywhere
	out, err := explainOut(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BLOCKS:", `slice "name" {}`, "atomic {}", "backbone {}", "canvas {}", `environment "name" {}`} {
		if !strings.Contains(out, want) {
			t.Errorf("bare explain does not list %q:\n%s", want, out)
		}
	}
}

// A block prints its labels, its attributes with their types and whether each
// is required, and the blocks inside it.
func TestExplain_ABlock(t *testing.T) {
	out, err := explainOut(t, "atomic.element.pulse")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`BLOCK:  pulse "trigger" {}`, "inside atomic.element",
		"handler = string   -required-",
		`memory = size ("32MiB": whole KiB, MiB or GiB)   -required-`,
		"cron = string\n",
		"BLOCKS:", "auth {}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain atomic.element.pulse does not print %q:\n%s", want, out)
		}
	}
}

// An attribute prints its type, where it sits, and its whole description.
func TestExplain_AnAttribute(t *testing.T) {
	out, err := explainOut(t, "atomic.element.pulse.memory")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ATTRIBUTE:  memory = size", "-required-", `inside pulse "trigger"`, "rewrites it from what the slice measured"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain atomic.element.pulse.memory does not print %q:\n%s", want, out)
		}
	}
}

// --recursive prints the whole tree under a path.
func TestExplain_Recursive(t *testing.T) {
	out, err := explainOut(t, "slice", "--recursive")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"billing = string", "limits {", "backup_retention = duration"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain slice --recursive does not print %q:\n%s", want, out)
		}
	}
}

// A name one edit away is suggested, and what the block does hold is listed.
func TestExplain_AnUnknownNameSuggestsTheNearest(t *testing.T) {
	_, err := explainOut(t, "atomic.element.puls")
	if err == nil {
		t.Fatal("an unknown name was explained")
	}
	for _, want := range []string{`a element block holds no block or attribute "puls"`, "did you mean: pulse", "available here:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// What belongs to `drift file resolve` is refused by naming it: a path on disk,
// or an environment.
func TestExplain_RefusesWhatBelongsToResolve(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte("slice \"x\" { billing = \"free\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"Driftfile"}, {"./anything"}, {"--env", "staging"}} {
		_, err := explainOut(t, args...)
		if err == nil || !strings.Contains(err.Error(), "drift file resolve") {
			t.Errorf("explain %v: got %v, want a refusal naming drift file resolve", args, err)
		}
	}
}
