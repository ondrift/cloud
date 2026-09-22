package file

// schemawalk_test.go — the three shapes that make a schema walk lie.
//
// Each case here is a way the renderer can produce a CONFIDENT WRONG ANSWER
// rather than an error: fields that are present but not found, a required list
// that is present but not read, an accepted spelling that is never shown. None
// of them fails loudly, which is why they are pinned.
//
// The fixture is local rather than the real cached schema. A test that reads
// ~/.drift/driftfile.schema.json passes or fails on what the machine happens to
// have fetched, which is not a property of this code.

import (
	"strings"
	"testing"
)

// fixture mirrors the three constructs the real document uses: a $ref into
// definitions, an allOf that splits fields from their required list, and a
// oneOf carrying a string-or-object shorthand.
const fixture = `{
  "type": "object",
  "properties": {
    "slice": { "type": "string", "description": "The slice this file applies to." },
    "atomic": {
      "description": "Serverless section.",
      "allOf": [
        { "$ref": "#/definitions/atomicSection" },
        { "required": ["functions"] }
      ]
    },
    "backbone": {
      "type": "object",
      "properties": {
        "queues": {
          "type": "array",
          "description": "FIFO queues.",
          "items": {
            "oneOf": [
              { "type": "string" },
              {
                "type": "object",
                "properties": { "name": { "type": "string", "description": "Queue name." } },
                "required": ["name"]
              }
            ]
          }
        },
        "secrets": {
          "type": "object",
          "additionalProperties": { "type": "string", "description": "A secret value." }
        }
      }
    }
  },
  "definitions": {
    "atomicSection": {
      "type": "object",
      "properties": {
        "functions": {
          "type": "array",
          "items": { "$ref": "#/definitions/atomicEntry" }
        }
      }
    },
    "atomicEntry": {
      "type": "object",
      "required": ["handler", "memory"],
      "properties": {
        "route":   { "type": "string", "description": "The path it serves." },
        "handler": { "type": "string", "description": "The callable." },
        "memory":  { "type": "string", "description": "What it books.", "pattern": "^[0-9]+MB$" }
      }
    }
  }
}`

func mustRoot(t *testing.T) schemaNode {
	t.Helper()
	root, err := parseSchema([]byte(fixture))
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return root
}

// A $ref is followed, or the fields behind it are reported as absent — not as
// an error, as an empty answer, which is the worst outcome available.
func TestWalkFollowsRef(t *testing.T) {
	root := mustRoot(t)
	node, _, missing, err := walk(root, "atomic.functions")
	if err != nil || node == nil {
		t.Fatalf("atomic.functions did not resolve: missing=%q err=%v", missing, err)
	}
	if got := typeName(root, node); got != "<[]object>" {
		t.Errorf("type = %q, want <[]object>", got)
	}
	fields := fieldsOf(root, node)
	if len(fields) != 3 {
		t.Fatalf("fields = %v, want three from the $ref'd entry", fields)
	}
	// Required first, then alphabetical.
	if fields[0] != "handler" || fields[1] != "memory" {
		t.Errorf("fields = %v, want the two required ones first", fields)
	}
}

// The fields come from the $ref branch and the required list from its sibling.
// A walker that handles $ref alone finds the fields and marks none required.
func TestAllOfMergesRequiredFromSiblingBranch(t *testing.T) {
	root := mustRoot(t)
	node, _, _, err := walk(root, "atomic")
	if err != nil || node == nil {
		t.Fatal("atomic did not resolve")
	}
	if !isRequired(root, node, "functions") {
		t.Error("functions is required by the allOf's second branch and was not read as required")
	}
	if fs := fieldsOf(root, node); len(fs) != 1 || fs[0] != "functions" {
		t.Errorf("fields = %v, want [functions] from the $ref'd branch", fs)
	}
	// The local description must survive the merge with the $ref'd target.
	if d := str(flatten(root, node, 0), "description"); d != "Serverless section." {
		t.Errorf("description = %q, want the node's own", d)
	}
}

// Both spellings or the output contradicts what lint accepts.
func TestOneOfRendersEveryAcceptedForm(t *testing.T) {
	root := mustRoot(t)
	node, _, _, err := walk(root, "backbone.queues")
	if err != nil || node == nil {
		t.Fatal("backbone.queues did not resolve")
	}
	forms := formsFor(root, node)
	if len(forms) != 2 {
		t.Fatalf("forms = %d, want 2 (the bare name and the long form)", len(forms))
	}
	var sawString, sawObject bool
	for _, f := range forms {
		switch typeName(root, f) {
		case "<string>":
			sawString = true
		case "<object>":
			sawObject = true
			if !isRequired(root, f, "name") {
				t.Error("name is required on the long form and was not marked")
			}
		}
	}
	if !sawString || !sawObject {
		t.Errorf("forms did not cover both spellings: string=%v object=%v", sawString, sawObject)
	}
	if got := typeName(root, node); !strings.Contains(got, "|") {
		t.Errorf("type = %q, want both forms named", got)
	}
}

// An array is walked THROUGH. Making a user write `functions.items.memory`
// would put a JSON Schema keyword in a tool whose job is to hide it.
func TestArrayIsWalkedThrough(t *testing.T) {
	root := mustRoot(t)
	node, _, missing, err := walk(root, "atomic.functions.memory")
	if err != nil || node == nil {
		t.Fatalf("path through the array failed: missing=%q", missing)
	}
	if p := str(node, "pattern"); p != "^[0-9]+MB$" {
		t.Errorf("landed on the wrong node; pattern = %q", p)
	}
}

// A map-typed node explains its VALUE for any key, which is the honest answer
// for a secret name the schema has never heard of.
func TestMapTypeExplainsItsValue(t *testing.T) {
	root := mustRoot(t)
	if got := typeName(root, mustNode(t, root, "backbone.secrets")); got != "<map[string]string>" {
		t.Errorf("type = %q, want <map[string]string>", got)
	}
	node, _, _, err := walk(root, "backbone.secrets.DATABASE_URL")
	if err != nil || node == nil {
		t.Fatal("a key under a map type should resolve to the value schema")
	}
	if d := str(node, "description"); d != "A secret value." {
		t.Errorf("description = %q, want the value's", d)
	}
}

// A miss reports where it got to, so the caller can suggest from that level.
func TestMissReportsTheLevelItReached(t *testing.T) {
	root := mustRoot(t)
	node, at, missing, err := walk(root, "backbone.queus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node != nil {
		t.Fatal("a miss must not return a node")
	}
	if missing != "queus" {
		t.Errorf("missing = %q, want the failing segment", missing)
	}
	if n := nearest(fieldsOf(root, at), missing); n != "queues" {
		t.Errorf("nearest = %q, want queues", n)
	}
}

// The suggester must stay quiet rather than offer the alphabetically luckiest
// key — a wrong suggestion sends someone to edit a field that is not theirs.
// The fourth construct, and the one that made the command say the opposite of
// the truth: requirements carried in anyOf branches rather than in `required`.
//
// The real atomicEntry has no top-level required at all — it is
// `[route, method, handler]` OR `[name, memory, handler]` — so a walker reading
// only `required` renders every function field as optional, under a description
// that says "Required, and never inferred".
func TestAnyOfRequiredBranchesAreBothShown(t *testing.T) {
	root, err := parseSchema([]byte(`{
      "properties": { "fn": { "$ref": "#/definitions/entry" } },
      "definitions": {
        "entry": {
          "type": "object",
          "anyOf": [
            { "required": ["route", "method", "handler"] },
            { "required": ["name", "memory", "handler"] }
          ],
          "properties": {
            "route":   { "type": "string" },
            "method":  { "type": "string" },
            "name":    { "type": "string" },
            "memory":  { "type": "string" },
            "handler": { "type": "string" }
          }
        }
      }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	node := mustNode(t, root, "fn")

	sets := requiredSets(root, node)
	if len(sets) != 2 {
		t.Fatalf("requiredSets = %v, want both branches", sets)
	}

	// Required in EVERY branch, so genuinely mandatory.
	if !isRequired(root, node, "handler") {
		t.Error("handler is required in both branches and must be marked")
	}
	// Required in ONE branch. Marking it would tell someone to write both
	// spellings; marking nothing would tell them to write neither.
	for _, conditional := range []string{"route", "method", "name", "memory"} {
		if isRequired(root, node, conditional) {
			t.Errorf("%s is required in only one branch and must not be marked mandatory", conditional)
		}
	}
}

func TestNearestDeclinesWhenNothingIsClose(t *testing.T) {
	if got := nearest([]string{"queues", "secrets", "nosql"}, "kubernetes"); got != "" {
		t.Errorf("nearest = %q, want no suggestion", got)
	}
}

// A $ref cycle must answer, not recurse until the stack goes.
func TestFlattenBoundsARefCycle(t *testing.T) {
	root, err := parseSchema([]byte(`{
      "properties": { "a": { "$ref": "#/definitions/loop" } },
      "definitions": { "loop": { "$ref": "#/definitions/loop" } }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = flatten(root, sub(sub(root, "properties"), "a"), 0)
	}()
	<-done
}

func TestLooksLikeFile(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"backbone.nosql", false},
		{"atomic", false},
		{"./Driftfile", true},
		{"../other/Driftfile", true},
		{"staging/", true},
		{".", true},
	} {
		if got := looksLikeFile(tc.in); got != tc.want {
			t.Errorf("looksLikeFile(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func mustNode(t *testing.T, root schemaNode, path string) schemaNode {
	t.Helper()
	n, _, missing, err := walk(root, path)
	if err != nil || n == nil {
		t.Fatalf("%s did not resolve (missing=%q, err=%v)", path, missing, err)
	}
	return n
}
