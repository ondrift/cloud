package slice

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every limit in Drift is unlimited at zero. Printing "0 MiB" for one would
// state the exact opposite of what it means: a slice with no readable cgroup
// ceiling has NO gate, and rendering that as a zero-byte budget reads as a
// slice that can run nothing at all.
func TestBytesLabel_ZeroIsUnboundedRatherThanNone(t *testing.T) {
	for _, b := range []int64{0, -1} {
		if got := bytesLabel(b); got != "unbounded" {
			t.Errorf("bytesLabel(%d) = %q, want \"unbounded\" — the platform's "+
				"convention is that <= 0 means no limit, so a number here inverts it", b, got)
		}
	}
	if got := bytesLabel(64 * 1024 * 1024); got != "64 MiB" {
		t.Errorf("bytesLabel(64 MiB) = %q", got)
	}
}

// The shared pool arrives with an empty function name. It is the pool every
// unbooked function draws from — and on a slice that declares no per-function
// memory it is the only pool there is — so printing it as a blank line hides
// the whole of that slice's compute.
func TestPoolLabel_TheSharedPoolIsNamed(t *testing.T) {
	if got := poolLabel(""); got != "(shared)" {
		t.Errorf("poolLabel(\"\") = %q — the shared pool must be named, not blank", got)
	}
	if got := poolLabel("post:expenses"); got != "post:expenses" {
		t.Errorf("poolLabel = %q", got)
	}
}

// A percentage needs something to be a percentage OF. An unbounded pool has no
// denominator, and dividing by it would panic; reporting 0% would claim a full
// pool is empty.
func TestHeldLabel_NoPercentageWithoutABound(t *testing.T) {
	got := heldLabel(31*1024*1024, 0)
	if strings.Contains(got, "%") {
		t.Errorf("heldLabel against an unbounded pool = %q, and a percentage of "+
			"unbounded is not a number", got)
	}
	if !strings.Contains(got, "31 MiB") {
		t.Errorf("heldLabel = %q, and what is held is still worth saying", got)
	}

	bounded := heldLabel(32*1024*1024, 64*1024*1024)
	if !strings.Contains(bounded, "50%") {
		t.Errorf("heldLabel(32, 64) = %q, want 50%%", bounded)
	}
}

// The document the slice answers with, decoded through the types the CLI
// actually uses. This is the half that breaks silently: every field here is a
// number, so a key the slice stops sending decodes as zero — which for a budget
// reads as "unbounded" and for an eviction count reads as "nothing was
// refused". Both are the opposite of not knowing.
func TestScheduler_DecodesTheSliceDocument(t *testing.T) {
	const body = `{
	  "slice": {
	    "committed_bytes": 32505856,
	    "budget_bytes": 838860800,
	    "invocations_in_flight": 2,
	    "warm_workers_idle": 3,
	    "evicted_total": 7,
	    "evicted_reclaimed_bytes": 4096
	  },
	  "pools": [
	    {"function": "post:expenses", "booked_bytes": 67108864, "committed_bytes": 33554432},
	    {"function": "", "booked_bytes": 33554432, "committed_bytes": 0}
	  ]
	}`

	var s Scheduler
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("the scheduler document must decode: %v", err)
	}

	if s.Slice.BudgetBytes != 838860800 || s.Slice.CommittedBytes != 32505856 {
		t.Errorf("slice block decoded wrong: %+v", s.Slice)
	}
	if s.Slice.InvocationsInFlight != 2 || s.Slice.WarmWorkersIdle != 3 {
		t.Errorf("live counters decoded wrong: %+v", s.Slice)
	}
	if s.Slice.EvictedTotal != 7 || s.Slice.EvictedReclaimedBytes != 4096 {
		t.Errorf("eviction figures decoded wrong: %+v", s.Slice)
	}

	if len(s.Pools) != 2 {
		t.Fatalf("got %d pools, want 2", len(s.Pools))
	}
	if s.Pools[0].Function != "post:expenses" || s.Pools[0].BookedBytes != 67108864 {
		t.Errorf("first pool decoded wrong: %+v", s.Pools[0])
	}
	if s.Pools[1].Function != "" {
		t.Errorf("the shared pool must keep its empty name, got %q", s.Pools[1].Function)
	}
}
