package project

import (
	"strings"
	"testing"
)

const identityFile = `variable "m" { default = "32MiB" }
slice "prorata" { billing = "monthly" }
environment "staging" {}
environment "prod" {}
environment "demo" {
  slice = "prorata-demo-box"
}
`

// The slice block's label is the identity.
func TestSliceIdentity_TheSliceBlockIsTheIdentity(t *testing.T) {
	withRealSchema(t)
	if m := planManifest(t, identityFile); m.Name() != "prorata" {
		t.Errorf("Name() = %q, want prorata", m.Name())
	}
}

// THE property: `drift file stop` and `logs` find their container through
// ParseProjectName, which reads names without compiling, and `drift file run`
// names the container from the compile. The two agree for the default slice
// and for every environment, or a container `run` started is one `stop` cannot
// find.
func TestSliceIdentity_TheCheapReadNamesWhatTheCompileNames(t *testing.T) {
	withRealSchema(t)
	path := writeDriftfile(t, identityFile)
	for env, want := range map[string]string{
		"":        "prorata",
		"staging": "prorata-staging",
		"prod":    "prorata",
		"demo":    "prorata-demo-box",
	} {
		got, err := ParseProjectName(path, env)
		if err != nil {
			t.Errorf("ParseProjectName(%q): %v", env, err)
			continue
		}
		c, err := CompileDriftfileEnv(path, env)
		if err != nil {
			t.Fatalf("compile %q: %v", env, err)
		}
		if got != want || c.Manifest.Name() != want {
			t.Errorf("environment %q: the cheap read names %q and the compile %q; want %q", env, got, c.Manifest.Name(), want)
		}
	}
}

// The cheap read needs nothing the compile needs: a variable read from an unset
// environment variable does not stop it.
func TestSliceIdentity_TheCheapReadNeedsNoVariable(t *testing.T) {
	path := writeDriftfile(t, "variable \"k\" { default = env(\"DRIFT_TEST_SURELY_UNSET\") }\nslice \"prorata\" { billing = \"monthly\" }\n")
	if got, err := ParseProjectName(path, ""); err != nil || got != "prorata" {
		t.Errorf("ParseProjectName = %q, %v; want prorata with no variable set", got, err)
	}
}

// What the cheap read cannot answer, it refuses by name: no slice block, an
// environment the file does not declare, and an environment slice written as
// an expression only a full parse evaluates.
func TestSliceIdentity_TheCheapReadRefusesWhatItCannotName(t *testing.T) {
	for _, tc := range []struct{ body, env, want string }{
		{"queue \"jobs\" {}\n", "", `add slice "<name>"`},
		{identityFile, "nope", `there is no environment "nope"`},
		{"variable \"s\" { default = \"x\" }\nslice \"p\" { billing = \"monthly\" }\nenvironment \"e\" { slice = var.s }\n",
			"e", "write it as a string"},
	} {
		_, err := ParseProjectName(writeDriftfile(t, tc.body), tc.env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("env %q: got %v, want a refusal naming %q", tc.env, err, tc.want)
		}
	}
}

// The derived slice name never appears in the file, so nothing but the compile
// catches a long name plus `-staging` going over 32 characters.
//
// NamePattern reading a key that stopped existing would return nil and turn this
// guard into a silent skip, which is the failure mode being pinned.
func TestSliceIdentity_ADerivedNameOverTheLimitIsRefused(t *testing.T) {
	withRealSchema(t)
	if NamePattern() == nil {
		t.Fatal("NamePattern() is nil against this machine's schema, so the derived-name " +
			"check is skipped entirely rather than failed")
	}
	long := strings.Repeat("a", 28) // 28 + len("-staging") = 36, over the 32 limit
	path := writeDriftfile(t, "slice \""+long+"\" { billing = \"monthly\" }\nenvironment \"staging\" {}\n")
	if _, err := CompileDriftfileEnv(path, "staging"); err == nil || !strings.Contains(err.Error(), "not a slice name") {
		t.Fatalf("a derived slice name of %d characters: got %v", len(long)+len("-staging"), err)
	}

	// The control: the same file's own slice, and a short name's environment,
	// compile, so the guard is refusing length rather than everything.
	if _, err := CompileDriftfileEnv(path, ""); err != nil {
		t.Errorf("the default slice of the same file was refused: %v", err)
	}
	if _, err := CompileDriftfileEnv(writeDriftfile(t, identityFile), "staging"); err != nil {
		t.Errorf("prorata-staging is 15 characters and must be accepted: %v", err)
	}
}
