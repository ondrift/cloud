package common

import (
	"testing"
)

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
