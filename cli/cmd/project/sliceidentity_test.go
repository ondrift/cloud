package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The identity is a reference to a slice the configurator created.
func TestSliceIdentity_SliceKeyIsTheIdentity(t *testing.T) {
	m := parseIdentity(t, "slice: prorata\n")
	if m.Name() != "prorata" {
		t.Errorf("Name() = %q, want prorata", m.Name())
	}
}

// The retired spelling keeps applying unchanged, and says once what to write.
func TestSliceIdentity_TheRetiredKeyStillResolvesAndSaysSoOnce(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	m := parseIdentity(t, "name: prorata\n")
	if m.Name() != "prorata" {
		t.Errorf("Name() = %q, want prorata — the retired spelling must resolve to the "+
			"same identity, or an existing Driftfile stops applying", m.Name())
	}
	if n := strings.Count(notices.String(), `"name" is deprecated`); n != 1 {
		t.Errorf("the retired key was announced %d times, want 1:\n%s", n, notices.String())
	}
	if !strings.Contains(notices.String(), `"slice"`) {
		t.Errorf("the notice must name what to write instead, got:\n%s", notices.String())
	}
}

// THE test the card asks for first: `drift file stop` and `logs` find their
// container through ParseProjectName, which never runs the key walker — it is a
// raw decode that deliberately skips validation and secret resolution. A reader
// left on the retired key alone makes every container started from a
// `slice:`-keyed file unstoppable except by hand.
func TestSliceIdentity_TheCheapDecodeAcceptsBothSpellings(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"slice: prorata\n", "prorata"},
		{"name: prorata\n", "prorata"},
		// A document carrying both means the current spelling, matching what the
		// walker does with the pair.
		{"slice: fresh\nname: stale\n", "fresh"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "Driftfile")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ParseProjectName(path)
		if err != nil {
			t.Errorf("ParseProjectName(%q) failed: %v", tc.body, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseProjectName(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// A file naming no slice at all is still an error, and the message says what to
// add rather than what is missing.
func TestSliceIdentity_TheCheapDecodeStillRefusesAFileNamingNoSlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte("atomic: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseProjectName(path)
	if err == nil {
		t.Fatal("a Driftfile naming no slice must be refused")
	}
	if !strings.Contains(err.Error(), "slice") {
		t.Errorf("the error should name the key to add, got: %v", err)
	}
}

// The one check the schema structurally cannot perform: the DERIVED slice name
// never appears in the Driftfile, so nothing but this catches a long identity
// plus `-staging` going over 32 characters.
//
// NamePattern reading a key that stopped existing would return nil and turn this
// guard into a silent skip, which is the failure mode being pinned.
func TestSliceIdentity_ADerivedNameOverTheLimitIsStillRefused(t *testing.T) {
	if NamePattern() == nil {
		t.Fatal("NamePattern() is nil against this machine's schema, so the derived-name " +
			"check is skipped entirely rather than failed")
	}

	long := strings.Repeat("a", 28) // 28 + len("-staging") = 36, over the 32 limit
	m := parseIdentity(t, "slice: "+long+"\nenvironments:\n  staging: {}\n  prod: {}\n")

	if _, err := m.SelectEnvironment("staging", true); err == nil {
		t.Fatalf("a derived slice name of %d characters was accepted", len(long)+len("-staging"))
	}
}

// The same project under an environment that owns the bare identity still
// resolves, so the guard above is refusing length rather than everything.
func TestSliceIdentity_TheDerivedNameGuardIsNotRefusingEverything(t *testing.T) {
	m := parseIdentity(t, "slice: prorata\nenvironments:\n  staging: {}\n  prod: {}\n")

	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatalf("prorata-staging is 15 characters and must be accepted: %v", err)
	}
	if m.Name() != "prorata-staging" {
		t.Errorf("Name() = %q, want prorata-staging", m.Name())
	}
}

// SetName writes where Name reads. They are two halves of one fact, and
// SelectEnvironment is the only caller of the setter.
func TestSliceIdentity_SetNameWritesWhereNameReads(t *testing.T) {
	m := parseIdentity(t, "slice: prorata\n")
	m.SetName("prorata-staging")
	if m.Name() != "prorata-staging" {
		t.Errorf("Name() = %q after SetName, want prorata-staging", m.Name())
	}
}

func parseIdentity(t *testing.T, body string) *Manifest {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}
