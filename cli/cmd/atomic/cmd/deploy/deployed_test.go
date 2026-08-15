package atomic_cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// listReturning points the CLI at a stub serving one /ops/atomic/list body.
func listReturning(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/atomic/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })

	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
}

// The two consumers of one fetch want different sets, and the difference is the
// whole reason for the split. A function deployed by an older CLI has no
// recorded digest — it must still redeploy, so the digest map omits it, and it
// is still serving and still holding a slot, so the deployed set must not.
func TestDeployedFunctions_KeepsARecordTheDigestMapOmits(t *testing.T) {
	listReturning(t, `[
		{"function_name":"auth/challenge","method":"post","digest":"abc"},
		{"function_name":"legacy","method":"get","digest":""},
		{"function_name":"","method":"","digest":""}
	]`)

	fns, err := DeployedFunctions()
	if err != nil {
		t.Fatalf("DeployedFunctions: %v", err)
	}
	if len(fns) != 2 {
		t.Fatalf("both deployed functions must be reported, and the empty slot must not: got %+v", fns)
	}

	byKey := map[string]DeployedFunction{}
	for _, f := range fns {
		byKey[f.Key] = f
	}
	if _, ok := byKey["post:auth/challenge"]; !ok {
		t.Errorf("a deployed function is keyed by method and route, got %+v", fns)
	}
	if legacy, ok := byKey["get:legacy"]; !ok || legacy.Digest != "" {
		t.Errorf("a record carrying no digest is still deployed and must be reported, got %+v", fns)
	}

	digests, err := DeployedDigests()
	if err != nil {
		t.Fatalf("DeployedDigests: %v", err)
	}
	if _, present := digests["get:legacy"]; present {
		t.Error("a record with no digest must stay out of the digest map, or it would be " +
			"skipped as unchanged against a digest nobody recorded")
	}
	if digests["post:auth/challenge"] != "abc" {
		t.Errorf("skip-unchanged must be unaffected by the split, got %v", digests)
	}
}

// A queue handler's stored method is "queue", so both consumers key it by its
// bare name — the identity the Driftfile writes.
func TestDeployedFunctions_AQueueHandlerKeysOnItsName(t *testing.T) {
	listReturning(t, `[{"function_name":"orders","method":"queue","digest":"abc"}]`)

	fns, err := DeployedFunctions()
	if err != nil {
		t.Fatalf("DeployedFunctions: %v", err)
	}
	if len(fns) != 1 || fns[0].Key != "orders" {
		t.Errorf("a queue handler keys on its queue name, got %+v", fns)
	}
}
