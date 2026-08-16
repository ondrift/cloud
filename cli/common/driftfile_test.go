package common

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The refusal rung. A major difference means keys have been renamed, retyped or
// removed, so this binary would misread a file written for the served format —
// and every message the parse could produce would describe the symptom.
func TestFormatSkew_RefusesAServedMajorThisBinaryCannotRead(t *testing.T) {
	warning, err := driftfileFormatSkew("1.7.2", "2.0.0")
	if err == nil {
		t.Fatal("a served MAJOR ahead of the implemented one must be refused — " +
			"this binary cannot read a file written for it")
	}
	if warning != "" {
		t.Errorf("a refusal must not also warn, got %q", warning)
	}
	// The message has to carry both numbers and the remedy: "incompatible" with
	// no versions and no verb leaves the user with nothing to do.
	for _, want := range []string{"1.7.2", "2.0.0", "drift upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, got:\n%v", want, err)
		}
	}
}

// The warning rung, and the one that carries the weight: breaking changes ride
// a MINOR bump while the format's major rule is suspended, so this is the only
// rung that fires for them. Silence here is the failure being fixed.
func TestFormatSkew_WarnsWhenTheServedMinorIsAhead(t *testing.T) {
	warning, err := driftfileFormatSkew("1.7.2", "1.9.0")
	if err != nil {
		t.Fatalf("a minor ahead must not ground a client that can still parse: %v", err)
	}
	if warning == "" {
		t.Fatal("a served MINOR ahead of the implemented one must be announced — " +
			"it is the rung every alpha-era format break arrives on")
	}
	for _, want := range []string{"1.7.2", "1.9.0", "drift upgrade"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning should mention %q, got:\n%s", want, warning)
		}
	}
}

// One direction only. A client AHEAD of the platform understands everything the
// platform can express, so refusing or warning would ground a working client
// for no reason — in both the major and the minor position.
func TestFormatSkew_SaysNothingWhenThisBinaryIsAhead(t *testing.T) {
	for _, tc := range []struct{ implemented, served string }{
		{"2.0.0", "1.9.0"},
		{"1.9.0", "1.2.0"},
	} {
		warning, err := driftfileFormatSkew(tc.implemented, tc.served)
		if err != nil || warning != "" {
			t.Errorf("implemented %s vs served %s must pass silently, got err=%v warning=%q",
				tc.implemented, tc.served, err, warning)
		}
	}
}

// A PATCH difference is documentation only, so it changes nothing a client can
// act on. Warning about one trains people to ignore the warning that matters.
func TestFormatSkew_IgnoresAPatchDifference(t *testing.T) {
	for _, tc := range []struct{ implemented, served string }{
		{"1.7.2", "1.7.9"},
		{"1.7.9", "1.7.2"},
		{"1.7.2", "1.7.2"},
	} {
		warning, err := driftfileFormatSkew(tc.implemented, tc.served)
		if err != nil || warning != "" {
			t.Errorf("implemented %s vs served %s differ only in patch and must be silent, "+
				"got err=%v warning=%q", tc.implemented, tc.served, err, warning)
		}
	}
}

// A machine that has never reached the platform holds no schema, and one whose
// copy is unreadable holds no version. Both have their own remedy, and meeting
// either with a version complaint sends the user to fix the wrong thing.
func TestFormatSkew_IsSilentWhenTheServedVersionIsUnknown(t *testing.T) {
	for _, served := range []string{"", "not-a-version", "v", "..."} {
		warning, err := driftfileFormatSkew("1.7.2", served)
		if err != nil || warning != "" {
			t.Errorf("served %q is not a version this can grade and must be silent, "+
				"got err=%v warning=%q", served, err, warning)
		}
	}
}

// The constant is the whole basis of the comparison. A value that does not
// parse grades as major 0, which is behind every real served version and would
// refuse every deploy on the planet.
func TestImplementedFormatIsAReadableVersion(t *testing.T) {
	if DriftfileMajor(ImplementedDriftfileFormat) == 0 {
		t.Fatalf("ImplementedDriftfileFormat = %q has no readable major, so every served "+
			"version outranks it", ImplementedDriftfileFormat)
	}
	if strings.Count(ImplementedDriftfileFormat, ".") != 2 {
		t.Errorf("ImplementedDriftfileFormat = %q should be a full semver, so the minor "+
			"rung has something to read", ImplementedDriftfileFormat)
	}
}

// semverPart is what both rungs read through, so the shapes it must survive are
// pinned here rather than left to the callers to discover.
func TestSemverPart(t *testing.T) {
	for _, tc := range []struct {
		v     string
		major int
		minor int
	}{
		{"1.7.2", 1, 7},
		{"v2.0.0", 2, 0},
		{"2", 2, 0},
		{"1.9", 1, 9},
		{"", 0, 0},
		{"1.x.0", 1, 0},
		{"garbage", 0, 0},
	} {
		if got := DriftfileMajor(tc.v); got != tc.major {
			t.Errorf("DriftfileMajor(%q) = %d, want %d", tc.v, got, tc.major)
		}
		if got := driftfileMinor(tc.v); got != tc.minor {
			t.Errorf("driftfileMinor(%q) = %d, want %d", tc.v, got, tc.minor)
		}
	}
}

// The reporter is what commands actually call, so the wiring between "there is
// a warning" and "the user sees it" is asserted rather than assumed.
func TestReportSkew_WritesTheWarningAndReturnsTheRefusal(t *testing.T) {
	t.Run("a warning reaches the writer and the command continues", func(t *testing.T) {
		withCachedSchemaVersion(t, bumpMinor(ImplementedDriftfileFormat))
		var out bytes.Buffer
		if err := ReportDriftfileFormatSkew(&out); err != nil {
			t.Fatalf("a minor skew must not stop the command: %v", err)
		}
		if out.Len() == 0 {
			t.Error("the warning was graded but never written, so nobody sees it")
		}
	})

	t.Run("a refusal is returned and nothing is written", func(t *testing.T) {
		withCachedSchemaVersion(t, "99.0.0")
		var out bytes.Buffer
		if err := ReportDriftfileFormatSkew(&out); err == nil {
			t.Fatal("a served major ahead of this binary must be returned as an error")
		}
		if out.Len() != 0 {
			t.Errorf("a refusal is the return value, not a warning, got %q", out.String())
		}
	})

	t.Run("no schema on the machine is not a version complaint", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		var out bytes.Buffer
		if err := ReportDriftfileFormatSkew(&out); err != nil {
			t.Fatalf("a machine with no schema has its own remedy, got: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("nothing to compare must say nothing, got %q", out.String())
		}
	})
}

// Every authenticated request declares the format this binary implements.
//
// The platform otherwise cannot tell an old client from a new one: nothing in
// the CLI sends a version or a User-Agent, so a deploy from a binary too old to
// read the manifest it is applying is indistinguishable from any other. A gate
// shipped in the client protects only the people who already upgraded; the
// population that needs protecting is the one running the old binary, and only
// the server can refuse them.
//
// It carries ImplementedDriftfileFormat rather than the cached schema version.
// The cache says what the PLATFORM last served — which the platform already
// knows — while what it cannot know is which format this binary can read.
func TestAuthenticatedRequestDeclaresTheImplementedFormat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveSession("tok", "ref"); err != nil {
		t.Fatal(err)
	}

	req, err := NewAuthenticatedRequest("GET", "https://example.invalid/ops/whatever", nil)
	if err != nil {
		t.Fatal(err)
	}

	got := req.Header.Get(DriftfileFormatHeader)
	if got == "" {
		t.Fatalf("%s is absent, so the platform cannot tell this client from one too old "+
			"to read the manifest it is deploying", DriftfileFormatHeader)
	}
	if got != ImplementedDriftfileFormat {
		t.Errorf("%s = %q, want %q — the header and the client-side gate must read one "+
			"number, not two", DriftfileFormatHeader, got, ImplementedDriftfileFormat)
	}
}

// withCachedSchemaVersion points this machine's schema cache at a temporary HOME
// holding a document that declares `version`. Only the version is read on this
// path, so the rest of the document is deliberately minimal.
func withCachedSchemaVersion(t *testing.T, version string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"$schema":"http://json-schema.org/draft-07/schema#","version":"` + version + `","properties":{}}`
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// bumpMinor derives a version one minor ahead of the given one, so the warning
// test moves with the constant instead of pinning a number that goes stale the
// first time the format changes.
func bumpMinor(v string) string {
	return strconv.Itoa(DriftfileMajor(v)) + "." + strconv.Itoa(driftfileMinor(v)+1) + ".0"
}
