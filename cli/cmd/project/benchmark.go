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
// The booking lives on the slice, which the configurator owns. This command
// reports what to set and, with --apply, opens the form with the figures already
// in it — so the number that gets bought is the one that was measured.
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
	"os"
	"path/filepath"
	"sort"
	"strings"

	slice "github.com/ondrift/cloud/cli/cmd/slice"
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
	var apply, write bool
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Measure what each Atomic function costs, and what it should book",
		Long: strings.TrimSpace(`
Report what each Atomic function has actually cost in memory, and the size it
should book.

The numbers come from the deployed slice's own measurements of real traffic: it
weighs every invocation and keeps the high-water mark per function. A function
nobody has called yet says so, rather than reporting a confidently small number.

A booking is part of a slice's shape, which the configurator owns. --apply opens
it with the measured recommendations already filled in, so the price and the
restart are shown before anything is bought.`),
		Example: "  drift file benchmark\n  drift file benchmark --apply",
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return err
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}
			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}
			rows, err := benchmarkFromSlice(m)
			if err != nil {
				return err
			}
			renderSizing(cmd.OutOrStdout(), m.Name(), rows)

			// A thin alias onto --apply, not a second path. It used to edit the
			// Driftfile's `memory` key, which the platform stopped reading — so
			// the file carried the recommendation and the slice kept whatever the
			// configurator sold it. There is no behaviour left to keep working,
			// only a name people have in their fingers.
			if write {
				common.DeprecateFlag(cmd, "write", common.Deprecation{
					Old: "drift file benchmark --write",
					New: "drift file benchmark --apply",
					Because: "A booking is a slice setting rather than a Driftfile one. " +
						"This used to edit the manifest's retired `memory` key, which the platform ignores; " +
						"--apply opens the configurator with the measured figures filled in.",
				})()
				apply = true
			}
			if apply {
				return applyBookings(cmd.OutOrStdout(), m.Name(), rows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false,
		"open the configurator with each function's booking set to the recommendation")
	cmd.Flags().BoolVar(&write, "write", false,
		"Deprecated: use --apply")
	return cmd
}

// applyBookings hands the measured recommendations to the configurator.
//
// It does NOT write them anywhere itself. A booking is priced, billed, and
// travels in the pod spec, so applying one buys a resize and replaces the pod —
// and the CLI has no path to a slice's shape at all any more, by design. The
// configurator is the one writer, and opening it pre-filled means the price and
// the restart are disclosed to the person paying before they agree to either.
//
// A function the slice has never run is left alone. Its "recommendation" is the
// floor rather than a measurement, and pre-filling that would look like advice
// while being the absence of any.
func applyBookings(w io.Writer, sliceName string, rows []sizingRow) error {
	rec := map[string]int64{}
	for _, r := range rows {
		if r.Measurements > 0 && r.Recommended > 0 {
			rec[r.Function] = r.Recommended
		}
	}
	if len(rec) == 0 {
		fmt.Fprintln(w, "  Nothing to apply: no function has been invoked yet, so there is no")
		fmt.Fprintln(w, "  measurement to size from. Send some traffic and run this again.")
		return nil
	}

	const op = "apply the measured bookings"
	cfg, err := FetchSliceConfigRaw(sliceName, op)
	if err != nil {
		return err
	}

	changed := overlayBookings(cfg, rec)
	if changed == 0 {
		fmt.Fprintln(w, "  Every booking already matches its recommendation.")
		return nil
	}

	fmt.Fprintf(w, "  Opening the resize form with %d booking(s) set to the recommendation.\n", changed)
	fmt.Fprintln(w, "  Nothing is bought until you apply it.")

	// The overlaid config is handed to the form as the shape to open on, so the
	// recommendation is in the rows before anything is agreed to. A measurement
	// is evidence for a booking, not authority to buy one.
	shaped, ok := cfg.(map[string]any)
	if !ok {
		return fmt.Errorf("the slice's live config could not be read as a shape to open")
	}
	return slice.ResizeWithConfig(sliceName, shaped, 1)
}

// overlayBookings sets each recommended function's booking on the live config,
// in place, and reports how many it changed.
//
// The config is the decoded JSON rather than a typed struct: the CLI forwards it
// whole and never learns its shape, so a field it has no name for survives the
// round trip. Separated from the fetch and the handoff so the substitution is
// testable without either.
//
// Matching is on the booking key — `method:route` — which is what the sizing
// endpoint reports and what the runtime looks a pool up by. The two agreeing is
// what makes a row matchable at all, and they have not always agreed.
func overlayBookings(cfg any, rec map[string]int64) int {
	root, ok := cfg.(map[string]any)
	if !ok {
		return 0
	}
	atomicNode, ok := root["atomic"].(map[string]any)
	if !ok {
		return 0
	}
	functions, ok := atomicNode["functions"].([]any)
	if !ok {
		return 0
	}

	changed := 0
	for _, entry := range functions {
		fn, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		want, found := rec[name]
		if !found {
			continue
		}
		// JSON numbers decode as float64, so an unchanged booking has to be
		// compared as one rather than by ==-ing two different types.
		if current, ok := fn["memory_bytes"].(float64); ok && int64(current) == want {
			continue
		}
		fn["memory_bytes"] = want
		changed++
	}
	return changed
}

// benchmarkFromSlice asks the deployed slice what its functions have cost.
func benchmarkFromSlice(m *Manifest) ([]sizingRow, error) {
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

	declared := declaredMemory(m)
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

// declaredMemory maps each declared function to the memory it books.
//
// Keyed by the name in the Driftfile, which is the same `method:route` the slice
// books and reports under — the two agreeing is what lets a row be matched at
// all. They have not always agreed.
func declaredMemory(m *Manifest) map[string]string {
	out := map[string]string{}
	for _, fn := range m.Slice().Entries("name", "atomic", "functions") {
		if name := fn.Str("name"); name != "" {
			out[name] = fn.Str("memory")
		}
	}
	return out
}

// renderSizing prints the table.
//
// sliceName is carried purely so the closing line can name the page these
// figures are set on. A measurement is only useful where it can be applied, and
// the reader is one click from applying it rather than one search.
func renderSizing(w io.Writer, sliceName string, rows []sizingRow) {
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
	fmt.Fprintf(w, "  Set these with `drift slice resize %s`, or apply them with --apply.\n", sliceName)
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
