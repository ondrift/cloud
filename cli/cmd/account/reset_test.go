package account

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The platform's /reset/verify answers 200 with mfa_required rather than a
// refusal — the same shape /login already uses for a confirmed second
// factor — because the reset code really was correct and this is the first
// leg of one round trip, not a failed one (flow enumeration finding
// IDN-26..28). resetNeedsMFA is what tells that reply apart from a completed
// reset.
func TestResetNeedsMFA(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"mfa required":         {`{"mfa_required":true}`, true},
		"completed reset":      {`{"status":"ok","message":"password reset successfully"}`, false},
		"mfa_required false":   {`{"mfa_required":false}`, false},
		"empty body":           {``, false},
		"unrelated JSON":       {`{"status":"ok"}`, false},
		"not even JSON at all": {`not json`, false},
	}
	for name, c := range cases {
		if got := resetNeedsMFA([]byte(c.body)); got != c.want {
			t.Errorf("%s: resetNeedsMFA(%q) = %v, want %v", name, c.body, got, c.want)
		}
	}
}

// postResetVerify must hand back the body on a 2xx (so the mfa_required
// check above has something to look at) and an error otherwise, exactly like
// every other CheckResponse-based call in this CLI.
func TestPostResetVerify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reset/verify" {
			t.Errorf("posted to %s, want /reset/verify", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mfa_required":true}`))
	}))
	defer srv.Close()
	oldBase := common.APIBaseURL
	common.APIBaseURL = srv.URL
	defer func() { common.APIBaseURL = oldBase }()

	body, err := postResetVerify(&http.Client{}, map[string]string{"username": "alice"})
	if err != nil {
		t.Fatalf("postResetVerify: %v", err)
	}
	if !resetNeedsMFA(body) {
		t.Errorf("body did not round-trip as mfa_required: %s", body)
	}
}

// The refusal shape (wrong code, wrong password, etc.) must come back as an
// error, not a body to inspect — same as every other verb in this CLI.
func TestPostResetVerify_ARefusalIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid or expired reset code"}`))
	}))
	defer srv.Close()
	oldBase := common.APIBaseURL
	common.APIBaseURL = srv.URL
	defer func() { common.APIBaseURL = oldBase }()

	if _, err := postResetVerify(&http.Client{}, map[string]string{"username": "alice"}); err == nil {
		t.Error("a 400 refusal was not reported as an error")
	}
}
