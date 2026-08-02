package main

import (
	"runtime/debug"
	"testing"
)

// The release stamp is authoritative when it is there — the release job builds
// with -ldflags and then asserts the binary echoes the tag, so nothing may
// override it.
func TestResolveVersion_LdflagsStampWins(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })

	version = "v1.2.3"
	if got := resolveVersion(); got != "v1.2.3" {
		t.Errorf("a stamped binary must report its stamp, got %q", got)
	}
}

// THE bug, at the level this file can reach.
//
// `go install` does not apply ldflags, so an upgraded binary has no stamp. It
// used to fall back to a hardcoded "v0.1.1", which meant every binary installed
// by `drift upgrade` reported v0.1.1 forever — so the next `drift upgrade`
// compared v0.1.1 against the latest release, found it behind, reinstalled, and
// produced another binary reporting v0.1.1. Verified by hand on this machine:
// `go install`ed at v0.4.0, `--version` said v0.1.1, and upgrading again
// reported "v0.1.1 → v0.4.0" a second time.
//
// What this test pins is that no release number is invented when there is no
// stamp and no module version. What it CANNOT reach is the case that actually
// fixes the loop — debug.ReadBuildInfo().Main.Version being a real tag — because
// a test binary is always built, never `go install`ed from a module proxy. That
// path is confirmed instead by `go version -m $(which drift)` printing
//
//	mod  github.com/ondrift/cloud/cli  v0.4.0
//
// on the very binary that was reporting v0.1.1: the right answer was embedded
// all along, and nothing read it.
func TestResolveVersion_UnstampedNeverInventsARelease(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })

	version = ""
	got := resolveVersion()

	// Whatever the toolchain reports for a test binary, it must never be a
	// plausible-looking release — that is what made the loop invisible.
	if got == "v0.1.1" {
		t.Fatal("an unstamped binary reported a hardcoded release number, which is the " +
			"bug: `drift upgrade` compares this against the latest release and reinstalls forever")
	}
	if got == "" {
		t.Error("version must never be empty — it is printed by --version and read by upgrade")
	}

	// And it must agree with what the toolchain actually recorded.
	want := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			want = v
		}
	}
	if got != want {
		t.Errorf("resolveVersion() = %q, want %q (the module version, or dev when there is none)", got, want)
	}
}
