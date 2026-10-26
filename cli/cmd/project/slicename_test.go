package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

func writeNamedDriftfile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A command run anywhere inside a project acts on the slice the project's
// Driftfile names, found by walking up from the working directory.
func TestTheDriftfileNamesTheSliceFromAnywhereInsideTheProject(t *testing.T) {
	t.Setenv(common.SliceEnv, "")
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeNamedDriftfile(t, root, "# my project\nslice \"orders\" {}\n")
	inner := filepath.Join(root, "atomic", "billing")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner)

	target, err := common.ResolveSlice()
	if err != nil {
		t.Fatalf("ResolveSlice: %v", err)
	}
	if target.Name != "orders" || target.From != common.SliceFromDriftfile {
		t.Errorf("resolved %+v, want slice orders from the Driftfile", target)
	}
	if got, _ := filepath.EvalSymlinks(target.Path); got != mustEval(t, filepath.Join(root, "Driftfile")) {
		t.Errorf("the Driftfile reported is %q", target.Path)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The slice is read from the syntax tree and nothing is evaluated: a file whose
// other attributes read an unset env("…") still names its slice, for a command
// that never needed those values.
func TestTheSliceNameNeedsNoEnvironmentVariableToBeSet(t *testing.T) {
	name, err := sliceLabel("Driftfile", []byte(
		"slice \"shop\" {\n  labels = { owner = env(\"NOT_SET_ANYWHERE\") }\n}\n"+
			"backbone {\n  secret \"K\" { value = env(\"ALSO_NOT_SET\") }\n}\n"))
	if err != nil || name != "shop" {
		t.Errorf("sliceLabel = %q, %v; want shop", name, err)
	}
}

func TestADriftfileThatCannotNameOneSliceSaysWhy(t *testing.T) {
	for body, want := range map[string]string{
		"atomic {}\n":                                  "no slice block",
		"slice \"a\" {}\nslice \"b\" {}\n":             "2 slice blocks",
		"slice \"a\" {\n":                              "does not parse",
		"slice \"a\" {}\nenvironment \"prod\" {}\n":    "",
		"slice \"a\" { billing = \"monthly\" }\n":      "",
		"slice \"a-b\" {}\nenvironment \"x\" {}\n\n\n": "",
	} {
		name, err := sliceLabel("Driftfile", []byte(body))
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: unexpected error %v", body, err)
		case want == "" && name == "":
			t.Errorf("%q: no name", body)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: error %v, want it to say %q", body, err, want)
		}
	}
}
