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
	m := manifestFrom(Node{"slice": "demo", "atomic": map[string]any{"pulses": []any{
		map[string]any{"name": "queue:orders", "handler": "HandleOrder"},
	}}})

	warnings := checkQueueReferences(m)
	if len(warnings) != 1 {
		t.Fatalf("an undeclared queue must be reported once, got %d: %v", len(warnings), warnings)
	}
	// The pulse as the file labels it, and the block that declares the queue.
	for _, want := range []string{`pulse "QUEUE orders"`, "`queue \"orders\" {}`"} {
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
	withRealSchema(t)
	p := writeManifest(t, `slice "demo" { billing = "monthly" }

element "default" {
  pulse "QUEUE orders" {
    handler = "HandleOrder"
    memory = "16MiB"
  }
}
`)
	m, err := ParseDriftfile(p)
	if err != nil {
		t.Fatalf("an undeclared queue must not fail the parse — the trigger still fires: %v", err)
	}
	if w := checkQueueReferences(m); len(w) != 1 {
		t.Errorf("the parse accepted it, and the gap must still be reported once, got %v", w)
	}
}

// The control: the same manifest with the queue declared parses clean, and the
// reference check has nothing to report. Without this the test above passes for
// a check that refuses every trigger.
func TestParse_AcceptsADeclaredQueue(t *testing.T) {
	withRealSchema(t)
	p := writeManifest(t, `slice "demo" { billing = "monthly" }

element "default" {
  pulse "QUEUE orders" {
    handler = "HandleOrder"
    memory = "16MiB"
  }
}

queue "orders" {}
`)
	m, err := ParseDriftfile(p)
	if err != nil {
		t.Fatalf("a declared queue must parse clean, got: %v", err)
	}
	if w := checkQueueReferences(m); len(w) != 0 {
		t.Errorf("a declared queue was reported as undeclared: %v", w)
	}
}

// The other control: an HTTP function is not a queue consumer, so a manifest
// with no queues at all is fine. Without this the check could refuse every
// project that declares no `queue` block.
func TestParse_AnHTTPFunctionNeedsNoQueue(t *testing.T) {
	withRealSchema(t)
	p := writeManifest(t, `slice "demo" { billing = "monthly" }

element "default" {
  pulse "GET /ping" {
    handler = "GetPing"
    memory = "16MiB"
  }
}
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
	withRealSchema(t)

	p := writeManifest(t, `slice "demo" { billing = "monthly" }

element "default" {
  pulse "QUEUE orders" {
    handler = "HandleOrder"
    memory = "16MiB"
  }
}

queue "orders" {}
`)
	if _, err := ParseDriftfile(p); err != nil {
		t.Errorf("the parse dialled something, or refused for another reason: %v", err)
	}
}
