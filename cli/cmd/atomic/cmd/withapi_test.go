package atomic_cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// withAPI points the CLI at a stub API for one test and restores the real one.
//
// It also seeds a session in a scratch HOME. Without that these tests pass only
// on a machine that happens to be logged in: every request needs a token, so on
// a fresh runner the command fails while reading the session and never reaches
// the status code under test — a failure that looks exactly like the one being
// asserted, for entirely the wrong reason.
func withAPI(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
}
