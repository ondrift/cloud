package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSchemaEdited is withRealSchema with the document changed by edit first,
// for a test about a schema shape the platform has not served yet.
func withSchemaEdited(t *testing.T, edit func(doc map[string]any)) {
	t.Helper()
	withRealSchema(t)
	p := filepath.Join(os.Getenv("HOME"), ".drift", "driftfile.schema.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	compiledSchema = nil
}

// The booking range is the platform's, published in the schema's root
// `x-drift` block as `memory-pattern`. The document has no `memory` key, so
// the range has no other home in a schema that describes only what the
// compiler writes; reading it from the old property alone would skip the check
// against such a schema, and a 512MiB booking would reach the platform.
func TestTheBookingRangeIsReadFromTheRootBlock(t *testing.T) {
	// Whichever schema this machine holds, end with the range in x-drift only:
	// format 1 carries it on the property, format 2 already in the root block.
	withSchemaEdited(t, func(doc map[string]any) {
		props := doc["definitions"].(map[string]any)["atomicEntry"].(map[string]any)["properties"].(map[string]any)
		xdrift := doc["x-drift"].(map[string]any)
		if memory, ok := props["memory"].(map[string]any); ok {
			xdrift["memory-pattern"] = memory["pattern"]
			delete(props, "memory")
		}
		if xdrift["memory-pattern"] == nil {
			t.Fatal("this schema publishes the booking range nowhere")
		}
	})
	_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n\n"+
		"atomic {\n  backend \"default\" {\n    function \"GET /big\" {\n      handler = \"GetBig\"\n      memory  = \"512MiB\"\n    }\n  }\n}\n", "atomic/"))
	if err == nil || !strings.Contains(err.Error(), "Memory out of range") {
		t.Fatalf("a 512MiB booking compiled against a schema publishing the range only in x-drift: %v", err)
	}
}

// The control: against the schema this machine holds as served, whichever
// format it is, the same booking is refused.
func TestABookingOutsideTheRangeIsRefusedByTheServedSchema(t *testing.T) {
	withRealSchema(t)
	_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\n\n"+
		"atomic {\n  backend \"default\" {\n    function \"GET /big\" {\n      handler = \"GetBig\"\n      memory  = \"512MiB\"\n    }\n  }\n}\n", "atomic/"))
	if err == nil || !strings.Contains(err.Error(), "Memory out of range") {
		t.Fatalf("a 512MiB booking compiled against the served schema: %v", err)
	}
}
