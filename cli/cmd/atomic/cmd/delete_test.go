package atomic_cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// withAPI points the CLI at a stub API for one test and restores the real one.
func withAPI(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
}

// A delete the platform REFUSED must fail the command.
//
// Under `Run` this handler could only print and fall off the end, so every
// refusal exited 0. That is the worst shape for a destructive command: a CI gate
// reads it as "the function is gone", and a `drift atomic delete x && ...` chain
// carries on as though it were.
//
// The assertion is on the returned error rather than on the exit code, because
// the error is what main() turns into a non-zero exit — testing the wrapper
// would test cobra.
func TestDelete_ARefusedDeleteIsAnError(t *testing.T) {
	withAPI(t, http.StatusNotFound, `{"error":"slot atomic-1 holds no function"}`)

	cmd := Delete()
	cmd.SetArgs([]string{"atomic-1"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a 404 from the platform returned no error, so the command exits 0 and " +
			"a refused delete is indistinguishable from a completed one")
	}

	// The operator's own words have to survive to the caller: the edge relays the
	// body precisely so "which slot" is answerable without reading a log.
	if !strings.Contains(err.Error(), "atomic-1") {
		t.Errorf("the refusal must name what was wrong, got: %v", err)
	}
}

// The control: a delete the platform ACCEPTED must succeed. Without it the test
// above passes for a command that fails unconditionally.
func TestDelete_AnAcceptedDeleteSucceeds(t *testing.T) {
	withAPI(t, http.StatusNoContent, "")

	cmd := Delete()
	cmd.SetArgs([]string{"greet"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("a 204 must succeed, got: %v", err)
	}
}
