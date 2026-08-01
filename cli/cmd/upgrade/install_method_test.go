package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

// `under` is what keeps a neighbouring directory from being mistaken for a
// Homebrew prefix. A plain strings.HasPrefix says "/opt/homebrew-old/bin/drift"
// is inside "/opt/homebrew", and the user would then be told to run
// `brew upgrade` for a binary Homebrew has never heard of.
func TestUnder_DoesNotMatchASiblingWithASharedPrefix(t *testing.T) {
	if under("/opt/homebrew-old/bin/drift", "/opt/homebrew") {
		t.Error("a sibling directory sharing a name prefix is NOT inside it")
	}
	if under("/opt/homebrewx", "/opt/homebrew") {
		t.Error("adjacent path must not match")
	}
	if !under("/opt/homebrew/bin/drift", "/opt/homebrew") {
		t.Error("a real child must match")
	}
	if !under("/opt/homebrew", "/opt/homebrew") {
		t.Error("the directory itself counts as inside it")
	}
}

// Trailing separators and unclean paths are normalised, so a prefix taken from
// an environment variable does not have to be tidy to work.
func TestUnder_NormalisesBothSides(t *testing.T) {
	for _, tc := range []struct{ path, dir string }{
		{"/opt/homebrew/bin/drift", "/opt/homebrew/"},
		{"/opt/homebrew//bin/drift", "/opt/homebrew"},
		{"/opt/homebrew/./bin/drift", "/opt/homebrew"},
	} {
		if !under(tc.path, tc.dir) {
			t.Errorf("under(%q, %q) should be true", tc.path, tc.dir)
		}
	}
	if under("/anything", "") {
		t.Error("an empty dir must never match — an unset env var is not a prefix")
	}
}

// An explicit HOMEBREW_PREFIX must win, because somebody who moved their prefix
// has told us exactly where it is and we should not fall back to guessing.
func TestDetectInstallMethod_HonoursAnExplicitHomebrewPrefix(t *testing.T) {
	// The binary under test is the compiled test binary, which lives in a temp
	// dir of the toolchain's choosing — so point the prefix AT it rather than
	// trying to relocate the executable.
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve own executable")
	}
	t.Setenv("HOMEBREW_PREFIX", filepath.Dir(exe))
	if m, _ := DetectInstallMethod(); m != MethodHomebrew {
		t.Errorf("an explicit HOMEBREW_PREFIX containing the binary must classify as homebrew, got %v", m)
	}
}

// GOBIN takes precedence over GOPATH, mirroring the go tool's own rule.
func TestDetectInstallMethod_GobinBeatsGopath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve own executable")
	}
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("GOBIN", filepath.Dir(exe))
	t.Setenv("GOPATH", t.TempDir())
	if m, _ := DetectInstallMethod(); m != MethodGoInstall {
		t.Errorf("a binary inside GOBIN is a go install, got %v", m)
	}
}

// Anything we cannot place is UNKNOWN, and unknown must stay unknown. Guessing
// "homebrew" would send the user to a command that fails; guessing
// "go install" would have them write a second binary they did not ask for.
func TestDetectInstallMethod_UnplaceableBinaryIsUnknown(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "/nonexistent-prefix-for-test")
	t.Setenv("GOBIN", "/nonexistent-gobin-for-test")
	t.Setenv("GOPATH", "/nonexistent-gopath-for-test")
	m, path := DetectInstallMethod()
	if m != MethodUnknown {
		t.Errorf("a binary in none of the known roots must be unknown, got %v", m)
	}
	if path == "" {
		t.Error("the resolved path should still be reported even when unclassified")
	}
}

// The names are user-visible in diagnostics; pin them so a reordering of the
// iota block cannot silently relabel them.
func TestInstallMethod_Strings(t *testing.T) {
	for m, want := range map[InstallMethod]string{
		MethodHomebrew:  "homebrew",
		MethodGoInstall: "go install",
		MethodUnknown:   "unknown",
	} {
		if got := m.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Every standard Homebrew layout classifies as Homebrew — Apple Silicon, Intel
// macOS and Linuxbrew all use different roots, and missing one silently sends
// that platform's users down the go install path, which would write a second
// binary they never asked for.
//
// These are real resolved Cellar paths, because that is what os.Executable
// returns after EvalSymlinks: Homebrew keeps the binary in
// <prefix>/Cellar/<formula>/<version>/bin and links it from <prefix>/bin.
func TestClassify_RecognisesEveryStandardHomebrewLayout(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/nonexistent-gopath-for-test")

	for _, p := range []string{
		"/opt/homebrew/Cellar/drift/0.2.0/bin/drift",              // Apple Silicon
		"/opt/homebrew/bin/drift",                                 // …and its link
		"/usr/local/Cellar/drift/0.2.0/bin/drift",                 // Intel macOS
		"/home/linuxbrew/.linuxbrew/Cellar/drift/0.2.0/bin/drift", // Linuxbrew
		"/home/linuxbrew/.linuxbrew/bin/drift",                    // …and its link
	} {
		if got := classify(p); got != MethodHomebrew {
			t.Errorf("classify(%q) = %v, want homebrew", p, got)
		}
	}
}

// The go install layout, and the boundary case that a naive prefix match gets
// wrong.
func TestClassify_RecognisesGoInstallAndRejectsLookalikes(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/home/alice/go")

	if got := classify("/home/alice/go/bin/drift"); got != MethodGoInstall {
		t.Errorf("a GOPATH/bin binary is a go install, got %v", got)
	}
	// Neighbouring directories that merely share a name prefix are NOT inside.
	for _, p := range []string{
		"/opt/homebrew-old/bin/drift",
		"/usr/local/CellarX/drift/bin/drift",
		"/home/alice/gopher/bin/drift",
	} {
		if got := classify(p); got != MethodUnknown {
			t.Errorf("classify(%q) = %v, want unknown — a shared name prefix is not containment", p, got)
		}
	}
}

// A multi-entry GOPATH is valid and each entry counts.
func TestClassify_HandlesAMultiEntryGopath(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "")
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/first/go"+string(os.PathListSeparator)+"/second/go")

	if got := classify("/second/go/bin/drift"); got != MethodGoInstall {
		t.Errorf("the second GOPATH entry must count too, got %v", got)
	}
}
