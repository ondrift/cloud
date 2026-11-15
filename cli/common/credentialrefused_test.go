package common

import (
	"net/http"
	"strings"
	"testing"
)

// A 401 is an expired session unless the request says it is something else. The
// renderer tells a person to log in again for every 401, which is right for a
// session that ran out and wrong for a credential the platform turned down: a
// wrong password typed into a prompt, or a proof of identity it did not accept.
// There the platform's own reason is the message, and the person is not sent to
// the command that cannot help.

// A refused credential reads as the platform's reason, with no code and no advice
// to log in.
func TestAPIError_ARefusedCredentialIsThePlatformsReasonAndNotASessionExpiry(t *testing.T) {
	e := NewAPIError("verify your password", http.StatusUnauthorized, nil, []byte(`{"error":"password is incorrect"}`))
	e.CredentialRefused = true

	got := e.Error()
	if want := "Couldn't verify your password: password is incorrect."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	for _, bad := range []string{"session expired", "account login", "DRIFT-"} {
		if strings.Contains(got, bad) {
			t.Errorf("a refused credential says %q:\n%s", bad, got)
		}
	}
	if code := e.code(); code != "" {
		t.Errorf("code() = %q, want none: the code for a 401 sends the person to log in", code)
	}
}

// A platform that gave no reason still does not say the session expired.
func TestAPIError_ARefusedCredentialWithNoReasonSaysItWasNotAccepted(t *testing.T) {
	e := &APIError{Op: "verify your password", Status: http.StatusUnauthorized, CredentialRefused: true}

	if want := "Couldn't verify your password: it was not accepted."; e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}

// The control: the same 401 with nothing said about it is still a session that
// ran out, and keeps its code and its advice. The change must not turn every 401
// into a credential refusal.
func TestAPIError_A401ThatIsNotARefusalStillSaysTheSessionExpired(t *testing.T) {
	e := NewAPIError("verify your password", http.StatusUnauthorized, nil, []byte(`{"error":"password is incorrect"}`))

	got := e.Error()
	for _, want := range []string{"session expired", "drift account login", "DRIFT-1001"} {
		if !strings.Contains(got, want) {
			t.Errorf("a 401 that no one marked does not say %q:\n%s", want, got)
		}
	}
	if code := e.code(); code != "DRIFT-1001" {
		t.Errorf("code() = %q, want DRIFT-1001", code)
	}
}

// The control: a refused login is a refused credential by its op, as it was, and
// with no reason it still says the username or password was wrong. It carries the
// code for a refused login, which sends the person nowhere near the command they
// just ran.
func TestAPIError_ARefusedLoginNeedsNoField(t *testing.T) {
	t.Skip("a refused login prints DRIFT-1017 once the commit that wires it lands")
	e := &APIError{Op: "log in", Status: http.StatusUnauthorized}

	if want := "Couldn't log in: invalid username or password."; !strings.HasPrefix(e.Error(), want) {
		t.Errorf("Error() = %q, want it to start %q", e.Error(), want)
	}
	if code := e.code(); code != "DRIFT-1017" {
		t.Errorf("code() = %q, want DRIFT-1017", code)
	}
	if strings.Contains(e.Error(), "DRIFT-1001") {
		t.Errorf("a refused login names the expired-session code:\n%s", e.Error())
	}
}

// The mark is about a 401 and about nothing else: any other status renders as it
// does without it, so a caller that sets it on every failure of a request cannot
// hide a 5xx or a quota.
func TestAPIError_TheMarkChangesNothingButA401(t *testing.T) {
	withStatusFeed(t, http.StatusOK, feedOK)

	for _, status := range []int{
		http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusServiceUnavailable,
	} {
		plain := &APIError{Op: "verify your password", Status: status, Detail: "reason"}
		marked := &APIError{Op: "verify your password", Status: status, Detail: "reason", CredentialRefused: true}
		if plain.Error() != marked.Error() || plain.code() != marked.code() {
			t.Errorf("status %d renders differently when marked:\n%s\n%s", status, plain.Error(), marked.Error())
		}
	}
}
