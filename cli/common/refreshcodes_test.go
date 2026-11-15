package common

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A request whose access token aged out is answered 401, and the refresh that
// follows can fail three ways. Each is a different failure with a different next
// step, so each prints its own code, through the same TransportError a command
// wraps what DoRequest returns in. These drive the real request path against a
// stub API, as the revoked-session tests do.

// notWiredYet skips a test until the commit that wires DoRequest and
// TransportError to the new codes lands; DRIFT-1018 is registered by the commit
// before it.
func notWiredYet(t *testing.T) {
	t.Helper()
	t.Skip("DRIFT-1018 is registered by the next commit and printed by the one after it")
}

// assertCodedOnce checks that the failure prints code's line exactly once, that
// CodeOf reads the same code, and that the code is registered.
func assertCodedOnce(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a failure carrying %s, got none", code)
	}
	if n := strings.Count(err.Error(), "\n"+codeLineOf(code)); n != 1 {
		t.Errorf("the failure prints %s's line %d times, want once:\n%s", code, n, err)
	}
	if got := CodeOf(err); got != code {
		t.Errorf("CodeOf = %q, want %q:\n%s", got, code, err)
	}
	if _, ok := LookupErrorCode(code); !ok {
		t.Errorf("%s is printed and not registered", code)
	}
}

// A platform that cannot answer the refresh is unavailable, and says to wait.
func TestARefreshTheUnavailablePlatformCannotAnswerPrintsDRIFT1012(t *testing.T) {
	notWiredYet(t)
	stubSession(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })

	err := TransportError("list slices", askForSomething(t))

	assertCodedOnce(t, err, "DRIFT-1012")
	if !strings.HasPrefix(err.Error(), "Couldn't list slices: "+MaintenanceMessage) {
		t.Errorf("the lead or the maintenance sentence changed:\n%s", err)
	}
	if strings.Contains(err.Error(), "account login") {
		t.Errorf("an unavailable platform advised logging in:\n%s", err)
	}
}

// A refresh the platform rejects is a session that ended, and says to log in.
func TestARejectedRefreshPrintsDRIFT1001(t *testing.T) {
	notWiredYet(t)
	stubSession(t, reject(`{"error":"invalid or expired refresh token"}`))

	err := TransportError("list slices", askForSomething(t))

	assertCodedOnce(t, err, "DRIFT-1001")
	for _, want := range []string{"Couldn't list slices: ", "drift account login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "reset-password") {
		t.Errorf("an ordinary expiry advised changing the password:\n%s", err)
	}
}

// A session the platform ended on purpose has a code of its own, because its next
// step differs from an ordinary expiry's, and the code's remedy keeps that step.
func TestARevokedRefreshPrintsItsOwnCodeAndKeepsItsSentence(t *testing.T) {
	notWiredYet(t)
	stubSession(t, reject(`{"error":"device mismatch; please log in again"}`))

	err := TransportError("list slices", askForSomething(t))

	assertCodedOnce(t, err, "DRIFT-1018")
	if !strings.Contains(err.Error(), SessionRevokedMessage) {
		t.Errorf("the revoked-session sentence is not whole:\n%s", err)
	}
	entry, _ := LookupErrorCode("DRIFT-1018")
	if !strings.Contains(entry.Remedy, "drift account reset-password") {
		t.Errorf("the remedy for a revoked session does not say to reset the password: %q", entry.Remedy)
	}
	if CodeOf(err) == "DRIFT-1001" {
		t.Errorf("a revoked session printed the code of an ordinary expiry:\n%s", err)
	}
}

// The three failures are three codes: none of them is another's.
func TestTheThreeRefreshFailuresPrintThreeCodes(t *testing.T) {
	notWiredYet(t)
	seen := map[string]string{}
	for name, stub := range map[string]func(w http.ResponseWriter){
		"unavailable": func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) },
		"rejected":    reject(`{"error":"invalid or expired refresh token"}`),
		"revoked":     reject(`{"error":"refresh token reuse detected; please log in again"}`),
	} {
		stubSession(t, stub)
		code := CodeOf(askForSomething(t))
		if code == "" {
			t.Errorf("the %s refresh prints no code", name)
		}
		if other, dup := seen[code]; dup && code != "" {
			t.Errorf("the %s and the %s refresh print the same code, %s", name, other, code)
		}
		seen[code] = name
	}
}

// TransportError strips the lead off a session-expired failure and keeps its code.
func TestTransportErrorKeepsTheCodeWhenItStripsTheLead(t *testing.T) {
	notWiredYet(t)
	err := TransportError("list slices",
		Failf("DRIFT-1001", "session expired — run 'drift account login' to re-authenticate"))

	assertCodedOnce(t, err, "DRIFT-1001")
	if !strings.HasPrefix(err.Error(), "Couldn't list slices: run 'drift account login'") {
		t.Errorf("the lead was not stripped as it was:\n%s", err)
	}
}

// A failure that carries a code and is not one TransportError reads keeps it, and
// the wrap adds its lead and nothing else.
func TestTransportErrorKeepsTheCodeOfAnyFailureItWraps(t *testing.T) {
	notWiredYet(t)
	inner := Failf("DRIFT-1012", "%s", MaintenanceMessage)
	err := TransportError("deploy", inner)

	assertCodedOnce(t, err, "DRIFT-1012")
	if !errors.Is(err, inner) {
		t.Errorf("the wrapped failure is no longer reachable through the error")
	}
}
