package common

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A refresh the platform answers 401 can mean three different things, and the CLI
// printed the same "session expired" line for all of them:
//
//   - the refresh token simply aged out or was already rotated away: log in
//     again;
//   - a refresh arrived from a different workstation than the one that logged in,
//     or with a token that had already been used. The platform treats that as a
//     stolen session file, ends EVERY session on the account on purpose, and says
//     why in the response body;
//   - the platform could not answer at all (covered elsewhere).
//
// The second one warrants a password change, and nothing the person was told said
// so. These drive the real request path against a stub api.

// stubSession points the CLI at a server that answers a protected route 401 and
// the refresh route with `refresh`, under a scratch HOME holding a session.
func stubSession(t *testing.T, refresh func(w http.ResponseWriter)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/refresh":
			refresh(w)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)

	was := APIBaseURL
	APIBaseURL = srv.URL
	t.Cleanup(func() { APIBaseURL = was })

	t.Setenv("HOME", t.TempDir())
	t.Setenv(TokenEnv, "")
	if err := SaveSession("expired-access", "refresh-token"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func reject(body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}
}

func askForSomething(t *testing.T) error {
	t.Helper()
	_, err := DoRequest(http.MethodGet, APIBaseURL+"/ops/slice", nil)
	return err
}

// THE ONE THAT MATTERS: the platform's theft response is named, with both next
// steps, and is not the plain expiry line.
func TestARevokedSessionSaysWhyAndWhatToDo(t *testing.T) {
	for name, body := range map[string]string{
		"a different workstation": `{"error":"device mismatch; please log in again"}`,
		"a spent token replayed":  `{"error":"refresh token reuse detected; please log in again"}`,
	} {
		t.Run(name, func(t *testing.T) {
			stubSession(t, reject(body))
			err := askForSomething(t)
			if err == nil {
				t.Fatal("a rejected refresh returned no error")
			}
			msg := err.Error()
			for _, want := range []string{"drift account login", "drift account reset-password", "every session"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the message does not say %q:\n%s", want, msg)
				}
			}
			if strings.HasPrefix(msg, "session expired") {
				t.Errorf("a revoked session was reported as an ordinary expiry:\n%s", msg)
			}
		})
	}
}

// THE CONTROL: an ordinary rejected refresh keeps the plain expiry line and does
// not frighten anyone about a theft.
func TestAnOrdinaryExpiryStaysAnExpiry(t *testing.T) {
	stubSession(t, reject(`{"error":"invalid or expired refresh token"}`))
	err := askForSomething(t)
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("an expired refresh token = %v, want the plain expiry line", err)
	}
	if strings.Contains(err.Error(), "reset-password") {
		t.Errorf("an ordinary expiry advised changing the password:\n%v", err)
	}
}

// An unavailable platform still never advises logging in: the guard that exists
// for the maintenance message holds for the new branch too.
func TestAnUnavailablePlatformStillSaysWait(t *testing.T) {
	stubSession(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	err := askForSomething(t)
	if err == nil || err.Error() != MaintenanceMessage {
		t.Fatalf("a 503 on refresh = %v, want the maintenance message", err)
	}
}

// The message goes through TransportError at the call sites, which prefixes the
// operation; it must still read as one sentence and keep both steps.
func TestARevokedSessionSurvivesTransportError(t *testing.T) {
	err := TransportError("list slices", errSessionRevokedForTest())
	if !strings.Contains(err.Error(), "reset-password") || !strings.Contains(err.Error(), "list slices") {
		t.Errorf("the wrapped message lost a step or the operation: %v", err)
	}
}

func errSessionRevokedForTest() error { return &revokedForTest{} }

type revokedForTest struct{}

func (*revokedForTest) Error() string { return SessionRevokedMessage }
