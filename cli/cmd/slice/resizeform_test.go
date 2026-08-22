package slice

import (
	"encoding/json"
	"testing"
)

// configFrom decodes a config literal the way the api serves one, so a test
// reads the same bytes the form does.
func configFrom(t *testing.T, raw string) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("bad config literal: %v", err)
	}
	return cfg
}

// memoryRowsUnder returns the memory value and placeholder of every function row.
func memoryRowsUnder(f *shapeForm) (values, placeholders []string) {
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			if n.kind == kindItem && n.prefix == "Function " && len(n.children) > 1 {
				values = append(values, n.children[1].value)
				placeholders = append(placeholders, n.children[1].placeholder)
			}
			walk(n.children)
		}
	}
	walk(f.root)
	return values, placeholders
}

// A slice from before per-function bookings must not come back with one.
//
// Its functions share ONE slice-wide pool and it declares no function names at
// all. Writing that pool into each row is the whole trap: it is the slice's
// figure, not the row's, so five rows carrying 32 MB apiece reprice a 32 MB
// slice at 160 — on a form somebody opened to change something else. The pool
// is shown as a placeholder, which is visibly not a value and does not submit.
func TestFillFromConfigNeverBooksALegacyPoolPerFunction(t *testing.T) {
	cfg := configFrom(t, `{
		"atomic": {"MaxNumberOfFunctions": 5, "MaxFunctionMemoryBytes": 33554432},
		"backbone": {}, "canvas": {}
	}`)

	f := newShapeForm("legacy")
	fillFromConfig(f, cfg)

	values, placeholders := memoryRowsUnder(f)
	if len(values) != 5 {
		t.Fatalf("a slice with 5 slots opened %d function rows", len(values))
	}
	for i, v := range values {
		if v != "" {
			t.Errorf("row %d carries memory %q — the shared pool became a per-function booking", i, v)
		}
		if placeholders[i] != "32" {
			t.Errorf("row %d shows placeholder %q, want the 32 MB pool the slice runs on", i, placeholders[i])
		}
	}

	// The submitted shape books nothing, which is what leaves the price alone.
	shape, _, err := f.collectForTest()
	if err == nil {
		for _, s := range shape.Slots {
			if s.Memory != 0 {
				t.Errorf("route %q was booked %d MB nobody typed", s.Route, s.Memory)
			}
		}
	}
}

// A slice on the declaration model opens on exactly what it declares, and what
// comes back out is what went in.
//
// The form is the only thing between a stored config and the config that
// replaces it, so a value it fails to read is a limit the tenant silently loses
// on a resize they opened for something else entirely.
func TestFillFromConfigRoundTripsWhatTheSliceDeclares(t *testing.T) {
	cfg := configFrom(t, `{
		"atomic": {
			"MaxNumberOfFunctions": 2,
			"MaxNumberOfScheduledJobs": 3,
			"MaxNumberOfRequestsPerMinute": 2500,
			"MaxStorageBytes": 104857600,
			"functions": [
				{"name": "get:probe", "route": "probe", "method": "get", "memory_bytes": 33554432},
				{"name": "post:ingest", "route": "ingest", "method": "post", "memory_bytes": 67108864}
			]
		},
		"backbone": {
			"secrets": {"MaxCount": 7, "MaxSizeInBytesEach": 1024},
			"blobs": {"MaxSizeInBytesEach": 5242880, "buckets": {"uploads": 10485760}},
			"nosql": {"collections": {"events": 10485760}},
			"sql": {"MaxDatabases": 1, "databases": {"ledger": 10485760}},
			"queues": {"MaxDepthEach": 500, "queues": {"emails": 250}},
			"realtime": {"MaxConcurrentConnections": 25},
			"locks": {"MaxConcurrent": 1000},
			"backup_retention_days": 3
		},
		"canvas": {"TotalMaxSizeInBytes": 52428800},
		"deed": {"vault": {"MaxSizeInBytesEach": 4096, "MaxEntriesPerUID": 100},
			"pocket": {"MaxSizeInBytesEach": 16384}}
	}`)

	f := newShapeForm("lab")
	fillFromConfig(f, cfg)

	shape, _, err := f.collectForTest()
	if err != nil {
		t.Fatalf("a shape read straight off a live slice did not collect: %v", err)
	}
	got := buildConfig(shape.StorageMiB, shape.Slots, shape.Backbone, shape.CanvasMiB, shape.BackboneScalars)

	raw, _ := json.Marshal(got)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)

	for _, want := range []struct {
		path  []string
		value float64
	}{
		{[]string{"atomic", "MaxNumberOfFunctions"}, 2},
		{[]string{"atomic", "MaxNumberOfScheduledJobs"}, 3},
		{[]string{"atomic", "MaxNumberOfRequestsPerMinute"}, 2500},
		{[]string{"atomic", "MaxStorageBytes"}, 104857600},
		{[]string{"backbone", "secrets", "MaxCount"}, 7},
		{[]string{"backbone", "secrets", "MaxSizeInBytesEach"}, 1024},
		{[]string{"backbone", "blobs", "MaxSizeInBytesEach"}, 5242880},
		{[]string{"backbone", "blobs", "buckets", "uploads"}, 10485760},
		{[]string{"backbone", "nosql", "collections", "events"}, 10485760},
		{[]string{"backbone", "sql", "databases", "ledger"}, 10485760},
		{[]string{"backbone", "queues", "MaxDepthEach"}, 500},
		{[]string{"backbone", "queues", "queues", "emails"}, 250},
		{[]string{"backbone", "realtime", "MaxConcurrentConnections"}, 25},
		{[]string{"backbone", "locks", "MaxConcurrent"}, 1000},
		{[]string{"backbone", "backup_retention_days"}, 3},
		{[]string{"canvas", "TotalMaxSizeInBytes"}, 52428800},
		{[]string{"deed", "vault", "MaxSizeInBytesEach"}, 4096},
		{[]string{"deed", "vault", "MaxEntriesPerUID"}, 100},
		{[]string{"deed", "pocket", "MaxSizeInBytesEach"}, 16384},
	} {
		if at := valueAt(out, want.path); at != want.value {
			t.Errorf("%v came back as %v, want %v — a resize would drop it", want.path, at, want.value)
		}
	}

	// The functions keep their identity and their own bookings.
	if len(shape.Slots) != 2 {
		t.Fatalf("got %d functions, want 2", len(shape.Slots))
	}
	byRoute := map[string]functionSlot{}
	for _, s := range shape.Slots {
		byRoute[s.Route] = s
	}
	if s := byRoute["probe"]; s.Method != "get" || s.Memory != 32 {
		t.Errorf("probe came back as %s at %d MB, want get at 32", s.Method, s.Memory)
	}
	if s := byRoute["ingest"]; s.Method != "post" || s.Memory != 64 {
		t.Errorf("ingest came back as %s at %d MB, want post at 64", s.Method, s.Memory)
	}
}

// A resize cannot rename the slice it is resizing.
func TestTheResizeFormLocksTheSliceName(t *testing.T) {
	f := newShapeForm("lab")
	f.root[0].locked = true

	if typesInto(f.root[0], 'x') {
		t.Error("the slice name accepts typing on a resize, so it can be renamed into a different slice")
	}
	// The control: unlocked, it is the create form's editable name row.
	f.root[0].locked = false
	if !typesInto(f.root[0], 'x') {
		t.Error("the slice name refuses typing even unlocked, so create cannot name a slice")
	}
}
