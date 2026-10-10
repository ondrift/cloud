package project

import "github.com/spf13/cobra"

// Verbs returns the `drift file` commands that act on the project a Driftfile
// describes: running, testing, measuring, stopping or tailing what it declares.
// Applying it to a slice is `drift apply`, at the top level, because it is about
// the whole slice the file declares.
//
// Vocabulary the platform uses:
//   - Primitives — Atomic, Backbone, Canvas (the *what*).
//   - Slice      — the rented infrastructure that holds primitives (the *where*).
//   - Driftfile  — primitives + slice bundled as one unit (the *what + where*).
func Verbs() []*cobra.Command {
	return []*cobra.Command{
		getBenchmarkCmd(),
		getRunCmd(),
		getTestCmd(),
		getStopCmd(),
		getLogsCmd(),
	}
}
