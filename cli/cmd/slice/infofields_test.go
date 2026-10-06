package slice

import (
	"errors"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd"
)

// `drift slice info` shows each Field on one line after the pools, says when
// the Fields could not be read rather than printing nothing, and prints nothing
// for a slice running none.
func TestInfo_ShowsTheFields(t *testing.T) {
	var out strings.Builder
	printFieldsSection(&out, []atomic_cmd.FieldStatus{
		{Name: "sequencer", State: "paused", Restarts: 5},
	}, nil)
	if got := out.String(); !strings.Contains(got, "fields") ||
		!strings.Contains(got, "sequencer") || !strings.Contains(got, "paused, 5 restarts") {
		t.Errorf("the Field is not shown with its state and restarts:\n%s", got)
	}

	out.Reset()
	printFieldsSection(&out, nil, errors.New("HTTP 502"))
	if got := out.String(); !strings.Contains(got, "couldn't be read: HTTP 502") {
		t.Errorf("an unreadable Field list must be said, not swallowed:\n%s", got)
	}

	out.Reset()
	printFieldsSection(&out, nil, nil)
	if out.Len() != 0 {
		t.Errorf("a slice running no Field prints nothing about Fields, got %q", out.String())
	}
}
