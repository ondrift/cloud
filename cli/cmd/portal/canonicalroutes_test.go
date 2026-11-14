package portal

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// THE DASHBOARD'S CALLS WORK AGAINST A PLATFORM THAT HAS ONLY THE CANONICAL ROUTES.
//
// The platform below answers each canonical route and answers every other path
// under /ops/ the way the api answers one it does not serve, a 404, recording it
// as a miss. A dashboard call that still sent an older spelling finds nothing
// here, where a platform that keeps the aliases would answer it and hide that.
func TestDashboardCallsReachAPlatformThatServesOnlyTheCanonicalRoutes(t *testing.T) {
	type reply struct {
		status int
		body   string
	}
	canonical := map[string]reply{
		"GET /ops/slice":          {http.StatusOK, `{"name":"demo","tier":"hacker","config":{}}`},
		"POST /ops/slice":         {http.StatusCreated, `{"name":"demo"}`},
		"DELETE /ops/slice":       {http.StatusNoContent, ``},
		"GET /ops/slice/snapshot": {http.StatusOK, `[{"id":"s1","name":"nightly","status":"ready"}]`},
		"GET /ops/atomic":         {http.StatusOK, `[{"function_name":"hello","method":"get"}]`},
		"DELETE /ops/atomic":      {http.StatusNoContent, ``},
	}

	var served, missed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		got, ok := canonical[key]
		if !ok {
			got = reply{http.StatusNotFound, `{"error":"not found"}`}
			if strings.HasPrefix(r.URL.Path, "/ops/") {
				missed = append(missed, key)
			}
		} else {
			served = append(served, key)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(got.status)
		_, _ = w.Write([]byte(got.body))
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.SliceEnv, "demo")
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	for _, c := range []struct {
		name string
		run  func() error
		want string
	}{
		{"list functions", func() error { _, err := fetchFunctions(); return err }, "GET /ops/atomic"},
		{"delete a function", func() error { return deleteFunction("hello") }, "DELETE /ops/atomic"},
		{"list snapshots", func() error { _, err := fetchSnapshots(); return err }, "GET /ops/slice/snapshot"},
		{"create a slice", func() error { return createSlice("demo", "hacker", nil, 1) }, "POST /ops/slice"},
		{"read a slice", func() error { _, err := fetchSliceDoc("demo"); return err }, "GET /ops/slice"},
		{"delete a slice", func() error { return deleteSliceByName("demo") }, "DELETE /ops/slice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			served, missed = nil, nil
			if err := c.run(); err != nil {
				t.Errorf("failed against a platform with the canonical routes: %v", err)
			}
			sort.Strings(missed)
			if len(missed) != 0 {
				t.Errorf("sent paths that platform does not serve: %s", strings.Join(missed, ", "))
			}
			if len(served) != 1 || served[0] != c.want {
				t.Errorf("platform served %v, want exactly [%s]", served, c.want)
			}
		})
	}
}
