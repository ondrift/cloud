package atomic_cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// `--method` defaults to "post" with no cross-check against what the
// function is actually deployed as. Before this fix, setting a key for a
// `method: get` function with --method omitted silently gated POST — a
// method the function never answers on — while the real GET route stayed
// permanently 503 with nothing pointing at the mismatch (ATM-47).

type capturedRequest struct {
	Method string
	Path   string
	Body   string
}

type recordedRequests struct {
	mu   sync.Mutex
	reqs []capturedRequest
}

func (r *recordedRequests) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, capturedRequest{Method: req.Method, Path: req.URL.Path, Body: string(body)})
	r.mu.Unlock()
}

func (r *recordedRequests) last() capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		return capturedRequest{}
	}
	return r.reqs[len(r.reqs)-1]
}

// withMethodResolutionAPI seeds a session (same shape as withAPI in
// delete_test.go) and stubs GET /ops/atomic/list with slotsJSON — what
// resolveAuthMethod's own lookup reads — while recording every OTHER
// request the command sends, answered 204.
func withMethodResolutionAPI(t *testing.T, slotsJSON string) *recordedRequests {
	t.Helper()
	rec := &recordedRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/ops/atomic/list" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(slotsJSON))
			return
		}
		rec.record(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
	return rec
}

// The core claim: a function deployed as GET, with --method omitted (the
// flag's "post" default), gets its key set for GET — not silently gated
// under a method it does not answer on.
func TestAuthSet_UsesTheFunctionsActualMethodWhenNotGivenOne(t *testing.T) {
	rec := withMethodResolutionAPI(t, `[
		{"_id":"atomic-1","function_name":"greet","method":"get","language":"python"}
	]`)

	cmd := authSet()
	cmd.SetArgs([]string{"greet", "secret-key"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("auth set failed: %v", err)
	}

	last := rec.last()
	if last.Method != http.MethodPost || last.Path != "/ops/atomic/auth" {
		t.Fatalf("the auth-set call landed at %s %s, want POST /ops/atomic/auth", last.Method, last.Path)
	}
	if !strings.Contains(last.Body, `"method":"GET"`) {
		t.Errorf("the key must be set for GET, the function's actual deployed method, got body %q", last.Body)
	}
}

// An explicit --method is never overridden, even when it disagrees with what
// is deployed — that is the caller's own choice to make, not this command's.
func TestAuthSet_ExplicitMethodIsNeverOverridden(t *testing.T) {
	rec := withMethodResolutionAPI(t, `[
		{"_id":"atomic-1","function_name":"greet","method":"get","language":"python"}
	]`)

	cmd := authSet()
	cmd.SetArgs([]string{"greet", "secret-key", "--method", "put"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("auth set failed: %v", err)
	}

	last := rec.last()
	if !strings.Contains(last.Body, `"method":"PUT"`) {
		t.Errorf("an explicit --method must be respected as-is, got body %q", last.Body)
	}
}

// Two slots share the function name under different methods — get:greet and
// post:greet are different functions (selectAtomicSlot's own rule) — so
// there is no single right default to guess, and the command must refuse
// rather than silently pick one.
func TestAuthSet_RefusesWhenTheNameIsAmbiguous(t *testing.T) {
	withMethodResolutionAPI(t, `[
		{"_id":"atomic-1","function_name":"greet","method":"get","language":"python"},
		{"_id":"atomic-2","function_name":"greet","method":"post","language":"python"}
	]`)

	cmd := authSet()
	cmd.SetArgs([]string{"greet", "secret-key"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("an ambiguous function name must be refused, not silently defaulted")
	}
	if !strings.Contains(err.Error(), "--method") {
		t.Errorf("the refusal must point at --method as the fix, got: %v", err)
	}
}

// authRevoke shares resolveAuthMethod with authSet; one test is enough to
// pin that the wiring reaches it too, since the resolution logic itself is
// already covered above.
func TestAuthRevoke_UsesTheFunctionsActualMethodWhenNotGivenOne(t *testing.T) {
	rec := withMethodResolutionAPI(t, `[
		{"_id":"atomic-1","function_name":"greet","method":"get","language":"python"}
	]`)

	cmd := authRevoke()
	cmd.SetArgs([]string{"greet"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("auth revoke failed: %v", err)
	}

	last := rec.last()
	if last.Method != http.MethodDelete || last.Path != "/ops/atomic/auth" {
		t.Fatalf("the auth-revoke call landed at %s %s, want DELETE /ops/atomic/auth", last.Method, last.Path)
	}
	if !strings.Contains(last.Body, `"method":"GET"`) {
		t.Errorf("the revoke must target GET, the function's actual deployed method, got body %q", last.Body)
	}
}
