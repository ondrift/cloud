package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// canonicalPlatform is a platform that serves the canonical slice and function
// routes and none of their older spellings.
//
// Every route here is answered the way the api answers it and every other path
// under /ops/ is answered the way the api answers a path it does not serve, a
// 404 saying "not found", and is recorded as a miss. A command that still sent a
// verb-in-path spelling would find nothing here, where a platform that keeps the
// aliases would answer it and hide the difference.
type canonicalPlatform struct {
	mu     sync.Mutex
	served []string
	missed []string
}

func (p *canonicalPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	named := r.URL.Query().Get("name") != ""

	status, body := http.StatusNotFound, `{"error":"not found"}`
	switch key {
	case "GET /ops/slice":
		// One path for both reads, as the platform's: the slice's record when a
		// name narrows it and the caller's slices when none does.
		if named {
			status, body = http.StatusOK, `{"name":"demo","tier":"hacker","config":{}}`
		} else {
			status, body = http.StatusOK, `[{"name":"demo","tier":"hacker","monthly_cost_cents":0}]`
		}
	case "POST /ops/slice":
		status, body = http.StatusCreated, `{"name":"demo"}`
	case "DELETE /ops/slice", "DELETE /ops/atomic":
		if named {
			status, body = http.StatusNoContent, ``
		} else {
			status, body = http.StatusBadRequest, `{"error":"name query param required"}`
		}
	case "GET /ops/slice/snapshot":
		status, body = http.StatusOK, `[]`
	case "GET /ops/slice/status":
		status, body = http.StatusOK, `{"ready":true,"components":[]}`
	case "GET /ops/atomic":
		status, body = http.StatusOK, `[{"function_name":"hello","method":"get","digest":"abc","route":"hello"}]`
	}

	p.mu.Lock()
	switch {
	case status != http.StatusNotFound:
		p.served = append(p.served, key)
	case strings.HasPrefix(r.URL.Path, "/ops/"):
		p.missed = append(p.missed, key)
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// take returns what was served and what missed since the last call, sorted and
// without repeats, and forgets both.
func (p *canonicalPlatform) take() (served, missed []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	uniq := func(in []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, s := range in {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return out
	}
	served, missed = uniq(p.served), uniq(p.missed)
	p.served, p.missed = nil, nil
	return served, missed
}

// useCanonicalPlatform points the CLI at one, with a session in a scratch HOME
// and "demo" as the active slice, so nothing reaches a real platform or a real
// ~/.drift.
func useCanonicalPlatform(t *testing.T) *canonicalPlatform {
	t.Helper()
	p := &canonicalPlatform{}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })

	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.SliceEnv, "demo")
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	return p
}

// silenced runs fn with standard output thrown away, for a command that prints
// the table a person reads.
func silenced(t *testing.T, fn func()) {
	t.Helper()
	sink, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	previous := os.Stdout
	os.Stdout = sink
	defer func() { os.Stdout = previous }()
	fn()
}

// THE COMMANDS WORK AGAINST A PLATFORM THAT HAS ONLY THE CANONICAL ROUTES.
//
// The source guard proves no file names an older spelling. This proves the other
// half: the method and path each command sends is one the platform serves, which
// a spelling that is merely absent from the source does not show. It stands in
// for a run against a cluster, which is the check that a real platform answers
// the same.
func TestCommandsReachAPlatformThatServesOnlyTheCanonicalRoutes(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T) error
		want []string
	}{
		{
			name: "drift slice list",
			run:  func(t *testing.T) error { return runDrift("slice", "list") },
			want: []string{"GET /ops/slice"},
		},
		{
			name: "drift slice delete",
			run:  func(t *testing.T) error { return runDrift("slice", "delete", "demo", "--yes") },
			want: []string{"DELETE /ops/slice", "GET /ops/slice"},
		},
		{
			name: "drift slice snapshot list",
			run:  func(t *testing.T) error { return runDrift("slice", "snapshot", "list") },
			want: []string{"GET /ops/slice/snapshot"},
		},
		{
			name: "drift atomic list",
			run:  func(t *testing.T) error { return runDrift("atomic", "list") },
			want: []string{"GET /ops/atomic"},
		},
		{
			name: "creating a slice",
			run: func(t *testing.T) error {
				return slice.CreateWithShape("demo", map[string]any{}, true)
			},
			want: []string{"GET /ops/slice/status", "POST /ops/slice"},
		},
		{
			name: "reading a slice's record",
			run: func(t *testing.T) error {
				live, err := project.FetchLiveSlice("demo")
				if err == nil && (live == nil || live.Name != "demo") {
					t.Errorf("FetchLiveSlice read %+v, want the record of demo", live)
				}
				return err
			},
			want: []string{"GET /ops/slice"},
		},
		{
			name: "reading the deployed functions",
			run: func(t *testing.T) error {
				fns, err := atomic_cmd.DeployedFunctions()
				if err == nil && len(fns) != 1 {
					t.Errorf("DeployedFunctions read %d functions, want 1", len(fns))
				}
				return err
			},
			want: []string{"GET /ops/atomic"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := useCanonicalPlatform(t)
			var err error
			silenced(t, func() { err = c.run(t) })
			if err != nil {
				t.Errorf("failed against a platform with the canonical routes: %v", err)
			}
			served, missed := p.take()
			if len(missed) != 0 {
				t.Errorf("sent paths that platform does not serve: %s", strings.Join(missed, ", "))
			}
			if strings.Join(served, ",") != strings.Join(c.want, ",") {
				t.Errorf("platform served %v, want exactly %v", served, c.want)
			}
		})
	}
}

// runDrift runs one command of the real command tree.
func runDrift(args ...string) error {
	root := newRootCmd("test")
	root.SetArgs(args)
	return root.Execute()
}
