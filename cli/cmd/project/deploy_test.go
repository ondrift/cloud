package project

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
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
