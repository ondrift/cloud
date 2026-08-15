package project

import (
	"fmt"
	"testing"
)

// colls builds a declared-collection map of n entries. The COUNT of collections
// is len(the map) rather than a field, so a test that used to set a number now
// has to declare that many — which is the model these tests are guarding.
func colls(n int) map[string]int {
	m := make(map[string]int, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("c%d", i)] = 5 * 1024 * 1024
	}
	return m
}

// TestManifestToSliceConfig_Counts verifies that resource lists are
// counted correctly and land in the right Max… fields.
func TestManifestToSliceConfig_Counts(t *testing.T) {
	m := manifestFrom(Node{
		"name": "test",
		"atomic": map[string]any{"functions": []any{
			map[string]any{"name": "a", "memory": "32MB"},
			map[string]any{"name": "b", "memory": "64MB"},
			map[string]any{"name": "c", "memory": "32MB", "cron": "0 * * * *"}, // scheduled
		}},
		"backbone": map[string]any{
			"nosql": []any{
				map[string]any{"name": "x", "size": "10MB"},
				map[string]any{"name": "y", "size": "20MB"},
			},
			"queues": []any{
				map[string]any{"name": "q1"},
				map[string]any{"name": "q2"},
				map[string]any{"name": "q3"},
			},
			"secrets":              map[string]any{"A": "1", "B": "2"},
			"realtime_connections": 200,
		},
	})
	cfg, err := ManifestToSliceConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Atomic.MaxNumberOfFunctions != 3 {
		t.Errorf("functions: got %d, want 3", cfg.Atomic.MaxNumberOfFunctions)
	}
	if cfg.Atomic.MaxNumberOfScheduledJobs != 1 {
		t.Errorf("scheduled: got %d, want 1", cfg.Atomic.MaxNumberOfScheduledJobs)
	}
	// The count IS the number declared, read off the map rather than a field.
	if len(cfg.Backbone.NoSQL.Collections) != 2 {
		t.Errorf("collections: got %d, want 2", len(cfg.Backbone.NoSQL.Collections))
	}
	if cfg.Backbone.Secrets.MaxCount != 2 {
		t.Errorf("secrets: got %d, want 2", cfg.Backbone.Secrets.MaxCount)
	}
	if cfg.Backbone.Realtime.MaxConcurrentConnections != 200 {
		t.Errorf("realtime: got %d, want 200", cfg.Backbone.Realtime.MaxConcurrentConnections)
	}
}

// TestManifestToSliceConfig_EnvelopeKnobs verifies that envelope
// strings parse into the right integer values.
func TestManifestToSliceConfig_EnvelopeKnobs(t *testing.T) {
	m := manifestFrom(Node{
		"name": "test",
		"atomic": map[string]any{
			"function_memory":  "256MB",
			"function_timeout": "60s",
			"rate_limit":       "1000/min",
			"atomic_size":      "250MB",
		},
		"backbone": map[string]any{
			"nosql":           []any{map[string]any{"name": "events", "size": "500MB"}},
			"blob_max_size":   "5MB",
			"queue_max_depth": 1000,
		},
		"canvas":           map[string]any{"canvas_size": "100MB"},
		"log_retention":    "7d",
		"backup_retention": "14d",
	})
	cfg, err := ManifestToSliceConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Atomic.MaxFunctionMemoryBytes; got != 256*1024*1024 {
		t.Errorf("function_memory: got %d, want %d", got, 256*1024*1024)
	}
	if cfg.Atomic.MaxFunctionRuntimeInSeconds != 60 {
		t.Errorf("function_timeout: got %d, want 60", cfg.Atomic.MaxFunctionRuntimeInSeconds)
	}
	if cfg.Atomic.MaxNumberOfRequestsPerMinute != 1000 {
		t.Errorf("rate_limit: got %d, want 1000", cfg.Atomic.MaxNumberOfRequestsPerMinute)
	}
	// The runner volume's cap — deployed code and its vendored dependencies.
	// Without this the knob parses nowhere and the slice silently keeps the
	// platform default, which reads as the declaration having been honoured.
	if got := cfg.Atomic.MaxStorageBytes; got != 250*1024*1024 {
		t.Errorf("atomic_size: got %d, want %d", got, 250*1024*1024)
	}
	if cfg.Backbone.NoSQL.Collections["events"] != 500*1024*1024 {
		t.Errorf("nosql[events].size: got %d, want %d", cfg.Backbone.NoSQL.Collections["events"], 500*1024*1024)
	}
	if cfg.Backbone.Blobs.MaxSizeInBytesEach != 5*1024*1024 {
		t.Errorf("blob_max_size: got %d, want %d", cfg.Backbone.Blobs.MaxSizeInBytesEach, 5*1024*1024)
	}
	if cfg.Canvas.TotalMaxSizeInBytes != 100*1024*1024 {
		t.Errorf("canvas_size: got %d, want %d", cfg.Canvas.TotalMaxSizeInBytes, 100*1024*1024)
	}
	if cfg.Atomic.MaxNumberOfHoursForLogRetention != 24*7 {
		t.Errorf("log_retention: got %d, want %d", cfg.Atomic.MaxNumberOfHoursForLogRetention, 24*7)
	}
	if cfg.Backbone.BackupRetentionDays != 14 {
		t.Errorf("backup_retention: got %d, want 14", cfg.Backbone.BackupRetentionDays)
	}
}

// TestManifestToSliceConfig_RatePerS verifies the /s and /h rate
// shortcuts normalise to per-minute correctly.
func TestManifestToSliceConfig_RatePerS(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"100/s", 6000},
		{"1000/min", 1000},
		{"60000/h", 1000},
	}
	for _, c := range cases {
		m := manifestFrom(Node{"name": "t", "atomic": map[string]any{"rate_limit": c.in}})
		cfg, err := ManifestToSliceConfig(m)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got := cfg.Atomic.MaxNumberOfRequestsPerMinute; got != c.want {
			t.Errorf("%s: got %d rpm, want %d", c.in, got, c.want)
		}
	}
}

// TestDiff_CreatePath verifies a missing live slice produces a Create
// verdict with all positive Wanted fields as grows.
func TestDiff_CreatePath(t *testing.T) {
	manifest := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5, MaxFunctionMemoryBytes: 64 * 1024 * 1024},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{Collections: colls(2)}},
	}
	d := Diff("hello", manifest, nil, "", 0, 0)
	if d.Verdict != VerdictCreate {
		t.Errorf("verdict: got %s, want create", d.Verdict)
	}
	if !d.IsNewSlice {
		t.Error("IsNewSlice should be true on Create")
	}
	// functions, memory, the collection COUNT, and one row per declared
	// collection — the count is len(the map), so declaring two of them is two
	// per-collection storage rows as well.
	if len(d.Grows) != 5 {
		t.Errorf("grows: got %d, want 5 (got %+v)", len(d.Grows), d.Grows)
	}
}

// Shrinking the runner volume has to abort like every other shrink. A byte cap
// absent from the delta list is invisible to that check, so a manifest asking
// for less disk than the slice holds would resize straight through it.
func TestDiff_AtomicStorageShrinkAborts(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxStorageBytes: 1024 * 1024 * 1024}}
	manifest := SliceConfig{Atomic: AtomicLimits{MaxStorageBytes: 100 * 1024 * 1024}}
	d := Diff("hello", manifest, &live, "", 3000, 1500)
	if d.Verdict != VerdictAbort {
		t.Errorf("verdict: got %s, want abort", d.Verdict)
	}
	if len(d.Shrinks) != 1 || d.Shrinks[0].Path != "atomic.storage" {
		t.Errorf("shrinks: got %+v, want one entry for atomic.storage", d.Shrinks)
	}
	// The control: growing it is a Grow, not an Abort.
	g := Diff("hello", SliceConfig{Atomic: AtomicLimits{MaxStorageBytes: 2 * 1024 * 1024 * 1024}}, &live, "", 1500, 3000)
	if g.Verdict != VerdictGrow {
		t.Errorf("grow verdict: got %s, want grow", g.Verdict)
	}
}

// TestDiff_MatchPath verifies identical manifests produce a Match
// verdict with no grows or shrinks.
func TestDiff_MatchPath(t *testing.T) {
	cfg := SliceConfig{
		Atomic: AtomicLimits{MaxNumberOfFunctions: 5},
	}
	d := Diff("hello", cfg, &cfg, "", 1500, 1500)
	if d.Verdict != VerdictMatch {
		t.Errorf("verdict: got %s, want match", d.Verdict)
	}
	if len(d.Grows) != 0 || len(d.Shrinks) != 0 {
		t.Errorf("expected no deltas, got grows=%d shrinks=%d", len(d.Grows), len(d.Shrinks))
	}
}

// TestDiff_GrowPath verifies a Wanted > Live in some field produces
// a Grow verdict and the field shows up in d.Grows.
func TestDiff_GrowPath(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 3}}
	manifest := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 5}}
	d := Diff("hello", manifest, &live, "", 1500, 3000)
	if d.Verdict != VerdictGrow {
		t.Errorf("verdict: got %s, want grow", d.Verdict)
	}
	if len(d.Grows) != 1 || d.Grows[0].Path != "atomic.functions" {
		t.Errorf("grows: got %+v, want one entry for atomic.functions", d.Grows)
	}
	if d.Grows[0].Delta() != 2 {
		t.Errorf("delta: got %d, want 2", d.Grows[0].Delta())
	}
}

// TestDiff_AbortPath verifies a Wanted < Live in some field produces
// an Abort verdict — the load-bearing safety property.
func TestDiff_AbortPath(t *testing.T) {
	live := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{Collections: colls(6)}},
	}
	manifest := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{Collections: colls(4)}},
	}
	d := Diff("hello", manifest, &live, "", 3000, 1500)
	if d.Verdict != VerdictAbort {
		t.Errorf("verdict: got %s, want abort", d.Verdict)
	}
	// Going from six declared collections to four shrinks the count AND drops
	// two named collections, each of which is its own row. All three are real
	// reductions and the abort has to see every one of them.
	if len(d.Shrinks) != 3 || d.Shrinks[0].Path != "backbone.nosql_collections" {
		t.Errorf("shrinks: got %+v, want the count plus the two dropped collections", d.Shrinks)
	}
}

// TestRenderDiff_FreeWording verifies the binary "This slice is free."
// vs "€N/month" wording rule.
func TestRenderDiff_FreeWording(t *testing.T) {
	d := Diff("hello", SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 1}}, nil, "", 0, 0)
	out := RenderDiff(d)
	if !contains(out, "This slice is free.") {
		t.Errorf("free slice: got %q, want 'This slice is free.'", out)
	}

	d2 := Diff("hello", SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 50}}, nil, "", 0, 1500)
	out2 := RenderDiff(d2)
	if !contains(out2, "Cost: €15/month") {
		t.Errorf("paid slice: want 'Cost: €15/month' in output, got: %s", out2)
	}
}

// TestRenderDiff_FreeToPaidCrossing verifies the explicit
// "free → €N/mo" wording when a grow crosses the Hacker boundary.
func TestRenderDiff_FreeToPaidCrossing(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 3}}
	manifest := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 8}}
	d := Diff("hello", manifest, &live, "", 0, 1500)
	out := RenderDiff(d)
	if !contains(out, "free → €15/mo") {
		t.Errorf("expected 'free → €15/mo' in output, got: %s", out)
	}
}

// TestRenderDiff_AbortMessage verifies the abort UX names the offending
// fields and gives the operator the escape hatch (`drift slice resize`).
func TestRenderDiff_AbortMessage(t *testing.T) {
	live := SliceConfig{Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{Collections: colls(6)}}}
	manifest := SliceConfig{Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{Collections: colls(4)}}}
	d := Diff("hello", manifest, &live, "", 1500, 1000)
	out := RenderDiff(d)

	for _, want := range []string{
		"Refusing to deploy",
		"backbone.nosql_collections",
		"6 (current) > 4 (declared)",
		// Three words. This is the one verdict where a user is stopped and has
		// to act, so it is the worst place for the remedy to be four words away
		// — the reason `drift slice shrink` exists at all rather than the
		// six-word flag pair that used to be printed here.
		"drift slice shrink",
		"--no-slice-reconcile",
	} {
		if !contains(out, want) {
			t.Errorf("abort message missing %q\nfull output:\n%s", want, out)
		}
	}

	// The retired spelling must be gone from the output, not merely joined by
	// the short one — printing both would leave the long form as the thing a
	// user copies.
	if contains(out, "--allow-destructive") {
		t.Errorf("the six-word remedy is still being printed:\n%s", out)
	}
}

// manifestFrom builds a Manifest directly from a document, for tests that want a
// shape without writing a Driftfile to disk. There is no struct to populate any
// more — the document IS the manifest.
func manifestFrom(slice Node) *Manifest {
	return &Manifest{doc: slice, slice: slice}
}
