package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A shape key is announced ONCE PER PATH, not once per occurrence.
//
// `Warn` keys on the deprecation's `Old`, which is the path, so a manifest
// declaring nineteen function bookings and seven collection sizes hears about
// each once. Per-occurrence would bury the file's real errors under its own
// notices, which is how a notice becomes noise people filter out.
func TestShapeKeys_AnnouncedOncePerPathHoweverManyOccurrences(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	body := "slice: demo\natomic:\n  functions:\n"
	for _, id := range []string{"get:a", "get:b", "get:c"} {
		method, route, _ := strings.Cut(id, ":")
		body += "    - route: " + route + "\n      method: " + method +
			"\n      handler: H\n      memory: 32MB\n"
	}
	body += "backbone:\n  nosql:\n" +
		"    - slot: one\n      size: 5MB\n" +
		"    - slot: two\n      size: 5MB\n"

	parseShape(t, body)

	if n := strings.Count(notices.String(), "atomic.functions[].memory"); n != 1 {
		t.Errorf("three declared bookings produced %d notices, want 1", n)
	}
	if n := strings.Count(notices.String(), "backbone.nosql[].size"); n != 1 {
		t.Errorf("two declared collection sizes produced %d notices, want 1", n)
	}
}

// Parsing twice in one process still says it once. The once-per-name rule is
// process-wide on purpose — a `drift file apply` parses more than once, and a
// user renaming one thing must not see the same line twice.
func TestShapeKeys_ParsingTwiceStillAnnouncesOnce(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	body := shapeDriftfile
	parseShape(t, body)
	parseShape(t, body)

	if n := strings.Count(notices.String(), "atomic.atomic_size"); n != 1 {
		t.Errorf("two parses produced %d notices, want 1", n)
	}
}

// A shape key is IGNORED, not aliased: it has no target on this side, so the
// value stays exactly where it was written.
//
// Left in place deliberately — nothing downstream reads it, the schema still
// accepts it, and removing it would change what `drift file lint` validates.
func TestShapeKeys_TheValueIsLeftWhereItWasWritten(t *testing.T) {
	m := parseShape(t, shapeDriftfile)

	if got := m.Slice().Str("atomic", "atomic_size"); got != "100MB" {
		t.Errorf("atomic_size = %q, want it left untouched at 100MB — an ignored key "+
			"has no target to be rewritten to", got)
	}
}

// The compiled floor survives the annotations moving to the root.
//
// It is read from the SCHEMA rather than from the document, so it must keep
// working when a Driftfile stops writing `memory` — and returning zero here
// would not fail the floor check, it would silently skip it.
func TestShapeKeys_TheCompiledFloorSurvivesTheAnnotationMove(t *testing.T) {
	floor := CompiledMemoryFloor()
	if floor.Bytes != 16*1024*1024 {
		t.Fatalf("the compiled floor reads %d bytes, want 16MB — a zero here does not "+
			"refuse a low booking, it stops checking for one", floor.Bytes)
	}
	if !floor.Applies("go") {
		t.Error("the floor should bind a compiled language")
	}
	if floor.Applies("python") {
		t.Error("the floor should not bind an interpreted language")
	}
}

// A machine holding the schema from BEFORE the annotations moved still gets a
// floor, from their old home on `atomicEntry.properties.memory`.
//
// This is the case the fallback exists for and the one a test is most likely to
// miss: the cached schema is whatever the platform last served, which can predate
// any change here, and reading only the new location would silently stop checking
// on every machine that has not refreshed.
func TestShapeKeys_AnOlderCachedSchemaStillYieldsTheFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the retired location, exactly as an older served schema carried it.
	legacy := `{"$schema":"http://json-schema.org/draft-07/schema#","version":"1.10.0",
	  "definitions":{"atomicEntry":{"properties":{"memory":{
	    "x-drift-compiled-minimum":"16MB",
	    "x-drift-interpreted-languages":["node","php","python","ruby"]}}}}}`
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"),
		[]byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	floor := CompiledMemoryFloor()
	if floor.Bytes != 16*1024*1024 {
		t.Fatalf("an older cached schema yielded %d bytes — the floor check is skipped "+
			"on every machine that has not refreshed", floor.Bytes)
	}
	if floor.Applies("python") {
		t.Error("the interpreted set did not come through the fallback")
	}
}

// shapeDriftfile declares one real function, because the schema requires at
// least one: `functions: []` is refused, so a fixture cannot dodge the list.
const shapeDriftfile = "slice: demo\natomic:\n  atomic_size: 100MB\n  functions:\n" +
	"    - route: ping\n      method: get\n      handler: GetPing\n"

func parseShape(t *testing.T, body string) *Manifest {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}
