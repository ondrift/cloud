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
	"net/http"

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
		Long: "What the slice's scheduler is currently holding and refusing: per function\n" +
			"the memory booked and the memory in use, plus the slice-wide ceiling every\n" +
			"invocation shares, how many are running, how many workers are parked, and\n" +
			"what has been stopped for overrunning its booking.\n\n" +
			"Read this when something is slow. `drift file benchmark` answers the other\n" +
			"question — what a function has cost and what it should therefore book.",
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
				// registered functions has no pools, which is a different fact
				// from a slice whose pools could not be read.
				fmt.Printf("\n  no functions deployed\n")
				return nil
			}

			fmt.Printf("\n  pools\n")
			for _, p := range s.Pools {
				fmt.Printf("    %-28s %s\n", poolLabel(p.Function), heldLabel(p.CommittedBytes, p.BookedBytes))
			}
			return nil
		},
	}
}
