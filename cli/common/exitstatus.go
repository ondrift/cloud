package common

// exitstatus.go — an error that says which status the process exits with.
//
// A command returns its error and main exits 1 for it. A command whose answer is
// not a failure but is one a script branches on (`drift plan --exit-code` finding
// a difference) returns an ExitStatusError instead, and main exits with the
// status it carries. Only this type is honoured: another package's error that
// happens to have an ExitCode method, os/exec's for a program drift ran, carries
// that program's status, which drift does not pass on as its own.

import "errors"

// ExitStatusError is an error that carries the status the process exits with.
type ExitStatusError struct {
	// Status is what the process exits with. A status below 1 is no status: an
	// error never exits 0.
	Status int
	// Reason is what main says on stderr, in words.
	Reason string
}

// Error is the reason, so what a person reads is why and not a number.
func (e *ExitStatusError) Error() string { return e.Reason }

// ExitCode is the status the error carries, the method os/exec's error has.
func (e *ExitStatusError) ExitCode() int { return e.Status }

// ExitStatus is what the process exits with for the error a command returned: 0
// for none, the status an ExitStatusError carries, found through any wrapping, and
// 1 for every other error.
func ExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var carried *ExitStatusError
	if errors.As(err, &carried) && carried.Status > 0 {
		return carried.Status
	}
	return 1
}
