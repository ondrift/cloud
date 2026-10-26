// info.go — what a slice is doing right now.
//
// The runtime has a scheduler: work is admitted against a per-function memory
// pool, the reservation is learned from the peak RSS of every invocation it has
// reaped, workers are kept and retired as demand moves, and an invocation that
// overruns is stopped rather than the pod being OOM-killed. All of it was
// readable only as Prometheus gauges on the slice's ops port, which is
// cluster-internal by design — so the person paying for it could not see it.
//
// This is the read that changes that. It answers one question: is my slice
// holding what I booked, and is anything being refused.
package slice

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// SchedulerPool is one admission pool: what it was sold and what it is holding.
//
// The SHARED pool arrives with an empty Function. That is not a missing value —
// it is the pool every function without its own booking draws from, and on a
// slice that declares no per-function memory it is the only pool there is.
type SchedulerPool struct {
	Function       string `json:"function"`
	BookedBytes    int64  `json:"booked_bytes"`
	CommittedBytes int64  `json:"committed_bytes"`
}

// SchedulerSlice is the slice-wide picture, which no single pool can give.
//
// BudgetBytes is the ceiling every concurrent invocation shares, derived from
// the pod's own cgroup rather than from anything declared. It is the gate that
// refuses an invocation whose own pool still has room, and without it such a
// refusal has no visible cause.
type SchedulerSlice struct {
	CommittedBytes        int64 `json:"committed_bytes"`
	BudgetBytes           int64 `json:"budget_bytes"`
	InvocationsInFlight   int64 `json:"invocations_in_flight"`
	WarmWorkersIdle       int64 `json:"warm_workers_idle"`
	EvictedTotal          int64 `json:"evicted_total"`
	EvictedReclaimedBytes int64 `json:"evicted_reclaimed_bytes"`
}

// Scheduler is the whole /ops/atomic/scheduler document.
type Scheduler struct {
	Slice SchedulerSlice  `json:"slice"`
	Pools []SchedulerPool `json:"pools"`
}

// FetchScheduler reads what the active slice's scheduler is doing. Data-only so
// the portal TUI can share it.
func FetchScheduler() (*Scheduler, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/scheduler", nil)
	if err != nil {
		return nil, common.TransportError("read the scheduler", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read the scheduler")
	if err != nil {
		return nil, err
	}

	var s Scheduler
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("Couldn't read the scheduler: the API response didn't look right (%w)", err)
	}
	return &s, nil
}

// Envelope is the part of the slice's quota document `drift slice info` reads:
// what the slice holds and what each part of it is allowed.
//
// The platform emits it as the DRIFT_QUOTA_CONFIG the slice enforces
// (tier.QuotaConfigMap), and the slice parses it by field name, so these tags
// are that contract's names. Only what a person asks about is decoded here.
type Envelope struct {
	Slots               map[string]int `json:"atomic_function_slots"`
	Buckets             map[string]int `json:"blob_bucket_quotas"`
	Collections         map[string]int `json:"no_sql_collection_quotas"`
	Databases           map[string]int `json:"sql_database_quotas"`
	Queues              map[string]int `json:"queue_quotas"`
	QueueDepth          int            `json:"queue_max_depth"`
	RealtimeConnections int            `json:"realtime_max_connections"`
	AtomicStorageBytes  int64          `json:"atomic_max_storage_bytes"`
	LogRetentionHours   int            `json:"log_retention_hours"`
}

// FetchEnvelope reads the active slice's quota envelope.
func FetchEnvelope() (*Envelope, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/quota", nil)
	if err != nil {
		return nil, common.TransportError("read the slice's limits", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read the slice's limits")
	if err != nil {
		return nil, err
	}
	var e Envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("Couldn't read the slice's limits: the API response didn't look right (%w)", err)
	}
	return &e, nil
}

// recordedPrice is what the active slice costs, as the stored figure renders
// it, or "" when none was recorded. It is not PriceLabel's fallback: that names
// the tier, and a line headed "price" must not.
func recordedPrice(name string) string {
	slices, err := FetchSlices()
	if err != nil {
		return ""
	}
	for _, s := range slices {
		if s.Name != name {
			continue
		}
		if s.Tier == "hacker" {
			return "free"
		}
		if s.MonthlyCostCents > 0 {
			return PriceLabel(s.Tier, s.MonthlyCostCents)
		}
	}
	return ""
}

// printShapeSection is the part of `drift slice info` that answers what the
// slice holds and costs: its price, and one line for each kind of thing it
// declares. A kind the slice holds none of prints nothing.
//
// A zero figure is never printed as a limit. Every limit in Drift is read as
// unbounded or as the platform's default at zero, so the lines below say a
// number only where there is one.
func printShapeSection(w io.Writer, e *Envelope, price string) {
	fmt.Fprintf(w, "\n  shape\n")
	if price == "" {
		fmt.Fprintf(w, "    %-14s not recorded\n", "price")
	} else {
		fmt.Fprintf(w, "    %-14s %s\n", "price", price)
	}

	if len(e.Slots) > 0 {
		total := 0
		for _, b := range e.Slots {
			total += b
		}
		fmt.Fprintf(w, "    %-14s %d booked, %s\n", "slots", len(e.Slots), bytesLabel(int64(total)))
	}
	sized := func(label string, set map[string]int) {
		if len(set) == 0 {
			return
		}
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s %s", n, bytesLabel(int64(set[n]))))
		}
		fmt.Fprintf(w, "    %-14s %s\n", label, strings.Join(parts, ", "))
	}
	sized("databases", e.Databases)
	sized("collections", e.Collections)
	sized("buckets", e.Buckets)

	if len(e.Queues) > 0 {
		names := make([]string, 0, len(e.Queues))
		for n := range e.Queues {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			if d := e.Queues[n]; d > 0 {
				parts = append(parts, fmt.Sprintf("%s %d messages", n, d))
			} else {
				parts = append(parts, fmt.Sprintf("%s the slice's depth, %d messages", n, e.QueueDepth))
			}
		}
		fmt.Fprintf(w, "    %-14s %s\n", "queues", strings.Join(parts, ", "))
	}
	if e.RealtimeConnections > 0 {
		fmt.Fprintf(w, "    %-14s %d connections\n", "realtime", e.RealtimeConnections)
	}
	if e.AtomicStorageBytes > 0 {
		fmt.Fprintf(w, "    %-14s %s for code and dependencies\n", "storage", bytesLabel(e.AtomicStorageBytes))
	}
	if e.LogRetentionHours > 0 {
		fmt.Fprintf(w, "    %-14s %dh\n", "log retention", e.LogRetentionHours)
	}
}

// bytesLabel renders a byte count in MiB, and renders the platform's
// "<= 0 means unbounded" convention as the word rather than as a number.
//
// This is the whole reason it is a function. Every limit in Drift is unlimited
// at zero, so printing "0 MiB" would state the opposite of what it means: a
// slice with no readable cgroup ceiling has NO gate, and reporting that as a
// zero-byte budget reads as a slice that can run nothing.
func bytesLabel(b int64) string {
	if b <= 0 {
		return "unbounded"
	}
	return fmt.Sprintf("%d MiB", b/(1024*1024))
}

// poolLabel names a pool for display. The shared pool has no function name and
// is labelled as what it is, rather than printed as a blank line.
func poolLabel(function string) string {
	if function == "" {
		return "(shared)"
	}
	return function
}

// heldLabel renders committed against booked, with the percentage only where
// there is a bound to be a percentage OF.
func heldLabel(committed, booked int64) string {
	if booked <= 0 {
		return fmt.Sprintf("%s held", bytesLabel(committed))
	}
	return fmt.Sprintf("%s of %s held (%d%%)",
		bytesLabel(committed), bytesLabel(booked), committed*100/booked)
}

func getInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show what the active slice is doing right now",
		Long: "What the slice's scheduler is currently holding and refusing: per function and\n" +
			"worker the memory booked and the memory in use, plus the slice-wide ceiling every\n" +
			"invocation shares, how many are running, how many workers are parked, and\n" +
			"what has been stopped for overrunning its booking. Then each worker: whether it\n" +
			"is running, for how long, and how often it has been restarted.\n\n" +
			"Read this when something is slow. `drift file benchmark` answers the other\n" +
			"question: what a function or worker has cost and what it should therefore book.",
		Example: "  drift slice info",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := FetchScheduler()
			if err != nil {
				return err
			}

			active := common.GetActiveSlice()
			if active != "" {
				fmt.Printf("%s\n\n", active)
			}

			fmt.Printf("  compute        %s\n", heldLabel(s.Slice.CommittedBytes, s.Slice.BudgetBytes))
			fmt.Printf("  in flight      %d invocation(s)\n", s.Slice.InvocationsInFlight)
			fmt.Printf("  warm workers   %d parked\n", s.Slice.WarmWorkersIdle)
			if s.Slice.EvictedTotal > 0 {
				// Said only when it has happened, and said plainly when it has.
				// A steady eviction count is a product signal — the bookings are
				// too small — not only an incident one.
				fmt.Printf("  stopped        %d invocation(s) for overrunning their booking, %s reclaimed\n",
					s.Slice.EvictedTotal, bytesLabel(s.Slice.EvictedReclaimedBytes))
			}

			if len(s.Pools) == 0 {
				// Not an error and not an empty slice: a slice with no
				// registered functions or workers has no pools, which is a different
				// fact from a slice whose pools could not be read.
				fmt.Printf("\n  no functions or workers deployed\n")
			} else {
				fmt.Printf("\n  pools\n")
				for _, p := range s.Pools {
					fmt.Printf("    %-28s %s\n", poolLabel(p.Function), heldLabel(p.CommittedBytes, p.BookedBytes))
				}
			}

			// The workers, after the pools they hold: a worker's pool shows what
			// it is holding, and this shows whether it is running at all.
			workers, ferr := atomic_cmd.FetchWorkers()
			printWorkersSection(cmd.OutOrStdout(), workers, ferr)

			// What it holds and costs, last: the compute above is what is
			// happening now, and this is what was bought.
			if env, eerr := FetchEnvelope(); eerr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\n  shape          couldn't be read: %v\n", eerr)
			} else {
				printShapeSection(cmd.OutOrStdout(), env, recordedPrice(active))
			}
			return nil
		},
	}
}

// printWorkersSection is the workers part of `drift slice info`: one line per
// worker, or one line saying they could not be read. A slice with no workers
// prints nothing here, as it has nothing to say about them.
func printWorkersSection(w io.Writer, workers []atomic_cmd.WorkerStatus, err error) {
	if err != nil {
		fmt.Fprintf(w, "\n  workers         couldn't be read: %v\n", err)
		return
	}
	if len(workers) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  workers\n")
	for _, f := range workers {
		fmt.Fprintf(w, "    %-28s %s\n", f.Name, atomic_cmd.WorkerSummary(f))
	}
}
