package common

import (
	"testing"
)

// DRIFT_SLICE WINS over the session file, and this is the test that keeps it
// that way.
//
// A token carries an account rather than a slice, so a scripted caller has no
// `drift slice use` to have run. But the sharper case is a pipeline running on a
// developer's own machine, where a session file DOES exist: if the stored value
// won, the job would target whichever slice that developer last used rather than
// the one it names — silently, and differently on every machine.
func TestTheEnvironmentsSliceBeatsTheStoredOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(SliceEnv, "")

	if err := writeSessionMap(map[string]string{
		"token": "x", "refresh_token": "y", "active_slice": "the-stored-one",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if got := GetActiveSlice(); got != "the-stored-one" {
		t.Fatalf("with no %s set the stored slice must win, got %q", SliceEnv, got)
	}

	t.Setenv(SliceEnv, "the-named-one")
	if got := GetActiveSlice(); got != "the-named-one" {
		t.Errorf("%s did not win over the session file: got %q", SliceEnv, got)
	}
}

// Whitespace is trimmed, because a value pasted into CI config arrives with a
// trailing newline more often than not — and an untrimmed name goes into a URL
// authority, where it is a 400 at best.
func TestTheEnvironmentsSliceIsTrimmedAndBlankIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeSessionMap(map[string]string{
		"token": "x", "refresh_token": "y", "active_slice": "stored",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	t.Setenv(SliceEnv, "  spaced  \n")
	if got := GetActiveSlice(); got != "spaced" {
		t.Errorf("got %q, want the trimmed value", got)
	}

	// A variable set to whitespace is set to NOTHING, and must not beat the
	// stored value — otherwise an empty CI variable silently unsets the slice.
	t.Setenv(SliceEnv, "   ")
	if got := GetActiveSlice(); got != "stored" {
		t.Errorf("a blank %s overrode the stored slice: got %q", SliceEnv, got)
	}
}

// A PAT caller's access token is minted on demand and re-minted on a 401, so
// there is no "expired" state for them to act on. Answering true would send a
// pipeline down the re-login path, which is advice for a person at a terminal.
func TestATokenCallerIsNeverToldTheirSessionExpired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(TokenEnv, "")

	// No session at all: a person here really has nothing usable.
	if !TokenExpired() {
		t.Error("with no session and no token, expired must be true")
	}

	t.Setenv(TokenEnv, "drift_pat_whatever")
	if TokenExpired() {
		t.Error("a token caller was told their session expired — there is no session to expire")
	}
}

// PersonalAccessToken trims, for the same reason the slice name does: a value
// pasted into CI config carries a newline, and an untrimmed one goes out in an
// Authorization header where it is a refusal with no explanation.
func TestThePersonalAccessTokenIsTrimmed(t *testing.T) {
	t.Setenv(TokenEnv, "  drift_pat_abc \n")
	if got := PersonalAccessToken(); got != "drift_pat_abc" {
		t.Errorf("got %q, want the trimmed token", got)
	}
	t.Setenv(TokenEnv, "   ")
	if got := PersonalAccessToken(); got != "" {
		t.Errorf("a blank variable reported a token: %q", got)
	}
}

// currentAccessToken must NOT reach the network. It answers "what identity is
// this invocation acting with", which `drift account whoami` and the expiry
// check read — and a read that silently exchanged would make those able to fail
// on a plane.
func TestReadingTheCurrentTokenNeverExchanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(TokenEnv, "drift_pat_abc")
	forgetExchangedToken()

	// APIBaseURL points at a port nothing is listening on, so an exchange
	// attempt would surface as a connection error rather than as the "nothing
	// exchanged yet" this must report.
	t.Setenv("DRIFT_API_URL", "http://127.0.0.1:1")
	prev := APIBaseURL
	APIBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { APIBaseURL = prev })

	if _, _, err := currentAccessToken(); err == nil {
		t.Fatal("reported a token before any exchange had happened")
	}
	if got := GetUsername(); got != "" {
		t.Errorf("GetUsername returned %q with nothing exchanged", got)
	}
}
