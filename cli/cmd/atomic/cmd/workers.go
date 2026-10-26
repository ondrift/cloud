package atomic_cmd

// workers.go — `drift atomic workers`: what the slice's Worker supervisor is doing.
//
// A Worker is the one long-running process the slice keeps per declaration. It
// has no invocations to count and no requests to log against, so the questions
// worth asking about one are different from a function's: is it running, how long
// has it been up, how often has it been restarted, and why did it last stop.
// The supervisor answers all four at GET /workers, which the api relays as
// GET /ops/atomic/workers under the caller's own credentials.
//
// A Worker that crashes five times in ten minutes is PAUSED and not restarted
// again until its next deploy, a slice restart, or `drift atomic workers
// restart <name>`, which is the one write this file makes.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	deploy "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// WorkerStatus is one Worker as the slice's supervisor reports it.
type WorkerStatus struct {
	Name string `json:"name"`
	// State is running, backoff, paused or stopped.
	State     string      `json:"state"`
	PID       int         `json:"pid"`
	StartedAt workerTime  `json:"started_at"`
	Restarts  int         `json:"restarts"`
	LastExit  *WorkerExit `json:"last_exit"`
}

// WorkerExit is how a Worker's process last ended: an exit status or the signal
// that ended it, and a bounded excerpt of what it wrote to stderr on the way.
type WorkerExit struct {
	At      workerTime   `json:"at"`
	Status  *int         `json:"status"`
	Signal  workerSignal `json:"signal"`
	Excerpt string       `json:"excerpt"`
}

// workerTime reads a timestamp in either shape a supervisor may write: an
// RFC 3339 string, or a number of Unix seconds (milliseconds when the number is
// too large to be seconds). The zero value means the report carried none.
type workerTime struct{ time.Time }

func (t *workerTime) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "" || raw == "null" {
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("a worker timestamp %q is not RFC 3339: %w", s, err)
		}
		t.Time = parsed
		return nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("a worker timestamp %s is neither a string nor a number", raw)
	}
	if n <= 0 {
		return nil
	}
	// Seconds since 1970 stay below 1e11 until the year 5138; a number above it
	// is milliseconds.
	if n > 1e11 {
		t.Time = time.UnixMilli(int64(n))
		return nil
	}
	sec := int64(n)
	t.Time = time.Unix(sec, int64((n-float64(sec))*1e9))
	return nil
}

// workerSignal reads the signal that ended a process, as a name ("SIGKILL") or
// a number (9). Empty means the process exited rather than being signalled.
type workerSignal string

func (s *workerSignal) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	switch {
	case raw == "" || raw == "null":
		return nil
	case strings.HasPrefix(raw, `"`):
		var name string
		if err := json.Unmarshal(b, &name); err != nil {
			return err
		}
		*s = workerSignal(name)
	default:
		*s = workerSignal("signal " + raw)
	}
	return nil
}

// FetchWorkers reads the active slice's Workers. Data-only so `drift slice info`
// can share it.
func FetchWorkers() ([]WorkerStatus, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/workers", nil)
	if err != nil {
		return nil, common.TransportError("read the slice's workers", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read the slice's workers")
	if err != nil {
		return nil, err
	}

	// The supervisor answers a bare list. A relay that wraps it in an object is
	// read too, rather than decoded into nothing and reported as no workers.
	var workers []WorkerStatus
	if err := json.Unmarshal(body, &workers); err != nil {
		var wrapped struct {
			Workers []WorkerStatus `json:"workers"`
		}
		if werr := json.Unmarshal(body, &wrapped); werr != nil {
			return nil, fmt.Errorf("Couldn't read the slice's workers: the API response didn't look right (%w)", err)
		}
		workers = wrapped.Workers
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
	return workers, nil
}

// RestartWorker asks the supervisor to start a Worker again now: a paused one
// leaves the pause, one in backoff stops waiting, and a running one is stopped
// gracefully and started again.
func RestartWorker(name string) error {
	target := common.APIBaseURL + "/ops/atomic/workers/restart?worker=" + url.QueryEscape(name)
	resp, err := common.DoRequest(http.MethodPost, target, nil)
	if err != nil {
		return common.TransportError("restart the worker", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "restart the worker")
	return err
}

// workersNow is the clock uptimes and ages are measured against, so a test can
// fix it.
var workersNow = time.Now

// Workers is `drift atomic workers` and its `restart` subcommand.
func Workers() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workers",
		Short: "List the slice's workers: state, uptime, restarts and last exit",
		Long: "A worker is a long-running process: the slice keeps exactly one process for each " +
			"declared worker, restarts it when it exits, and pauses it after five exits in ten " +
			"minutes.\n\nThis lists every worker the slice runs, with its state (running, backoff, " +
			"paused or stopped), how long it has been up, how often it has been restarted, and how " +
			"it last stopped. Its output is in `drift atomic logs <worker>`.",
		Example: "  drift atomic workers\n  drift atomic workers restart sequencer\n  drift atomic logs sequencer",
		GroupID: "operations",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workers, err := FetchWorkers()
			if err != nil {
				return err
			}
			RenderWorkers(cmd.OutOrStdout(), workers)
			return nil
		},
	}
	cmd.AddCommand(workersRestartCmd())
	return cmd
}

func workersRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <worker>",
		Short: "Start a worker again now, including one paused after a crash loop",
		Long: "Asks the slice to start the worker again: a paused worker leaves the pause, one " +
			"waiting out a backoff starts now, and a running one is stopped gracefully (SIGTERM, " +
			"then SIGKILL after ten seconds) and started again. Nothing is rebuilt or redeployed.",
		Example:      "  drift atomic workers restart sequencer",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The booking key every other surface prints is accepted as well
			// as the bare name the supervisor keys a worker by.
			name := strings.TrimPrefix(args[0], deploy.WorkerMethod+":")
			if err := RestartWorker(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Worker %q is restarting. `drift atomic workers` shows when it is up.\n", name)
			return nil
		},
	}
}

// RenderWorkers prints the table, then the stderr excerpt of every worker that
// last stopped with one, then what to do about a paused worker.
func RenderWorkers(w io.Writer, workers []WorkerStatus) {
	if len(workers) == 0 {
		fmt.Fprintln(w, "No workers on this slice.")
		return
	}
	now := workersNow()

	rows := [][]string{{"NAME", "STATE", "UPTIME", "RESTARTS", "LAST EXIT"}}
	for _, f := range workers {
		rows = append(rows, []string{f.Name, stateLabel(f.State), uptimeLabel(f, now),
			strconv.Itoa(f.Restarts), lastExitLabel(f.LastExit, now)})
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, cell := range r {
			widths[i] = max(widths[i], len(cell))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, cell := range r {
			if i == len(r)-1 {
				b.WriteString(cell)
				break
			}
			fmt.Fprintf(&b, "%-*s  ", widths[i], cell)
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}

	for _, f := range workers {
		if f.LastExit == nil || strings.TrimSpace(f.LastExit.Excerpt) == "" {
			continue
		}
		fmt.Fprintf(w, "\n%s last wrote, before it stopped:\n", f.Name)
		for _, line := range strings.Split(strings.TrimRight(f.LastExit.Excerpt, "\n"), "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}

	for _, f := range workers {
		if f.State == "paused" {
			fmt.Fprintf(w, "\n%s is paused after exiting repeatedly. Fix what its last exit names, "+
				"redeploy it, or start it again with `drift atomic workers restart %s`.\n", f.Name, f.Name)
		}
	}
}

// WorkerSummary is one worker on one line, for a view with room for no table:
// its state, its uptime when running, its restarts, and its last exit when it
// is not running.
func WorkerSummary(f WorkerStatus) string {
	now := workersNow()
	parts := []string{stateLabel(f.State)}
	if up := uptimeLabel(f, now); up != "-" {
		parts = append(parts, "up "+up)
	}
	parts = append(parts, fmt.Sprintf("%d %s", f.Restarts, pluralWord(f.Restarts, "restart", "restarts")))
	if f.State != "running" && f.LastExit != nil {
		parts = append(parts, "last "+lastExitLabel(f.LastExit, now))
	}
	return strings.Join(parts, ", ")
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func stateLabel(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

// uptimeLabel is how long a running worker's current process has been up. A
// worker that is not running has no uptime, which is a dash rather than a zero.
func uptimeLabel(f WorkerStatus, now time.Time) string {
	if f.State != "running" || f.StartedAt.IsZero() {
		return "-"
	}
	return durationLabel(now.Sub(f.StartedAt.Time))
}

// lastExitLabel says how the previous process ended and how long ago.
func lastExitLabel(e *WorkerExit, now time.Time) string {
	if e == nil {
		return "-"
	}
	var how string
	switch {
	case e.Signal != "":
		how = string(e.Signal)
	case e.Status != nil:
		how = fmt.Sprintf("exit %d", *e.Status)
	default:
		how = "exited"
	}
	if e.At.IsZero() {
		return how
	}
	return fmt.Sprintf("%s, %s ago", how, durationLabel(now.Sub(e.At.Time)))
}

// durationLabel renders a duration at the precision a person reads an uptime
// in: seconds under a minute, minutes and seconds under an hour, then hours and
// minutes, then days and hours.
func durationLabel(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%02dh", days, int(d.Hours())%24)
}
