package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `// drift:schedule` comment declares nothing, and ignoring it silently
// would leave whoever wrote it believing a schedule runs. It is refused,
// naming the Driftfile attribute that declares a schedule, in the spelling the
// file writes it.
func TestRefuseScheduleComments_NamesTheDriftfileAttribute(t *testing.T) {
	for _, c := range []struct{ name, source string }{
		{"go", "package main\n\n// drift:schedule */5 * * * *\nfunc Handler() {}\n"},
		{"python", "# drift:schedule 0 15 * * *\ndef handler():\n    pass\n"},
		{"indented", "package main\n\n\t// drift:schedule 0 3 * * *\nfunc Handler() {}\n"},
	} {
		dir := t.TempDir()
		ext := map[string]string{"go": ".go", "python": ".py", "indented": ".go"}[c.name]
		if err := os.WriteFile(filepath.Join(dir, "h"+ext), []byte(c.source), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		err := refuseScheduleComments(dir)
		if err == nil {
			t.Errorf("%s: a drift:schedule comment must be refused, not ignored", c.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "cron = \"") || strings.Contains(msg, "cron:") {
			t.Errorf("%s: the refusal must name the Driftfile attribute as the file writes it.\ngot: %s", c.name, msg)
		}
		// The expression itself, so the fix is a copy rather than a re-derivation.
		if !strings.Contains(msg, "* * *") {
			t.Errorf("%s: the refusal must quote the expression back.\ngot: %s", c.name, msg)
		}
		if !strings.Contains(msg, "h"+ext) {
			t.Errorf("%s: the refusal must name the file it found it in.\ngot: %s", c.name, msg)
		}
	}
}

// THE CONTROL, and the line this change must not cross. `// drift:trigger` is a
// DIFFERENT mechanism: it is documented in driftfile-spec.md, it is honoured on
// every path, and it binds an extra queue to a function that already has its own
// trigger. Refusing it too would break a supported feature.
func TestRefuseScheduleComments_LeavesTriggerCommentsAlone(t *testing.T) {
	dir := t.TempDir()
	source := "package main\n\n" +
		"// drift:trigger queue orders poll=250ms retry=5\n" +
		"// drift:trigger webhook /hooks/payment\n" +
		"func Handler() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "h.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := refuseScheduleComments(dir); err != nil {
		t.Fatalf("a drift:trigger comment is supported and must not be refused: %v", err)
	}

	// And it still parses into the specs it always did.
	triggers, err := parseTriggerComments(dir)
	if err != nil {
		t.Fatalf("parseTriggerComments: %v", err)
	}
	if len(triggers) != 2 {
		t.Fatalf("got %d triggers, want 2 — the supported comment form stopped working", len(triggers))
	}
}

// Source with no schedule comment deploys as before. Obvious, and the assertion
// that keeps the refusal from becoming a refusal of everything.
func TestRefuseScheduleComments_OrdinarySourcePasses(t *testing.T) {
	dir := t.TempDir()
	source := "package main\n\n// A comment mentioning a schedule in prose, and cron, harmlessly.\nfunc Handler() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "h.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := refuseScheduleComments(dir); err != nil {
		t.Errorf("ordinary source must deploy: %v", err)
	}
}
