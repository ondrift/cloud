package account

// login_mfa_test.go — the second leg, from the client's side.
//
// The platform decides whether a code is right. What is decided HERE is the
// sequence, and the sequence has two ways to be wrong that no server test can
// see: prompting a pipe for a code, and letting the challenge handle outlive the
// login.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// authStub answers /login with a challenge and /login/mfa with tokens, and
// records what it was sent.
type authStub struct {
	srv *httptest.Server

	loginBodies []string
	mfaBodies   []string

	mfaStatus int
	mfaBody   string
}

func newAuthStub(t *testing.T) *authStub {
	t.Helper()
	a := &authStub{
		mfaStatus: http.StatusOK,
		mfaBody:   `{"access_token":"access-1","refresh_token":"refresh-1","token_type":"Bearer"}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.loginBodies = append(a.loginBodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mfa_required":true,"mfa_token":"handle-1","expires_in":300}`)
	})
	mux.HandleFunc("/login/mfa", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mfaBodies = append(a.mfaBodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.mfaStatus)
		_, _ = io.WriteString(w, a.mfaBody)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)

	// Point the CLI at the stub, and its session at a directory this test owns.
	original := common.APIBaseURL
	common.APIBaseURL = a.srv.URL
	t.Cleanup(func() { common.APIBaseURL = original })
	t.Setenv("HOME", t.TempDir())

	return a
}

// The whole point of the two legs: a password alone stores no session, and the
// code is what turns the challenge into one.
func TestLogin_ACodeCompletesTheChallengeAndStoresTheSession(t *testing.T) {
	a := newAuthStub(t)

	if err := DoLoginWithFactor("alice", "hunter2", fixedFactor("123456", false)); err != nil {
		t.Fatalf("login: %v", err)
	}

	if len(a.mfaBodies) != 1 {
		t.Fatalf("the second leg was called %d times, want 1", len(a.mfaBodies))
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(a.mfaBodies[0]), &sent); err != nil {
		t.Fatalf("the second leg's body must be JSON: %v", err)
	}
	if sent["mfa_token"] != "handle-1" {
		t.Errorf("the handle from the first leg must be sent back, got %q", sent["mfa_token"])
	}
	if sent["code"] != "123456" {
		t.Errorf("code = %q, want 123456", sent["code"])
	}
	if _, ok := sent["recovery_code"]; ok {
		t.Error("a TOTP code must not also be sent as a recovery code — it would spend one")
	}

	token, _, err := common.GetTokenFromSession()
	if err != nil || token != "access-1" {
		t.Errorf("the session was not saved: token=%q err=%v", token, err)
	}
}

// A recovery code travels in its OWN field. Sent as `code` it would be compared
// against TOTP and rejected, and the user would conclude their codes are junk at
// the exact moment they need them.
func TestLogin_ARecoveryCodeTravelsInItsOwnField(t *testing.T) {
	a := newAuthStub(t)

	if err := DoLoginWithFactor("alice", "hunter2", fixedFactor("ABCDE-FGHJK", true)); err != nil {
		t.Fatalf("login: %v", err)
	}

	var sent map[string]string
	_ = json.Unmarshal([]byte(a.mfaBodies[0]), &sent)
	if sent["recovery_code"] != "ABCDE-FGHJK" {
		t.Errorf("recovery_code = %q, want the code supplied", sent["recovery_code"])
	}
	if sent["code"] != "" {
		t.Errorf("a recovery code must not be sent as a TOTP code, got code=%q", sent["code"])
	}
}

// THE non-interactive failure. With nothing to answer the challenge, the login
// must FAIL and say what flag to pass — not hang on a prompt, and not exit 0
// having stored nothing.
func TestLogin_WithNoWayToAnswerTheChallengeItFailsAndNamesTheFlag(t *testing.T) {
	a := newAuthStub(t)

	err := DoLoginWithFactor("alice", "hunter2", nil)
	if err == nil {
		t.Fatal("a login that could not answer the challenge must fail")
	}
	if !strings.Contains(err.Error(), "--mfa-code") {
		t.Errorf("the error must name the flag that fixes it, got %q", err)
	}
	if len(a.mfaBodies) != 0 {
		t.Error("the second leg was called with no code")
	}
	if _, _, serr := common.GetTokenFromSession(); serr == nil {
		t.Error("a session was stored for a login that never completed")
	}
}

// An empty answer is not a login. It must not be sent as a code, and it must
// leave no session behind.
func TestLogin_AnEmptyCodeIsNotSent(t *testing.T) {
	a := newAuthStub(t)

	err := DoLoginWithFactor("alice", "hunter2", fixedFactor("", false))
	if err == nil {
		t.Fatal("an empty code must fail the login")
	}
	if len(a.mfaBodies) != 0 {
		t.Error("an empty code was sent to the platform")
	}
}

// A refused code fails the command. This is the regression that already cost
// this CLI once on the password path: a failed login that exits 0 makes every
// `drift account login && drift atomic deploy` in the world deploy anyway.
func TestLogin_ARefusedCodeIsAnError(t *testing.T) {
	a := newAuthStub(t)
	a.mfaStatus = http.StatusUnauthorized
	a.mfaBody = `{"error":"that code is not valid"}`

	if err := DoLoginWithFactor("alice", "hunter2", fixedFactor("000000", false)); err == nil {
		t.Fatal("a refused code must return an error, or a script carries on as if logged in")
	}
	if _, _, serr := common.GetTokenFromSession(); serr == nil {
		t.Error("a session was stored for a refused code")
	}
}

// The first leg still carries the device id. The refresh token minted at the end
// of the SECOND leg is bound to it, so losing it here would silently unbind
// every session an enrolled user creates.
func TestLogin_TheDeviceIdStillTravelsOnTheFirstLeg(t *testing.T) {
	a := newAuthStub(t)

	_ = DoLoginWithFactor("alice", "hunter2", fixedFactor("123456", false))

	var sent map[string]string
	_ = json.Unmarshal([]byte(a.loginBodies[0]), &sent)
	if sent["device_id"] == "" {
		t.Error("device_id was dropped from the first leg — every enrolled user's refresh token would be unbound")
	}
}

// An account with NO second factor is unaffected: one call, tokens, done.
func TestLogin_AnAccountWithoutAFactorStillLogsInWithOneCall(t *testing.T) {
	var mfaCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"access-1","refresh_token":"refresh-1"}`)
	})
	mux.HandleFunc("/login/mfa", func(w http.ResponseWriter, r *http.Request) { mfaCalls++ })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	original := common.APIBaseURL
	common.APIBaseURL = srv.URL
	defer func() { common.APIBaseURL = original }()
	t.Setenv("HOME", t.TempDir())

	if err := DoLoginWithFactor("alice", "hunter2", nil); err != nil {
		t.Fatalf("an account with no factor must log in exactly as before: %v", err)
	}
	if mfaCalls != 0 {
		t.Error("the second leg was called for an account with no second factor")
	}
	if token, _, err := common.GetTokenFromSession(); err != nil || token != "access-1" {
		t.Errorf("session not stored: %q %v", token, err)
	}
}

// The challenge handle must not be written anywhere. For its five minutes it
// completes a login without the password, so a copy on disk beside the session
// is the control undone.
func TestLogin_TheChallengeHandleIsNeverWrittenToDisk(t *testing.T) {
	newAuthStub(t)

	_ = DoLoginWithFactor("alice", "hunter2", fixedFactor("123456", false))

	// Whatever the session file ended up holding, the handle is not in it.
	token, refresh, err := common.GetTokenFromSession()
	if err != nil {
		return // nothing was stored at all, which also satisfies this
	}
	for _, stored := range []string{token, refresh} {
		if strings.Contains(stored, "handle-1") {
			t.Fatal("the challenge handle was stored — it completes a login without the password")
		}
	}
}
