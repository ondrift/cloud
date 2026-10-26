// Package file is `drift file` — working on a Driftfile directly, without a
// slice, a session, or a network round trip.
//
// "Is line 9 a typo?" is answerable on the laptop, in milliseconds, with no
// account and no login — rather than as a failed deploy.
//
// # What it validates against
//
// The same compile `drift plan` and `drift apply` run, so `lint` gives exactly
// the answer they would, minus the slice. The document the file compiles to is
// held to the JSON Schema the platform serves and this machine has cached.
//
// The cost of that is a dependency: with no schema ever fetched there is nothing
// to validate against, and `lint` refuses rather than printing a ✓ it cannot
// stand behind.
package file

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// GetCmd returns the `drift file` command group: everything you do with a
// Driftfile short of applying it.
//
// The offline verbs (lint, fmt, explain, resolve, new) and the ones that run or
// measure what it declares (run, test, benchmark, stop, logs) live together
// here. Applying the file to its slice is `drift plan` and `drift apply`, at
// the top level, because they are about the whole slice the file declares.
//
// `drift project` still resolves to this group, deprecated — see main.go.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "file",
		Short:   "Work on a Driftfile: lint it, run what it declares locally, measure it",
		Long:    "Everything a Driftfile does short of applying it: lint, format, explain, resolve and scaffold offline; run, test, stop and tail it locally; benchmark it against its slice. `drift plan` and `drift apply` apply it.",
		Example: "  drift file lint\n  drift file run\n  drift file explain function\n  drift file logs",
		GroupID: "project",
	}
	cmd.AddCommand(getLintCmd(), getExplainCmd(), getResolveCmd(), getFmtCmd(), getNewCmd())
	cmd.AddCommand(project.Verbs()...)
	return cmd
}

// resolvePath turns an optional positional argument into a Driftfile path.
// A directory argument means "the Driftfile in it", because `drift file lint .`
// is what people type.
func resolvePath(args []string) (string, error) {
	p := "Driftfile"
	if len(args) > 0 && args[0] != "" {
		p = args[0]
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if st, serr := os.Stat(abs); serr == nil && st.IsDir() {
		abs = filepath.Join(abs, "Driftfile")
	}
	if _, serr := os.Stat(abs); serr != nil {
		// NAME THE COMMAND THAT WRITES ONE. In a fresh directory a new account
		// lands here, so this message is the first wall of the product, and it
		// used to end without a next step. Someone who has never seen a
		// Driftfile cannot act on "pass a path".
		return "", fmt.Errorf("no Driftfile at %s\n"+
			"  run `drift file new` to write one, or pass the path to an existing file", abs)
	}
	return abs, nil
}

// ─── lint ───────────────────────────────────────────────────────────────────

func getLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint [path]",
		Short: "Validate a Driftfile and exit non-zero if it is wrong",
		Long: "Compiles and validates a Driftfile exactly as `drift plan` would, without needing " +
			"an account, a network or the values of its secrets. Exits non-zero on an invalid " +
			"file, so it can gate a CI job.",
		Args: cobra.MaximumNArgs(1),
		// SilenceUsage: a validation failure is the user's Driftfile being wrong,
		// not them holding the command wrong, and printing the usage block after a
		// list of field errors buries the thing they need to read.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			// Refuse rather than report a pass we cannot stand behind. The platform
			// owns the format and this machine has never fetched it, so the only
			// honest answer is "I don't know" — and `lint` printing ✓ after
			// validating nothing is precisely the false green a CI gate must never
			// give (#CLI-STANDARDUSAGE-ERF1CV).
			if !project.SchemaAvailable() {
				return project.ErrNoSchema
			}

			// The format this binary implements against the one this machine
			// holds, BEFORE the parse. A major skew makes every message the parse
			// could print a description of the symptom — a list of keys the
			// document is not allowed to carry — with nothing naming the cause.
			//
			// The comparison is against the cached copy, refreshed by whatever
			// online command ran last. A copy OLDER than the format this binary
			// implements is the one case that reaches the network: it is refreshed
			// once, because every key added since would otherwise be refused with
			// nothing naming the cause, and offline the warning says what to run.
			// A current copy is linted with no network at all, which is what a CI
			// gate has to hand.
			var stderr io.Writer = os.Stderr
			if cmd != nil {
				stderr = cmd.ErrOrStderr()
			}
			if err := common.ReportDriftfileFormatSkew(stderr); err != nil {
				return err
			}

			c, perr := project.CompileDriftfile(path)
			if perr != nil {
				return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), perr)
			}
			m := c.Manifest

			// The second half of "exactly as `drift plan` would": every declared
			// handler has to resolve to a callable in its backend.
			//
			// The schema cannot ask this — it validates a DOCUMENT, and whether
			// `handler = "PostUsers"` is really in its backend is a question only
			// this machine can answer. Without it a Driftfile lints green and then
			// fails at apply, which is the one thing a CI gate exists to prevent.
			//
			// Skipped when the source tree is absent rather than reported: a
			// Driftfile is legitimately linted on its own — a manifest handed over
			// for review, or one sitting in a directory whose source has not been
			// checked out — and calling that invalid would refuse a file that is fine.
			if _, serr := os.Stat(m.ResolvePath("atomic")); serr == nil {
				els, berr := atomic_cmd.BuildBackends(project.AtomicSpecs(m))
				if berr != nil {
					return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), berr)
				}
				// The third half: a booking the compiler accepts but the slot's
				// LANGUAGE does not. Only reachable once the backends are built,
				// because that is where the language was detected — the file
				// never says it.
				if cerr := project.CheckCompiledBookings(c, els); cerr != nil {
					return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), cerr)
				}
			}

			envs := ""
			if n := len(c.Environments); n > 0 {
				envs = fmt.Sprintf(", %d environment(s)", n)
			}
			fmt.Printf("%s %s is valid (slice %q%s)\n",
				common.Hint("✓"), shortPath(path), m.Name(), envs)
			return nil
		},
	}
}

// shortPath prints a path relative to the working directory when that is
// shorter, so the common case reads `Driftfile` rather than a home-dir prefix.
func shortPath(abs string) string {
	wd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || len(rel) >= len(abs) {
		return abs
	}
	return rel
}
