package slice

import (
	"errors"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd"
)

// `drift slice info` shows each Worker on one line after the pools, says when
// the Workers could not be read rather than printing nothing, and prints nothing
// for a slice running none.
func TestInfo_ShowsTheWorkers(t *testing.T) {
	var out strings.Builder
	printWorkersSection(&out, []atomic_cmd.WorkerStatus{
		{Name: "sequencer", State: "paused", Restarts: 5},
	}, nil)
	if got := out.String(); !strings.Contains(got, "workers") ||
		!strings.Contains(got, "sequencer") || !strings.Contains(got, "paused, 5 restarts") {
		t.Errorf("the Worker is not shown with its state and restarts:\n%s", got)
	}

	out.Reset()
	printWorkersSection(&out, nil, errors.New("HTTP 502"))
	if got := out.String(); !strings.Contains(got, "couldn't be read: HTTP 502") {
		t.Errorf("an unreadable Worker list must be said, not swallowed:\n%s", got)
	}

	out.Reset()
	printWorkersSection(&out, nil, nil)
	if out.Len() != 0 {
		t.Errorf("a slice running no Worker prints nothing about Workers, got %q", out.String())
	}
}
