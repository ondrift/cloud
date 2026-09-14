package project

import (
	"strings"
	"testing"
)

// A schema stub with the same SHAPE as the platform's `env` reservation: a
// propertyNames composed of a pattern and two negations. Local, because this is
// about how an error is RENDERED, and pinning it to the served document would
// make the test fail whenever the reservation's contents change.
const relocateSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "canvas": {},
    "atomic": {
      "type": "object",
      "properties": {
        "functions": {"type": "array", "items": {"$ref": "#/definitions/atomicEntry"}}
      }
    }
  },
  "definitions": {
    "atomicEntry": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "name": {"type": "string"},
        "handler": {"type": "string"},
        "memory": {"type": "string"},
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
// against a whole Driftfile. That names no function, no key and no position —
// it is the least actionable thing this package can print, and it is what a
// user gets the first time they try to set BACKBONE_URL in `env:`.
func TestAReservedEnvNameIsReportedWhereItIs(t *testing.T) {
	withSchema(t, relocateSchema)
	root := demoProject(t, `
name: demo
atomic:
  functions:
    - name: get:ping
      handler: GetPing
      memory: 8MB
      env:
        BACKBONE_URL: http://evil.example.com
canvas: ./canvas
`)

	_, err := ParseDriftfile(root + "/Driftfile")
	if err == nil {
		t.Fatal("a reserved env name must be refused — the whole point of the reservation")
	}
	msg := err.Error()

	if strings.Contains(msg, "at ''") {
		t.Errorf("the error still points at the document root, which tells the user "+
			"nothing about where to look:\n  %s", msg)
	}
	// The entry it is in, and the key it is under. Both are recoverable and
	// neither is in the leaf the library hands over.
	if !strings.Contains(msg, "/atomic/functions/0") {
		t.Errorf("the error must name the function entry, got:\n  %s", msg)
	}
	if !strings.Contains(msg, "env") {
		t.Errorf("the error must name the key, got:\n  %s", msg)
	}
}

// THE CONTROL, and it is the one that matters: an error that ALREADY knows
// where it is must not be moved. `relocate` repairs the empty case only, and a
// version that rewrote every location would scramble every other message in the
// package while passing the test above.
func TestAnOrdinaryErrorKeepsItsOwnLocation(t *testing.T) {
	withSchema(t, relocateSchema)
	root := demoProject(t, `
name: demo
atomic:
  functions:
    - name: get:ping
      handler: GetPing
      memory: 8MB
      nonsense: true
canvas: ./canvas
`)

	_, err := ParseDriftfile(root + "/Driftfile")
	if err == nil {
		t.Fatal("an undeclared key must be refused")
	}
	msg := err.Error()
	if !strings.Contains(msg, "/atomic/functions/0") {
		t.Errorf("an ordinary error must keep pointing at its own instance, got:\n  %s", msg)
	}
	if strings.Contains(msg, "/atomic/functions/0/env") {
		t.Errorf("relocate moved an error that already had a location:\n  %s", msg)
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
