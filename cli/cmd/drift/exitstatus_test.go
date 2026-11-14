package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// commandReturning is a root command whose run function returns err.
func commandReturning(err error) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "drift",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return err },
	}
	cmd.SetArgs([]string{})
	return cmd
}

// The process exits with the status the command's error carries and with 1 for
// any other error, and a failure is said once, on stderr, in the error's own
// words.
func TestRun_ExitsWithTheStatusTheCommandsErrorCarries(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		status int
		stderr string
	}{
		{"no error", nil, 0, ""},
		{"a failure", errors.New("Couldn't list slices."), 1, "Couldn't list slices.\n"},
		{
			"a slice that differs",
			&common.ExitStatusError{Status: 2, Reason: "the slice differs from the Driftfile"},
			2, "the slice differs from the Driftfile\n",
		},
		{
			"a slice that could not be read",
			&common.ExitStatusError{Status: 3, Reason: "part of the slice could not be read"},
			3, "part of the slice could not be read\n",
		},
		{
			"a status inside a wrapping error",
			fmt.Errorf("plan: %w", &common.ExitStatusError{Status: 3, Reason: "part of the slice could not be read"}),
			3, "plan: part of the slice could not be read\n",
		},
		// A status of 0 on an error would tell the shell the command worked.
		{
			"an error that carries 0",
			&common.ExitStatusError{Status: 0, Reason: "the slice differs from the Driftfile"},
			1, "the slice differs from the Driftfile\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := run(commandReturning(c.err), &stderr); got != c.status {
				t.Errorf("a command that returns %v exits %d, want %d", c.err, got, c.status)
			}
			if got := stderr.String(); got != c.stderr {
				t.Errorf("stderr is %q, want %q", got, c.stderr)
			}
		})
	}
}
