package project

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// A phase that always succeeds, echoing a line so runTriad's ordering is
// checkable.
func okPhase(label string) func(*Manifest, io.Writer) error {
	return func(_ *Manifest, out io.Writer) error {
		fmt.Fprintf(out, "%s ok\n", label)
		return nil
	}
}

func failPhase(msg string) func(*Manifest, io.Writer) error {
	return func(_ *Manifest, _ io.Writer) error {
		return errors.New(msg)
	}
}

// The common case: exactly one phase fails, and its own error is returned
// unwrapped — no name prefix a single failure does not need, and no change
// for every caller that only ever saw one phase fail at a time.
func TestRunTriad_OnePhaseFailingReturnsItsOwnError(t *testing.T) {
	err := runTriad(nil, "", []triadPhase{
		{"Atomic", okPhase("atomic")},
		{"Backbone", failPhase("no scope for secret:write")},
		{"Canvas", okPhase("canvas")},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "no scope for secret:write" {
		t.Errorf("got %q, want the failing phase's own error unwrapped", err.Error())
	}
}

// The bug: a token scoped for exactly one phase's job fails ONLY that phase,
// while the request as a whole hits it alongside another independent
// failure (e.g. the slice reads fine but Canvas's upload also fails on its
// own terms) — both must be named, not just whichever phase's goroutine
// happened to be checked first (ABN-03/04).
func TestRunTriad_MultiplePhasesFailingNamesEachOne(t *testing.T) {
	err := runTriad(nil, "", []triadPhase{
		{"Atomic", okPhase("atomic")},
		{"Backbone", failPhase("no scope for secret:write")},
		{"Canvas", failPhase("upload too large")},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"Backbone: no scope for secret:write", "Canvas: upload too large"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name every failed phase; missing %q in: %v", want, err)
		}
	}
}

// The control: nothing failing is nothing to report.
func TestRunTriad_NoFailuresReturnsNil(t *testing.T) {
	err := runTriad(nil, "", []triadPhase{
		{"Atomic", okPhase("atomic")},
		{"Backbone", okPhase("backbone")},
		{"Canvas", okPhase("canvas")},
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// splitDeployedByDigest must sort a currently-deployed function into
// exactly one of its two maps, never both and never neither — a function
// present in both would read as simultaneously skippable and warned-about.
func TestSplitDeployedByDigest_SeparatesByWhetherADigestWasRecorded(t *testing.T) {
	deployed, noDigest := splitDeployedByDigest([]atomic_cmd.DeployedFunction{
		{Key: "get:greet", Name: "greet", Method: "get", Digest: "abc123"},
		{Key: "post:reset", Name: "reset", Method: "post", Digest: ""},
	})

	if deployed["get:greet"] != "abc123" {
		t.Errorf("a function with a recorded digest must be in the digest map, got %q", deployed["get:greet"])
	}
	if noDigest["get:greet"] {
		t.Error("a function with a recorded digest must not also be in noDigest")
	}

	if !noDigest["post:reset"] {
		t.Error("a function with no recorded digest — rolled back, restored, or deployed by an older CLI — must be in noDigest")
	}
	if _, ok := deployed["post:reset"]; ok {
		t.Error("a function with no recorded digest must not be in the digest map")
	}
}

// functionsRedeployingWithNoDigestOnFile is the guard against ABN-22: a
// deliberate `drift atomic rollback` clears the deployed digest by design
// (see routes/atomic_artifact.go), so the very next `drift file apply` —
// for any reason, unrelated to the rolled-back function — redeploys it from
// local source and silently undoes the rollback. This is what makes that
// visible before it happens.
func TestFunctionsRedeployingWithNoDigestOnFile_OnlyNamesFunctionsThatAreActuallyLive(t *testing.T) {
	el := atomic_cmd.Element{
		Name: "default",
		Funcs: []atomic_cmd.ElementFunc{
			{Spec: atomic_cmd.FunctionSpec{Name: "get:rolledback"}},
			{Spec: atomic_cmd.FunctionSpec{Name: "post:changed"}},
			{Spec: atomic_cmd.FunctionSpec{Name: "post:brandnew"}},
		},
	}

	// "get:rolledback" is live with no digest (a rollback, most likely).
	// "post:changed" is live WITH a digest (a genuine, unrelated source
	// change — not what this warning is about). "post:brandnew" has never
	// been deployed at all and so appears in neither map.
	noDigest := map[string]bool{"get:rolledback": true}

	got := functionsRedeployingWithNoDigestOnFile(el, noDigest)
	if len(got) != 1 || got[0] != "get:rolledback" {
		t.Errorf("want exactly [\"get:rolledback\"] named, got %v", got)
	}
}
