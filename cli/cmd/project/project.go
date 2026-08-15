package project

import "github.com/spf13/cobra"

// Verbs returns the commands that act on the project a Driftfile describes:
// applying it to a slice, simulating that, and running, testing, measuring,
// stopping or tailing what it declares.
//
// They are registered under `drift file`, because the Driftfile IS the project —
// the resources and their sizes are the whole declaration, so there is one noun
// to learn rather than two that address the same thing.
//
// Vocabulary the platform uses:
//   - Primitives — Atomic, Backbone, Canvas (the *what*).
//   - Slice      — the rented infrastructure that holds primitives (the *where*).
//   - Driftfile  — primitives + slice bundled as one unit (the *what + where*).
func Verbs() []*cobra.Command {
	return []*cobra.Command{
		getApplyCmd(),
		getSimulateCmd(),
		getBenchmarkCmd(),
		getRunCmd(),
		getTestCmd(),
		getStopCmd(),
		getLogsCmd(),
	}
}
