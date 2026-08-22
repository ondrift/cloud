package slice

import (
	"encoding/json"
	"testing"
)

// dialPlacements is every dial the form offers, mapped to where the platform
// reads it from. The value is arbitrary; only the destination is under test.
//
// Spelling is the whole subject. models.SliceConfig tags its containers and its
// maps for json but leaves its scalar limits with a bson tag only, so
// encoding/json falls back to the Go field name for almost all of them — and
// `backup_retention_days`, the one scalar carrying a json tag of its own, is the
// exception that has to be spelled the other way. A miss either way is silent:
// an unknown key is dropped without an error, so the figure is priced, agreed
// to, and never applied.
var dialPlacements = []struct {
	scalar string
	path   []string
	value  int
}{
	{"atomic.MaxFunctionRuntimeInSeconds", []string{"atomic", "MaxFunctionRuntimeInSeconds"}, 60},
	{"atomic.MaxNumberOfDeploymentsInHistory", []string{"atomic", "MaxNumberOfDeploymentsInHistory"}, 5},
	{"atomic.MaxNumberOfHoursForLogRetention", []string{"atomic", "MaxNumberOfHoursForLogRetention"}, 72},
	{"atomic.MaxNumberOfRequestsPerMinute", []string{"atomic", "MaxNumberOfRequestsPerMinute"}, 2500},
	{"atomic.MaxNumberOfScheduledJobs", []string{"atomic", "MaxNumberOfScheduledJobs"}, 3},
	{"atomic.MaxStorageBytes", []string{"atomic", "MaxStorageBytes"}, 100 * bytesPerMiB},
	{"backbone.backup_retention_days", []string{"backbone", "backup_retention_days"}, 3},
	{"backbone.blobs.MaxSizeInBytesEach", []string{"backbone", "blobs", "MaxSizeInBytesEach"}, 5 * bytesPerMiB},
	{"backbone.locks.MaxConcurrent", []string{"backbone", "locks", "MaxConcurrent"}, 100000},
	{"backbone.queues.MaxDepthEach", []string{"backbone", "queues", "MaxDepthEach"}, 500},
	{"backbone.realtime.MaxConcurrentConnections", []string{"backbone", "realtime", "MaxConcurrentConnections"}, 50},
	{"backbone.secrets.MaxCount", []string{"backbone", "secrets", "MaxCount"}, 7},
	{"backbone.secrets.MaxSizeInBytesEach", []string{"backbone", "secrets", "MaxSizeInBytesEach"}, 1024},
	{"canvas.TotalMaxSizeInBytes", []string{"canvas", "TotalMaxSizeInBytes"}, 50 * bytesPerMiB},
	{"deed.pocket.MaxSizeInBytesEach", []string{"deed", "pocket", "MaxSizeInBytesEach"}, 16384},
	{"deed.vault.MaxEntriesPerUID", []string{"deed", "vault", "MaxEntriesPerUID"}, 100},
	{"deed.vault.MaxSizeInBytesEach", []string{"deed", "vault", "MaxSizeInBytesEach"}, 4096},
}

// Every dial the form offers must land at the path the platform reads it from.
func TestBuildConfigPutsEveryDialWhereThePlatformReadsIt(t *testing.T) {
	scalars := map[string]int{}
	for _, d := range dialPlacements {
		scalars[d.scalar] = d.value
	}

	cfg := buildConfig(0, nil, backboneShape{}, 0, scalars)

	// Read it the way the server does: through JSON, by path.
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("the config does not marshal: %v", err)
	}
	var got map[string]any
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("the config does not round-trip: %v", uerr)
	}

	for _, d := range dialPlacements {
		if at := valueAt(got, d.path); at != float64(d.value) {
			t.Errorf("%s landed at %v = %v, want %v — the platform reads that path and would find nothing",
				d.scalar, d.path, at, d.value)
		}
	}
}

// Every dial the form defines must be one the table above places.
//
// The form is the authority on which dials exist; this reads it rather than
// repeating it, so a dial added there and forgotten here fails instead of
// shipping untested.
func TestEveryDialTheFormDefinesIsCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, d := range dialPlacements {
		covered[d.scalar] = true
	}

	found := map[string]bool{}
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, c := range ns {
			if c.scalar != "" {
				found[c.scalar] = true
				if !covered[c.scalar] {
					t.Errorf("the form offers dial %q and the table does not place it", c.scalar)
				}
			}
			walk(c.children)
		}
	}
	walk(newShapeForm("t").root)

	for scalar := range covered {
		if !found[scalar] {
			t.Errorf("the table places %q and the form no longer offers it", scalar)
		}
	}
}

// The two dials that also arrive as their own argument must agree with it.
//
// `atomic.MaxStorageBytes` and `canvas.TotalMaxSizeInBytes` are collected twice
// — once as a dial and once as the storage/canvas figure buildConfig takes
// directly — and the dials are applied last, so a disagreement would resolve
// silently in the dial's favour. Both scale by MiB, and this is what holds them
// to it: a dial that stopped scaling would write raw MiB over a byte count and
// under-declare the volume a million-fold.
func TestTheDoubleCollectedDialsAgreeWithTheirArgument(t *testing.T) {
	cfg := buildConfig(100, nil, backboneShape{}, 50, map[string]int{
		"atomic.MaxStorageBytes":     100 * bytesPerMiB,
		"canvas.TotalMaxSizeInBytes": 50 * bytesPerMiB,
	})

	atomic, _ := cfg["atomic"].(map[string]any)
	if got := atomic["MaxStorageBytes"]; got != 100*bytesPerMiB {
		t.Errorf("MaxStorageBytes = %v, want %d", got, 100*bytesPerMiB)
	}
	canvas, _ := cfg["canvas"].(map[string]any)
	if got := canvas["TotalMaxSizeInBytes"]; got != 50*bytesPerMiB {
		t.Errorf("TotalMaxSizeInBytes = %v, want %d", got, 50*bytesPerMiB)
	}
}

// The count caps come off the declared lists, as they do in the browser.
//
// `MaxNumberOfFunctions` and `SQL.MaxDatabases` are ceilings the platform
// enforces and does not derive from the lists beside them, so a shape that
// declares six functions and leaves the cap at zero is filled from the free
// preset's five and refuses the sixth deploy — after it has been priced and
// paid for. The configurator sends `functions.length` and the database count
// for this reason; a slice made here has to mean the same thing as the same
// slice made there.
func TestBuildConfigDerivesTheCountCapsFromWhatIsDeclared(t *testing.T) {
	slots := []functionSlot{
		{Method: "get", Route: "a", Memory: 32},
		{Method: "post", Route: "b", Memory: 32},
		{Method: "get", Route: "c", Memory: 32},
	}
	bb := backboneShape{Databases: map[string]int{"ledger": 10, "audit": 10}}

	cfg := buildConfig(0, slots, bb, 0, map[string]int{})

	atomic, _ := cfg["atomic"].(map[string]any)
	if got := atomic["MaxNumberOfFunctions"]; got != len(slots) {
		t.Errorf("MaxNumberOfFunctions = %v, want %d — the sixth function would be refused after it was paid for",
			got, len(slots))
	}

	backbone, _ := cfg["backbone"].(map[string]any)
	sql, _ := backbone["sql"].(map[string]any)
	if got := sql["MaxDatabases"]; got != len(bb.Databases) {
		t.Errorf("MaxDatabases = %v, want %d", got, len(bb.Databases))
	}
}

// Declaring nothing states no ceiling, rather than a ceiling of zero.
//
// A canvas-only slice really does declare no functions, and writing an explicit
// 0 would be this form inventing a limit instead of leaving the platform to
// apply its own.
func TestBuildConfigStatesNoCountCapForAListThatIsEmpty(t *testing.T) {
	cfg := buildConfig(0, nil, backboneShape{}, 0, map[string]int{})

	atomic, _ := cfg["atomic"].(map[string]any)
	if _, present := atomic["MaxNumberOfFunctions"]; present {
		t.Error("a slice declaring no function stated a function ceiling")
	}
	if _, present := cfg["backbone"]; present {
		t.Error("a slice declaring no database stated a backbone shape")
	}
}

// A dial nobody touched stays absent, so the platform's own defaults apply
// rather than a zero this form invented. Pins the behaviour the placement fix
// must not change.
func TestBuildConfigLeavesAnUntouchedDialAbsent(t *testing.T) {
	cfg := buildConfig(0, nil, backboneShape{}, 0, map[string]int{})

	for _, pillar := range []string{"backbone", "deed", "canvas"} {
		if _, present := cfg[pillar]; present {
			t.Errorf("%s was written with nothing set; an empty object declares nothing and should be absent", pillar)
		}
	}
}

// valueAt walks a decoded config by path, returning -1 when any hop is missing
// so a wrong placement reports as a miss rather than panicking.
func valueAt(m map[string]any, path []string) float64 {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return -1
		}
		cur, ok = obj[key]
		if !ok {
			return -1
		}
	}
	n, ok := cur.(float64)
	if !ok {
		return -1
	}
	return n
}
