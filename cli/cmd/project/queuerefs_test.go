package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// writeManifest puts a Driftfile and one Go source file on disk, so the parse
// gets past its local-path checks and reaches the reference pass.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "atomic"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nfunc HandleOrder() {}\nfunc GetPing() {}\n"
	if err := os.WriteFile(filepath.Join(root, "atomic", "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "Driftfile")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A queue trigger whose queue the file never declares is reported, and the
// document is what answers it: the live slice carries a depth and no names.
func TestCheckQueueReferences_NamesAnUndeclaredQueue(t *testing.T) {
	m := manifestFrom(Node{"slice": "demo", "atomic": map[string]any{"functions": []any{
		map[string]any{"name": "queue:orders", "handler": "HandleOrder"},
	}}})

	warnings := checkQueueReferences(m)
	if len(warnings) != 1 {
		t.Fatalf("an undeclared queue must be reported once, got %d: %v", len(warnings), warnings)
	}
	for _, want := range []string{"orders", "queue:orders", "backbone.queues"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("the warning must carry %q, got %q", want, warnings[0])
		}
	}
}

// It WARNS and does not refuse, and that is the whole judgement in this file.
// The slice creates an undeclared queue on its first push and counts the write
// rather than refusing it, so the function genuinely fires — refusing the parse
// would break a setup that works.
func TestParse_AnUndeclaredQueueIsAWarningRatherThanARefusal(t *testing.T) {
	p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: orders
      method: queue
      handler: HandleOrder
`)
	if _, err := ParseDriftfile(p); err != nil {
		t.Fatalf("an undeclared queue must not fail the parse — the trigger still fires: %v", err)
	}
}

// The control: the same manifest with the queue declared parses clean. Both
// spellings of a queue entry are accepted by the schema, so both are pinned —
// without this the test above passes for a check that refuses every trigger.
func TestParse_AcceptsADeclaredQueueInEitherSpelling(t *testing.T) {
	for _, decl := range []string{
		"  queues:\n    - orders\n",
		"  queues:\n    - name: orders\n",
	} {
		p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: orders
      method: queue
      handler: HandleOrder
backbone:
`+decl)
		if _, err := ParseDriftfile(p); err != nil {
			t.Errorf("a declared queue must parse clean, spelling %q gave: %v", decl, err)
		}
	}
}

// The other control: an HTTP function is not a queue consumer, so a manifest
// with no queues at all is fine. Without this the check could refuse every
// project that declares no `backbone.queues`.
func TestParse_AnHTTPFunctionNeedsNoQueue(t *testing.T) {
	p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: ping
      method: get
      handler: GetPing
`)
	if _, err := ParseDriftfile(p); err != nil {
		t.Errorf("an HTTP function declares no queue and must parse clean, got: %v", err)
	}
}

// The check is document-internal and must not reach the network — that is what
// keeps `drift file lint` usable as an offline CI gate. Pointing the CLI at a
// dead host proves the parse never dials one.
func TestParse_TheQueueCheckMakesNoNetworkCall(t *testing.T) {
	previous := common.APIBaseURL
	common.APIBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { common.APIBaseURL = previous })
	t.Setenv("HOME", t.TempDir())

	p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: orders
      method: queue
      handler: HandleOrder
backbone:
  queues:
    - orders
`)
	if _, err := ParseDriftfile(p); err != nil {
		t.Errorf("the parse dialled something, or refused for another reason: %v", err)
	}
}
