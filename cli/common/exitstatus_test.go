package common

import (
	"errors"
	"fmt"
	"testing"
)

// anotherExitCode is an error of some other package that carries an ExitCode, as
// os/exec's does for the program it ran.
type anotherExitCode int

func (e anotherExitCode) Error() string { return "another package's error" }
func (e anotherExitCode) ExitCode() int { return int(e) }

// The status the process exits with is the one drift's own error carries, found
// through any wrapping; every other error is a failure, 1, and no error is 0.
func TestExitStatus_IsWhatTheErrorCarriesAndOneForAnyOther(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"no error", nil, 0},
		{"a plain error", errors.New("no"), 1},
		{"a status of 2", &ExitStatusError{Status: 2, Reason: "it differs"}, 2},
		{"a status of 3 inside a wrapping error", fmt.Errorf("plan: %w", &ExitStatusError{Status: 3, Reason: "part is unread"}), 3},
		// An error that exits 0 would tell the shell the command succeeded.
		{"an error that carries 0", &ExitStatusError{Status: 0, Reason: "it differs"}, 1},
		{"an error that carries a negative status", &ExitStatusError{Status: -1, Reason: "it differs"}, 1},
		// A program that drift ran has its own status, which is not drift's to pass
		// on as its own.
		{"another package's error with an ExitCode", anotherExitCode(7), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitStatus(c.err); got != c.want {
				t.Errorf("ExitStatus(%v) is %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// The error says what it means in words, so the line a person reads on stderr
// is the reason and not a number, and it carries the status as ExitCode, the
// method os/exec's error has.
func TestExitStatusError_SaysItsReasonAndCarriesItsStatus(t *testing.T) {
	err := &ExitStatusError{Status: 3, Reason: "part of the slice could not be read"}
	if got := err.Error(); got != "part of the slice could not be read" {
		t.Errorf("the error says %q, want its reason", got)
	}
	var coded interface{ ExitCode() int } = err
	if got := coded.ExitCode(); got != 3 {
		t.Errorf("the error's ExitCode is %d, want 3", got)
	}
}
