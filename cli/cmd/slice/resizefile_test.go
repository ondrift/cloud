package slice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE GAP THIS CLOSES, asserted as a property of the command rather than of a
// terminal: there IS a non-interactive resize.
//
// For a long time there was not. The form needs a TTY, so no script, CI job or
// SDK consumer could change a slice's shape at all — and the one knob a
// scheduled job needs is reachable no other way. The refusal named its reason
// ("a resize can reprice a slice or destroy what it holds, and both are answered
// at the form"), and the reason is true of the QUESTIONS and false of the form:
// the platform asks both of every caller and refuses until they are answered.
func TestResizeOffersANonInteractivePath(t *testing.T) {
	cmd := getResizeCmd()

	for _, name := range []string{"dump", "config", "acknowledge-monthly-cents", "confirm"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is missing, so a script cannot resize a slice", name)
		}
	}

	// The help has to show the loop. A flag nobody can find is a flag that does
	// not exist, and this is the command whose absence sent people to a form.
	for _, want := range []string{"--dump", "--config", "shape.json"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("the help does not show %q, so the non-interactive path is undiscoverable", want)
		}
	}
}

// --dump and --config are the two halves of one loop, and asking for both in one
// command is a caller who means one of them.
func TestResizeRefusesDumpAndConfigTogether(t *testing.T) {
	cmd := getResizeCmd()
	if err := cmd.Flags().Set("dump", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("config", "shape.json"); err != nil {
		t.Fatal(err)
	}

	err := cmd.RunE(cmd, []string{"demo"})
	if err == nil {
		t.Fatal("--dump with --config was accepted")
	}
	if !strings.Contains(err.Error(), "--dump") || !strings.Contains(err.Error(), "--config") {
		t.Errorf("the refusal names neither flag: %v", err)
	}
}

// A shape file that is not a shape is refused BEFORE anything is sent, and the
// refusal says how to make one. Applying an empty object would ask the platform
// to take everything away — which it would then refuse, but only after the
// caller had asked for it.
func TestResizeRefusesAnUnusableShapeFile(t *testing.T) {
	dir := t.TempDir()

	notJSON := filepath.Join(dir, "notjson.json")
	if err := os.WriteFile(notJSON, []byte("this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := resizeFromFile("demo", notJSON, 1, -1, "")
	if err == nil {
		t.Fatal("a file that is not JSON was accepted")
	}
	if !strings.Contains(err.Error(), "--dump") {
		t.Errorf("the refusal does not say how to produce a shape: %v", err)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = resizeFromFile("demo", empty, 1, -1, "")
	if err == nil {
		t.Fatal("an empty shape was accepted — applying it asks for everything to be removed")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the refusal does not say the shape is empty: %v", err)
	}

	missing := filepath.Join(dir, "nope.json")
	if err := resizeFromFile("demo", missing, 1, -1, ""); err == nil {
		t.Fatal("a missing file was accepted")
	}
}

// describeDestroy renders whatever the platform sent. A destroy the CLI could
// not describe used to print as an empty line, which reads as "nothing" beside a
// warning that something is being removed.
func TestDescribeDestroyAlwaysSaysSomething(t *testing.T) {
	for name, in := range map[string]map[string]string{
		"resource and name": {"resource": "collection", "name": "events"},
		"name only":         {"name": "events"},
		"an unknown shape":  {"surprise": "value"},
		"empty":             {},
	} {
		got := describeDestroy(in)
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s rendered as nothing, beside a warning that it is being destroyed", name)
		}
	}
	if got := describeDestroy(map[string]string{"resource": "collection", "name": "events"}); got != "collection events" {
		t.Errorf("want %q, got %q", "collection events", got)
	}
}
