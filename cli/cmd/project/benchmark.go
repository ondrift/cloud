package project

// benchmark.go — `drift file benchmark`. What each function actually costs,
// and what it should therefore book.
//
// A booking has no default, which is correct — a reservation that appears by
// itself is one nobody chose, and it is both the function's pool and its price.
// But it leaves a real question: what number? Guessing high wastes money on
// memory nobody uses; guessing low means the pool fills and invocations are
// refused under load. Neither is discoverable by reading your own source.
//
// The booking is written in the Driftfile, beside what it books. This command
// reports what to write; `drift size` writes it, and `drift plan` and
// `drift apply` buy it, so the number that gets bought is the one that was
// measured and shown.
//
// The platform has been measuring the answer all along. Every invocation's child
// is reaped with wait4, which carries its peak RSS; the slice keeps the
// high-water mark per function and serves it at GET /sizing. This command asks,
// through `/ops/atomic/sizing`, which proxies it under the caller's own
// credentials — the slice's own copy is behind a service token the CLI does not
// hold.
//
// # Why the recommendation is not just the peak
//
// A benchmark measures what a function DID cost. A booking has to cover what it
// WILL. Without headroom the first invocation heavier than anything measured is
// refused, so the recommendation carries margin and lands on a size the platform
// accepts. The platform computes it: this command reports the figure rather than
// deriving a second opinion that could round differently and have a tenant write
// one number and be billed another.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// sizingRow is one function's answer.
type sizingRow struct {
	Function string
	// Measurements backing the peak. Zero means NOT MEASURED, which is a
	// different finding from "measured and cheap" and must not render as one.
	Measurements int64
	PeakBytes    int64
	BookedBytes  int64
	Recommended  int64
	Concurrency  int64
	// Note carries why a row has no number, so an absent measurement explains
	// itself instead of appearing as a small one.
	Note string
}

// sliceSizing is the GET /sizing shape, relayed by /ops/atomic/sizing.
type sliceSizing struct {
	BookingStepBytes int64 `json:"booking_step_bytes"`
	MinBookingBytes  int64 `json:"min_booking_bytes"`
	Functions        []struct {
		Function                 string `json:"function"`
		Measurements             int64  `json:"measurements"`
		PeakRSSBytes             int64  `json:"peak_rss_bytes"`
		BookedBytes              int64  `json:"booked_bytes"`
		RecommendedBookingBytes  int64  `json:"recommended_booking_bytes"`
		ConcurrencyAtCurrentBook int64  `json:"concurrency_at_current_booking"`
	} `json:"functions"`
}

func getBenchmarkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Measure what each pulse and field costs, and what it should book",
		Long: strings.TrimSpace(`
Report what each pulse and field has actually cost in memory, and the size it
should book.

The numbers come from the deployed slice's own measurements: it weighs every
pulse invocation and samples every running field, and keeps the high-water mark
per slot. A pulse nobody has called yet says so, rather than reporting a
confidently small number.

A booking is written in the Driftfile. 'drift size' writes these
recommendations into it, and 'drift plan' shows what they cost.`),
		Example: "  drift file benchmark",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, err := requireDriftfile()
			if err != nil {
				return err
			}
			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}
			rows, err := sizingRows(declaredSlots(m))
			if err != nil {
				return err
			}
			renderSizing(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	return cmd
}

// measuredRecommendations is the booking to set for each slot that has
// something to base one on. A slot nothing has invoked is left out: its
// "recommendation" is the floor rather than a measurement, and offering it
// would look like advice while being the absence of any.
func measuredRecommendations(rows []sizingRow) map[string]int64 {
	rec := map[string]int64{}
	for _, r := range rows {
		if r.Measurements > 0 && r.Recommended > 0 {
			rec[r.Function] = r.Recommended
		}
	}
	return rec
}

// sizingRows asks the deployed slice what its functions have cost, one row per
// slot it reports. declared holds a key for every slot the Driftfile names; it
// is what lets a row say "deployed, but not in this Driftfile". `drift file
// benchmark` and `drift size` both read the measurements here, so the two
// cannot disagree about a recommendation.
func sizingRows(declared map[string]string) ([]sizingRow, error) {
	resp, err := common.DoJSONRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/sizing", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() // #nosec G104
	body, err := common.CheckResponse(resp, "read what this slice's functions have cost")
	if err != nil {
		return nil, err
	}

	var s sliceSizing
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("read the slice's sizing report: %w", err)
	}

	rows := make([]sizingRow, 0, len(s.Functions))
	for _, f := range s.Functions {
		r := sizingRow{
			Function:     f.Function,
			Measurements: f.Measurements,
			PeakBytes:    f.PeakRSSBytes,
			BookedBytes:  f.BookedBytes,
			Recommended:  f.RecommendedBookingBytes,
			Concurrency:  f.ConcurrencyAtCurrentBook,
		}
		if _, ok := declared[f.Function]; !ok {
			r.Note = "deployed, but not in this Driftfile"
		}
		// Checked after, and deliberately overwrites the note above: a function
		// nobody has called is the more useful thing to say about it, and the
		// recommendation on that row is the floor rather than a measurement.
		if f.Measurements == 0 {
			r.Note = "never invoked — nothing measured"
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Function < rows[j].Function })
	return rows, nil
}

// declaredSlots holds a key for each declared Pulse and Field.
//
// Keyed by the booking key, which is the same `method:route` (or `field:<name>`)
// the slice books and reports under — the two agreeing is what lets a row be
// matched at all.
func declaredSlots(m *Manifest) map[string]string {
	out := map[string]string{}
	for _, s := range AtomicSpecs(m) {
		out[s.Name] = ""
	}
	return out
}

// renderSizing prints the table, and the command that writes it into the
// Driftfile.
func renderSizing(w io.Writer, rows []sizingRow) {
	fmt.Fprintf(w, "\n  measured by the slice, on real traffic\n\n")
	fmt.Fprintf(w, "  %-26s %8s %10s %9s %11s  %s\n",
		"function", "calls", "peak", "booked", "recommend", "")
	for _, r := range rows {
		fmt.Fprintf(w, "  %-26s %8s %10s %9s %11s  %s\n",
			r.Function,
			countOrDash(r.Measurements),
			megabytesOrDash(r.PeakBytes),
			megabytesOrDash(r.BookedBytes),
			megabytesOrDash(r.Recommended),
			r.Note)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  The recommendation carries headroom over the measured peak — a booking sized")
	fmt.Fprintln(w, "  exactly to what was observed is refused by the first heavier invocation.")
	fmt.Fprintln(w, "  `calls` is how many invocations back the peak: a figure from three calls of")
	fmt.Fprintln(w, "  one code path is not the claim one from thousands is.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  `drift size` writes these into the Driftfile; `drift plan` shows what they cost.")
	fmt.Fprintln(w)
}

func countOrDash(n int64) string {
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func megabytesOrDash(b int64) string {
	if b <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0fMB", float64(b)/(1024*1024))
}
