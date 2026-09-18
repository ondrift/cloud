package backbone

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// Every backbone command addresses ONE slice's secrets, cache, queues etc. via
// the X-Slice header GetActiveSlice supplies, and the API answers a request
// carrying no header by defaulting the slice name to the literal string
// "default" server-side (BBN-61) — which 404s for anyone who owns no slice
// with that exact name, and reports "slice not found: default" with no hint
// that the real cause was simply never having run `drift slice use`.
//
// `status` always checked locally first (RequireActiveSlice) and every other
// command in this package now does too. Each must refuse BEFORE any network
// call — proven here by failing the test outright if the stub is ever reached.
func TestBackboneCommandsRequireAnActiveSliceBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request reached the platform (%s %s) before the active-slice check ran", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	// Deliberately NOT common.SaveActiveSlice — that absence is the whole point.

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"status", statusCmd(), nil},
		{"secret set", secretSetCmd(), []string{"K=V"}},
		{"secret get", secretGetCmd(), []string{"K"}},
		{"secret list", secretListCmd(), nil},
		{"secret previous", secretPreviousCmd(), []string{"K"}},
		{"secret delete", secretDeleteCmd(), []string{"K"}},
		{"blob put", blobPutCmd(), []string{"b", "k", "f"}},
		{"blob get", blobGetCmd(), []string{"b", "k"}},
		{"blob list", blobListCmd(), []string{"b"}},
		{"blob delete", blobDeleteCmd(), []string{"b", "k"}},
		{"cache set", cacheSetCmd(), []string{"k", "v"}},
		{"cache get", cacheGetCmd(), []string{"k"}},
		{"cache del", cacheDelCmd(), []string{"k"}},
		{"cache exists", cacheExistsCmd(), []string{"k"}},
		{"lock acquire", lockAcquireCmd(), []string{"n", "o"}},
		{"lock release", lockReleaseCmd(), []string{"n", "o"}},
		{"lock renew", lockRenewCmd(), []string{"n", "o"}},
		{"nosql write", nosqlWriteCmd(), nil},
		{"nosql drop", nosqlDropCmd(), []string{"c"}},
		{"nosql list", nosqlListCmd(), nil},
		{"nosql read", nosqlReadCmd(), nil},
		{"queue push", queuePushCmd(), []string{"q", "{}"}},
		{"queue pop", queuePopCmd(), []string{"q"}},
		{"queue peek", queuePeekCmd(), []string{"q"}},
		{"queue drop", queueDropCmd(), []string{"q"}},
		{"queue len", queueLenCmd(), []string{"q"}},
		{"sql list", sqlListCmd(), nil},
		{"sql drop", sqlDropCmd(), []string{"d"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Called directly, bypassing cmd.Execute() — the same style
			// nosql_paging_test.go's runList uses — so Cobra's own Args/flag
			// validation never runs and cannot mask what RunE itself does.
			err := c.cmd.RunE(c.cmd, c.args)
			if err == nil {
				t.Fatal("expected the no-active-slice refusal, got nil")
			}
			if !strings.Contains(err.Error(), "no active slice") {
				t.Errorf("expected a no-active-slice error, got: %v", err)
			}
		})
	}
}
