package project

// benchmark.go — `drift file benchmark`. What each function actually costs,
// and what it should therefore book.
//
// `atomic.functions[].memory` is mandatory and has no default, which is correct
// — a reservation that appears by itself is one nobody chose, and it is both the
// function's pool and its price. But it leaves a real question the first time
// someone writes a Driftfile: what number? Guessing high wastes money on memory
// nobody uses; guessing low means the pool fills and invocations are refused
// under load. Neither is discoverable by reading your own source.
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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
	var write bool
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Measure what each Atomic function costs, and what it should book",
		Long: strings.TrimSpace(`
Report what each Atomic function has actually cost in memory, and the size it
should declare in its Driftfile.

The numbers come from the deployed slice's own measurements of real traffic: it
weighs every invocation and keeps the high-water mark per function. A function
nobody has called yet says so, rather than reporting a confidently small number.

With --write, each function's memory in the Driftfile is set to the
recommendation. Comments, ordering and everything else in the file are left
alone.`),
		Example: "  drift file benchmark\n  drift file benchmark --write",
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
			renderSizing(cmd.OutOrStdout(), rows)
			if write {
				return writeBookings(cmd.OutOrStdout(), manifestPath, rows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false,
		"set each function's `memory` in the Driftfile to the recommendation")
	return cmd
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
	fmt.Fprintln(w, "  Apply these with --write.")
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

// writeBookings sets each function's `memory` to the recommendation, in place.
//
// The file is edited as a yaml.Node tree rather than re-marshalled from a struct,
// because a round trip through a struct silently drops every comment in the file
// — and a Driftfile is a document people write in, not a serialisation format.
// Only the scalar values change; key order, spacing and comments survive.
//
// A function the slice has never run is skipped. Its "recommendation" is the
// floor, and writing that would look like advice while being the absence of any.
func writeBookings(w io.Writer, path string, rows []sizingRow) error {
	rec := map[string]int64{}
	for _, r := range rows {
		if r.Measurements > 0 && r.Recommended > 0 {
			rec[r.Function] = r.Recommended
		}
	}
	if len(rec) == 0 {
		fmt.Fprintln(w, "  Nothing to write: no function has been invoked yet, so there is no")
		fmt.Fprintln(w, "  measurement to size from. Send some traffic and run this again.")
		return nil
	}

	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}

	changed := writeBookingsInto(&doc, rec)
	if changed == 0 {
		fmt.Fprintln(w, "  Every booking already matches its recommendation.")
		return nil
	}

	// Two-space indent, matching `drift file fmt`. yaml.Marshal defaults to four
	// and would reindent every line of the file, turning "set two numbers" into a
	// whole-file diff — the same reason the formatter sets it explicitly.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("Driftfile: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("Driftfile: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil { // #nosec G306
		return err
	}
	fmt.Fprintf(w, "  Updated %d booking(s) in %s.\n\n", changed, filepath.Base(path))
	return nil
}

// writeBookingsInto rewrites the `memory` scalar of every function named in rec,
// and reports how many it changed. Separated from the file handling so the tree
// walk is testable without a filesystem.
func writeBookingsInto(doc *yaml.Node, rec map[string]int64) int {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return 0
	}
	atomic := childOf(doc.Content[0], "atomic")
	if atomic == nil {
		return 0
	}
	functions := childOf(atomic, "functions")
	if functions == nil || functions.Kind != yaml.SequenceNode {
		return 0
	}

	changed := 0
	for _, entry := range functions.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		nameNode := childOf(entry, "name")
		if nameNode == nil {
			continue
		}
		want, ok := rec[nameNode.Value]
		if !ok {
			continue
		}
		value := fmt.Sprintf("%dMB", want/(1024*1024))
		memNode := childOf(entry, "memory")
		if memNode == nil {
			// A function declared without memory cannot have deployed, so this is
			// a manifest edited since. Add the key rather than skipping it: the
			// point of --write is that the file is complete afterwards.
			entry.Content = append(entry.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "memory"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
			changed++
			continue
		}
		if memNode.Value == value {
			continue
		}
		memNode.Value = value
		memNode.Tag = "!!str"
		changed++
	}
	return changed
}

// childOf returns the value node for a key in a mapping node, or nil.
func childOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
