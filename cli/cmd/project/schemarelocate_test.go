package project

import (
	"strings"
	"testing"
)

// A schema stub with the same SHAPE as the platform's `env` reservation: a
// propertyNames composed of a pattern and two negations. Local, because this is
// about how an error is RENDERED, and pinning it to the served document would
// make the test fail whenever the reservation's contents change.
//
// It describes the compiled document: `slice`, and `atomic.elements[].pulses[]`
// with the keys a pulse compiles to.
const relocateSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "slice": {"type": "string"},
    "atomic": {
      "type": "object",
      "properties": {
        "elements": {"type": "array", "items": {"$ref": "#/definitions/elementEntry"}}
      }
    }
  },
  "definitions": {
    "elementEntry": {
      "type": "object",
      "properties": {
        "name": {"type": "string"},
        "pulses": {"type": "array", "items": {"$ref": "#/definitions/pulseEntry"}}
      }
    },
    "pulseEntry": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "route": {"type": "string"},
        "method": {"type": "string"},
        "handler": {"type": "string"},
        "env": {
          "type": "object",
          "propertyNames": {
            "allOf": [
              {"pattern": "^[A-Za-z_][A-Za-z0-9_]*$"},
              {"not": {"pattern": "^(?i:DRIFT_)"}},
              {"not": {"pattern": "^(?i:BACKBONE_URL|DEED_URL)$"}}
            ]
          },
          "additionalProperties": {"type": "string"}
        }
      }
    }
  }
}`

// THE MESSAGE A USER ACTUALLY MEETS when they set a reserved variable.
//
// `propertyNames` validates the property NAME as its own document, so the
// instance location resets to the root and the library renders
//
//	at '': 'not' failed
//
// against a whole document. That names no pulse, no key and no position: it is
// the least actionable thing this package can print, and it is what a user gets
// the first time they try to set BACKBONE_URL in `env`. Repaired, the refusal is
// reported at the `env` attribute of the pulse that carries it.
func TestAReservedEnvNameIsReportedWhereItIs(t *testing.T) {
	withSchema(t, relocateSchema)
	body := "slice \"demo\" { billing = \"monthly\" }\n" + // line 1
		"element \"api\" {\n" + // line 2
		"  pulse \"GET /ping\" {\n" + // line 3
		"    handler = \"GetPing\"\n" + // line 4
		"    memory  = \"8MiB\"\n" + // line 5
		"    env = {\n" + // line 6
		"      BACKBONE_URL = \"http://evil.example.com\"\n" +
		"    }\n" +
		"  }\n" +
		"}\n"

	_, err := ParseDriftfile(writeDriftfile(t, body, "atomic/api/"))
	if err == nil {
		t.Fatal("a reserved env name must be refused — the whole point of the reservation")
	}
	msg := err.Error()

	if strings.Contains(msg, "at ''") {
		t.Errorf("the error still points at the document root, which tells the user "+
			"nothing about where to look:\n  %s", msg)
	}
	// Moved back to the file: the env attribute, which is the line the key is
	// under. An unrepaired location resolves to the slice's label on line 1.
	if !strings.Contains(msg, "Driftfile:6:") {
		t.Errorf("the error must be at the env attribute (line 6), got:\n  %s", msg)
	}
	// The pulse it is in and the attribute, which are recoverable and are not in
	// the leaf the library hands over.
	if !strings.Contains(msg, `pulse "GET /ping": env:`) {
		t.Errorf("the error must name the pulse and its env, got:\n  %s", msg)
	}
}

// THE CONTROL, and it is the one that matters: an error that ALREADY knows
// where it is must not be moved. `relocate` repairs the empty case only, and a
// version that rewrote every location would scramble every other message in the
// package while passing the test above.
//
// The pulse carries a valid env and a `response` the stub does not list, so the
// refusal belongs to the pulse and must not drift onto its env.
func TestAnOrdinaryErrorKeepsItsOwnLocation(t *testing.T) {
	withSchema(t, relocateSchema)
	body := "slice \"demo\" { billing = \"monthly\" }\n" + // line 1
		"element \"api\" {\n" + // line 2
		"  pulse \"GET /ping\" {\n" + // line 3
		"    handler  = \"GetPing\"\n" +
		"    memory   = \"8MiB\"\n" +
		"    env      = { MODE = \"fast\" }\n" + // line 6
		"    response = \"text\"\n" + // line 7
		"  }\n" +
		"}\n"

	_, err := ParseDriftfile(writeDriftfile(t, body, "atomic/api/"))
	if err == nil {
		t.Fatal("an undeclared key must be refused")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Driftfile:3:") || !strings.Contains(msg, `pulse "GET /ping"`) {
		t.Errorf("an ordinary error must keep pointing at its own instance, the pulse on line 3, got:\n  %s", msg)
	}
	if !strings.Contains(msg, "response") {
		t.Errorf("the error should name the key the stub forbids, got:\n  %s", msg)
	}
	if strings.Contains(msg, "Driftfile:6:") {
		t.Errorf("relocate moved an error that already had a location onto the env:\n  %s", msg)
	}
}

// schemaProperty reads the LAST `properties/<name>` in the pointer, because the
// pointer nests and the outer names are the path taken to reach the failing
// subschema rather than the key it is about.
func TestSchemaPropertyReadsTheInnermostKey(t *testing.T) {
	cases := []struct{ url, want string }{
		{"file:///s.json#/definitions/atomicEntry/properties/env/propertyNames/allOf/2", "env"},
		{"file:///s.json#/properties/atomic/allOf/0/properties/functions/items", "functions"},
		{"file:///s.json#/definitions/nosqlEntry/properties/unique/items", "unique"},
		{"file:///s.json#", ""},
		{"file:///s.json", ""},
		{"file:///s.json#/definitions/atomicEntry", ""},
	}
	for _, c := range cases {
		if got := schemaProperty(c.url); got != c.want {
			t.Errorf("schemaProperty(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}
