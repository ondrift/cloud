package project

import (
	"os"
	"path/filepath"
	"testing"
)

// The compiled floor is read from the SCHEMA rather than from the document, so
// it must keep working whatever a Driftfile writes.
//
// Returning zero here would not fail the floor check, it would silently skip it.
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
