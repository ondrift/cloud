package portal

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// fakeAccessToken builds a token decodeTokenClaims can read: a real username
// claim, so GetUsername() succeeds and ensureLoggedIn gets past its "no
// session at all" branch, and an exp far in the past, so TokenExpired() is
// true and the refresh branch is what actually runs.
func fakeAccessToken(t *testing.T) string {
	t.Helper()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"testuser","exp":1}`))
	return "header." + payload
}

// A personal access token replaces the session entirely and is checked first
// everywhere else an authenticated request is built. ensureLoggedIn used to
// check GetUsername() first instead, which reads "" for a token that has not
// been exchanged yet — the normal state before any request has been made —
// and opened a login window nothing was meant to answer (flow enumeration
// finding PLM-74). No session file exists in this test at all: if the PAT
// check were not first, this would fall through to runLoginWindow and block
// forever reading a username from the test process's stdin.
func TestEnsureLoggedIn_APersonalAccessTokenSkipsTheLoginWindowEntirely(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.TokenEnv, "drift_pat_test_does_not_need_to_be_real")

	if err := ensureLoggedIn(); err != nil {
		t.Errorf("ensureLoggedIn with a configured token: %v", err)
	}
}

// A refresh that fails because the platform could not be reached says nothing
// about the session — collapsing it into "Your session has expired" told a
// user mid-outage to log out of a platform that could not log them back in,
// the exact failure common.DoRequest's own 401 handling was rewritten to
// avoid (flow enumeration finding PLM-75). Asserted by checking ensureLoggedIn
// returns the maintenance message rather than opening the login window: if it
// fell through to runLoginWindow, this test would hang reading stdin instead
// of failing cleanly, which is itself the point of separating this branch out.
func TestEnsureLoggedIn_AnUnreachablePlatformDuringRefreshIsNotAnExpiredSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.TokenEnv, "") // this test is about the session path, not the PAT one

	// A real username claim so GetUsername() succeeds and this reaches the
	// TokenExpired/refresh branch at all, with exp far in the past so
	// TokenExpired() is true and RefreshAccessToken actually runs.
	if err := common.SaveSession(fakeAccessToken(t), "not-a-real-refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	oldBase := common.APIBaseURL
	common.APIBaseURL = srv.URL
	defer func() { common.APIBaseURL = oldBase }()

	err := ensureLoggedIn()
	if err == nil {
		t.Fatal("ensureLoggedIn returned nil against a refresh endpoint answering 500")
	}
	if err.Error() != common.MaintenanceMessage {
		t.Errorf("got %q, want the maintenance message — a 5xx refresh is the platform, not an expired session", err.Error())
	}
	if errors.Is(err, common.ErrSessionRejected) {
		t.Error("a 500 was read as a rejected session, not as the platform being unavailable")
	}
}
