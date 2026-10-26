package atomic_cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// workersBody is what the supervisor reports for three Workers: one running, one
// paused after a crash loop, one waiting out a backoff after a SIGKILL. The
// timestamps come in both shapes a report may carry them in.
const workersBody = `[
  {"name":"sequencer","state":"running","pid":4211,"started_at":"2026-10-06T11:57:48Z","restarts":2,
   "last_exit":{"at":"2026-10-06T11:57:47Z","status":0,"excerpt":""}},
  {"name":"mailer","state":"paused","pid":0,"started_at":null,"restarts":5,
   "last_exit":{"at":1791287700,"status":1,"excerpt":"dial tcp: connection refused\nmailer: giving up"}},
  {"name":"indexer","state":"backoff","pid":0,"started_at":null,"restarts":1,
   "last_exit":{"at":"2026-10-06T11:59:30Z","signal":"SIGKILL","excerpt":""}}
]`

// fixClock pins the clock uptimes and ages are measured against.
func fixClock(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 10, 6, 12, 1, 0, 0, time.UTC)
	prev := workersNow
	workersNow = func() time.Time { return now }
	t.Cleanup(func() { workersNow = prev })
}

// `drift atomic workers` reads GET /ops/atomic/workers and prints, per Worker, its
// state, how long it has been up, its restarts and how it last stopped; then
// what a crashed Worker last wrote, and what to do about a paused one.
func TestWorkers_ListsStateUptimeRestartsAndLastExit(t *testing.T) {
	fixClock(t)
	withAPI(t, http.StatusOK, workersBody)

	cmd := Workers()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(new(strings.Builder))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("drift atomic workers: %v", err)
	}
	got := out.String()
	t.Logf("drift atomic workers:\n%s", got)

	for _, want := range []string{
		"NAME       STATE    UPTIME  RESTARTS  LAST EXIT",
		"indexer    backoff  -       1         SIGKILL, 1m30s ago",
		"mailer     paused   -       5         exit 1, 6m00s ago",
		"sequencer  running  3m12s   2         exit 0, 3m13s ago",
		"mailer last wrote, before it stopped:\n    dial tcp: connection refused\n    mailer: giving up",
		"drift atomic workers restart mailer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing must contain %q, got:\n%s", want, got)
		}
	}
	// Sorted by name, whatever order the slice answered in.
	if strings.Index(got, "indexer") > strings.Index(got, "sequencer") {
		t.Errorf("the workers are not in name order:\n%s", got)
	}
}

// A slice running no Worker says so, rather than printing an empty table.
func TestWorkers_NoWorkersSaysSo(t *testing.T) {
	withAPI(t, http.StatusOK, `[]`)
	cmd := Workers()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "No workers on this slice." {
		t.Errorf("got %q", out.String())
	}
}

// A refusal from the platform is the command's error, never an exit 0 with
// nothing printed.
func TestWorkers_ARefusalIsAnError(t *testing.T) {
	withAPI(t, http.StatusForbidden, `{"error":"slice read scope required"}`)
	cmd := Workers()
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "slice read scope required") {
		t.Fatalf("a 403 must fail the command with the platform's reason, got %v", err)
	}
}

// `drift atomic workers restart <name>` POSTs /ops/atomic/workers/restart with
// the Worker's bare name, accepting the booking key every other surface prints.
func TestWorkersRestart_PostsTheWorkerByName(t *testing.T) {
	var method, path, worker string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, worker = r.Method, r.URL.Path, r.URL.Query().Get("worker")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatal(err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })

	for _, arg := range []string{"mailer", "worker:mailer"} {
		cmd := Workers()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"restart", arg})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("restart %s: %v", arg, err)
		}
		if method != http.MethodPost || path != "/ops/atomic/workers/restart" || worker != "mailer" {
			t.Errorf("restart %s sent %s %s?worker=%s, want POST /ops/atomic/workers/restart?worker=mailer",
				arg, method, path, worker)
		}
		if !strings.Contains(out.String(), `Worker "mailer" is restarting`) {
			t.Errorf("restart %s printed %q", arg, out.String())
		}
	}
}

// The one-line summary `drift slice info` prints per Worker.
func TestWorkerSummary(t *testing.T) {
	fixClock(t)
	status := 1
	for want, f := range map[string]WorkerStatus{
		"running, up 3m12s, 2 restarts": {Name: "sequencer", State: "running", Restarts: 2,
			StartedAt: workerTime{time.Date(2026, 10, 6, 11, 57, 48, 0, time.UTC)}},
		"paused, 1 restart, last exit 1, 1m00s ago": {Name: "mailer", State: "paused", Restarts: 1,
			LastExit: &WorkerExit{At: workerTime{time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}, Status: &status}},
	} {
		if got := WorkerSummary(f); got != want {
			t.Errorf("WorkerSummary(%s) = %q, want %q", f.Name, got, want)
		}
	}
}
