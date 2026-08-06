package slice

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A free slice is created by createHeadless and a deploy is expected to follow
// it immediately — that is the documented three-command opening of every demo
// and of the getting-started page. The record exists the moment create returns,
// but the pods behind it do not, so a deploy issued straight afterwards is
// refused by a component that is not listening yet. The CLI renders that as
// "Drift is temporarily unavailable", because every 5xx maps to the maintenance
// message, which sends the reader to look at the platform instead of at their
// own slice.
//
// The --from path already waits. This asserts the free path does too, so the
// two agree and the documented sequence works without a retry.
func TestCreateHeadlessWaitsForReadiness(t *testing.T) {
	var mu sync.Mutex
	var statusPolls int
	created := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/ops/slice/create":
			created = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ops/slice/status":
			// Not ready on the first look, ready on the second: a create that
			// polls once and believes the first answer would still race.
			statusPolls++
			ready := statusPolls > 1
			w.WriteHeader(http.StatusOK)
			if ready {
				_, _ = w.Write([]byte(`{"name":"demo","ready":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"name":"demo","ready":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })

	// A scratch HOME with a seeded session: DoJSONRequest attaches the token
	// and refuses to send anything without one, and SaveActiveSlice writes the
	// session file — neither should reach the operator's real ~/.drift.
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	// The session write failing is not what this test is about — createHeadless
	// only warns on it — so the error is deliberately not asserted here.
	_ = createHeadless("demo")

	mu.Lock()
	defer mu.Unlock()

	// Control: the create call itself still happens. If this ever fails the
	// test below is failing for the wrong reason.
	if !created {
		t.Fatal("create was never posted — the test is not exercising createHeadless")
	}
	if statusPolls < 2 {
		t.Errorf("create returned after %d readiness polls; it must wait for ready:true "+
			"so the deploy that follows does not race the slice coming up", statusPolls)
	}
}
