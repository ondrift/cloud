package project

import (
	"os"
	"path/filepath"
	"testing"
)

// withSchema points the CLI's schema cache at a temporary HOME holding `doc`,
// and resets the compiled-schema memo so each test gets its own.
func withSchema(t *testing.T, doc string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if doc != "" {
		if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })
}

// THE test for this change.
//
// The CLI used to reject a Driftfile carrying a field it had not been taught —
// `ParseDriftfile` re-decoded with `KnownFields(true)` — so every format addition
// needed a CLI release before anyone could use it. That coupling is what serving
// the schema was meant to remove, and for a while nothing read the schema.
//
// Here the platform's schema permits a field this binary has no struct for. The
// parse must SUCCEED: legality is the platform's call, and a value this CLI does
// not act on is none of its business (#CLI-STANDARDUSAGE-ERF1CV).
// Note on what is validated: the SHORTHAND-EXPANDED document, not the bytes the
// user typed. `canvas: ./canvas` is an object by the time the schema sees it, which
// is why the real schema accepts both forms for those keys. Validating the raw
// document instead would make error locations match the user's file exactly, and is
// worth revisiting — it is a separate decision from who owns the format.
func TestSchema_AcceptsAFieldThisBinaryPredates(t *testing.T) {
	withSchema(t, `{
	  "$schema": "http://json-schema.org/draft-07/schema#",
	  "type": "object",
	  "required": ["name"],
	  "additionalProperties": false,
	  "properties": {
	    "name":            {"type": "string"},
	    "canvas":          {"type": ["string", "object"]},
	    "a_future_field":  {"type": "string"}
	  }
	}`)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Driftfile"), "name: hello\ncanvas: ./canvas\na_future_field: whatever\n")
	mustMkdir(t, filepath.Join(dir, "canvas"))

	m, err := ParseDriftfile(filepath.Join(dir, "Driftfile"))
	if err != nil {
		t.Fatalf("a field the SCHEMA allows was rejected, so the platform still cannot "+
			"add a field without a CLI release:\n%v", err)
	}
	if m.Name() != "hello" {
		t.Errorf("name = %q, want hello", m.Name())
	}
	// And it survives in the document, so nothing downstream has silently dropped it.
	if got := m.Raw().Str("a_future_field"); got != "whatever" {
		t.Errorf("the unknown field was dropped from the document: %q", got)
	}
}

// The other half: the schema is the authority for rejection too. A field NEITHER
// side knows is refused, because the schema says additionalProperties: false —
// not because this binary has a struct without it.
func TestSchema_RejectsWhatTheSchemaRejects(t *testing.T) {
	withSchema(t, `{
	  "$schema": "http://json-schema.org/draft-07/schema#",
	  "type": "object",
	  "required": ["name"],
	  "additionalProperties": false,
	  "properties": {"name": {"type": "string"}, "canvas": {"type": ["string", "object"]}}
	}`)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Driftfile"), "name: hello\ncanvas: ./canvas\nnmae: typo\n")
	mustMkdir(t, filepath.Join(dir, "canvas"))

	_, err := ParseDriftfile(filepath.Join(dir, "Driftfile"))
	if err == nil {
		t.Fatal("a key the schema forbids was accepted")
	}
	if !contains(err.Error(), "nmae") {
		t.Errorf("the error should name the offending key, got: %v", err)
	}
}

// A machine that has never reached the platform holds no schema. Parsing still
// works — `drift project run` needs no platform at all — but SchemaAvailable must
// say so, because a caller that reports "valid" after validating nothing is worse
// than one that refuses.
func TestSchema_AbsentIsReportedNotFaked(t *testing.T) {
	withSchema(t, "")

	if SchemaAvailable() {
		t.Fatal("SchemaAvailable() is true with no schema on disk")
	}

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Driftfile"), "name: hello\ncanvas: ./canvas\n")
	mustMkdir(t, filepath.Join(dir, "canvas"))
	if _, err := ParseDriftfile(filepath.Join(dir, "Driftfile")); err != nil {
		t.Errorf("parsing must still work offline on a never-online machine: %v", err)
	}
}

// oneOf reports every branch it tried, so a map with a bad key inside also fails
// the "or an array" branch. Printing `got object, want array` beside the real
// error invites the user to fix the wrong thing.
func TestSchemaErrors_DropsTheBranchNotTaken(t *testing.T) {
	got := dropShallowerBranches([]string{
		"at '/environments': got object, want array",
		"at '/environments/staging': additional properties 'name' not allowed",
	})
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d: %v", len(got), got)
	}
	if !contains(got[0], "staging") {
		t.Errorf("kept the shallower branch instead of the specific error: %v", got)
	}
}

// ...but a shallower error with no deeper sibling must survive, or a top-level
// problem would vanish entirely.
func TestSchemaErrors_KeepsAStandaloneShallowError(t *testing.T) {
	got := dropShallowerBranches([]string{"at '/name': got number, want string"})
	if len(got) != 1 {
		t.Fatalf("a standalone error was dropped: %v", got)
	}
}
