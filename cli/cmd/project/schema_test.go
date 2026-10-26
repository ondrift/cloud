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

// The guard that makes the rest of this suite mean something.
//
// Most of what these tests assert is "the platform's schema REJECTS this" — and
// with no schema on the machine, `validateAgainstSchema` returns nothing to say
// and every one of those assertions passes by validating nothing at all. That is
// not hypothetical: the whole suite was green on a developer laptop and failed on
// CI for exactly this reason, because a laptop that has run the CLI has a cached
// schema and a fresh runner does not.
//
// So the absence is made loud, here, once. A committed fixture copy would be the
// second definition of the format this card removed — and the copy that used to
// exist rotted because nothing compared it to the original.
func TestSchemaMustBePresentOrTheSuiteIsTheatre(t *testing.T) {
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })

	sch, err := loadSchema()
	if err != nil {
		t.Fatalf("the cached Driftfile schema is unusable: %v", err)
	}
	if sch == nil {
		t.Fatal("no Driftfile schema on this machine, so every rejection test in this " +
			"package would pass without validating anything.\n" +
			"Fetch it once and it stays: `drift slice list` while online, or\n" +
			"  mkdir -p ~/.drift && curl -fsS https://api.ondrift.eu/driftfile/schema -o ~/.drift/driftfile.schema.json")
	}
}

// The schema validates the COMPILED document, and a document it describes is
// accepted whatever else it could have allowed.
//
// The Driftfile is HCL, and the reader's block structs decide which blocks and
// attributes a file may hold: a worker the platform's schema adds is invisible to
// this binary until a block for it exists, so the property that the schema alone
// decides what is legal no longer holds for the file. It still holds for the
// document the compiler writes, which is what this stub describes: `slice` and
// `canvas`, and nothing else.
func TestSchema_ValidatesTheCompiledDocument(t *testing.T) {
	withSchema(t, `{
	  "$schema": "http://json-schema.org/draft-07/schema#",
	  "type": "object",
	  "required": ["slice"],
	  "additionalProperties": false,
	  "properties": {
	    "slice":  {"type": "string"},
	    "canvas": {"type": "object"}
	  }
	}`)

	path := writeDriftfile(t, "slice \"hello\" {}\ncanvas {\n  site \"/\" { dir = \"canvas\" }\n}\n", "canvas/")

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("a document the schema describes was refused:\n%v", err)
	}
	if m.Name() != "hello" {
		t.Errorf("name = %q, want hello", m.Name())
	}
	if sites := m.Slice().Entries("dir", "canvas", "sites"); len(sites) != 1 || sites[0].Str("dir") != "canvas" {
		t.Errorf("the site did not reach the document: %+v", sites)
	}
}

// The schema is the authority for rejection: a key the compiled document carries
// and the schema forbids is refused because the schema says
// additionalProperties: false, and the refusal is moved back to the block of the
// file that produced it.
func TestSchema_RejectsWhatTheSchemaRejects(t *testing.T) {
	withSchema(t, `{
	  "$schema": "http://json-schema.org/draft-07/schema#",
	  "type": "object",
	  "required": ["slice"],
	  "additionalProperties": false,
	  "properties": {
	    "slice":  {"type": "string"},
	    "canvas": {
	      "type": "object",
	      "properties": {
	        "sites": {
	          "type": "array",
	          "items": {
	            "type": "object",
	            "additionalProperties": false,
	            "properties": {"dir": {"type": "string"}}
	          }
	        }
	      }
	    }
	  }
	}`)

	body := "slice \"hello\" {}\n" + // line 1
		"canvas {\n" + // line 2
		"  site \"/\" { dir = \"canvas\" }\n" + // line 3
		"  site \"/admin\" { dir = \"canvas\" }\n" + // line 4: its path is the key the schema forbids
		"}\n"
	_, err := ParseDriftfile(writeDriftfile(t, body, "canvas/"))
	if err == nil {
		t.Fatal("a key the schema forbids was accepted")
	}
	if !contains(err.Error(), "path") {
		t.Errorf("the error should name the offending key, got: %v", err)
	}
	if !contains(err.Error(), "Driftfile:4:") || contains(err.Error(), "Driftfile:3:") {
		t.Errorf("the refusal should be at the site that carries the key (line 4), got: %v", err)
	}
}

// A machine that has never reached the platform holds no schema. Parsing still
// works — `drift file run` needs no platform at all — but SchemaAvailable must
// say so, because a caller that reports "valid" after validating nothing is worse
// than one that refuses.
func TestSchema_AbsentIsReportedNotFaked(t *testing.T) {
	withSchema(t, "")

	if SchemaAvailable() {
		t.Fatal("SchemaAvailable() is true with no schema on disk")
	}

	path := writeDriftfile(t, "slice \"hello\" {}\ncanvas {\n  site \"/\" { dir = \"canvas\" }\n}\n", "canvas/")
	if _, err := ParseDriftfile(path); err != nil {
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
