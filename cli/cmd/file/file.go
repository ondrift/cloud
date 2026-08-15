// Package file is `drift file` — working on a Driftfile directly, without a
// slice, a session, or a network round trip.
//
// The Driftfile used to be validated only as a side effect of
// `drift file apply` / `--plan`. So "is line 9 a typo?" required an account,
// a login and a call to the platform, and the answer arrived as a failed deploy.
// Every question the golden-path run actually hit — `nosql_storage` not being a
// field, `queues:` taking a shape the spec did not, an element being per-language,
// `function_memory` being required in practice — is answerable on the laptop, in
// milliseconds (#CLI-STANDARDUSAGE-RKN51F).
//
// # What it validates against
//
// `ParseDriftfile` — the same parse a deploy runs, so `lint` gives exactly the
// answer a deploy would, minus the deploy. And that parse now has exactly one
// authority: the JSON Schema the platform serves and this machine has cached
// (#CLI-STANDARDUSAGE-ERF1CV). The CLI holds no rules of its own to disagree
// with it, so a field the platform has added is accepted here the moment the
// cached schema is refreshed, without a CLI release.
//
// The cost of that is a dependency: with no schema ever fetched there is nothing
// to validate against, and `lint` refuses rather than printing a ✓ it cannot
// stand behind.
package file

import (
	"fmt"
	"os"
	"path/filepath"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// GetCmd returns the `drift file` command group — everything you do with a
// Driftfile, whether or not it touches a slice.
//
// One noun, because the Driftfile IS the project: the resources and their sizes
// are the whole declaration, so `project` and `file` addressed the same thing
// under two names and cost a user the choice between them. The offline verbs
// (lint, fmt, explain, new) and the ones that reach a slice (apply, simulate,
// run, test, benchmark, stop, logs) live together for that reason.
//
// `drift project` still resolves to this group, deprecated — see main.go.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "file",
		Short:   "Work on a Driftfile — lint it, apply it, run what it declares",
		Long:    "Everything a Driftfile does: lint, format, explain and scaffold offline; apply, simulate, run, test, benchmark, stop and tail against a slice.",
		Example: "  drift file lint\n  drift file apply\n  drift file simulate\n  drift file logs",
		GroupID: "project",
	}
	cmd.AddCommand(getLintCmd(), getExplainCmd(), getFmtCmd(), getNewCmd())
	cmd.AddCommand(project.Verbs()...)

	// The two verbs that changed spelling in the move. Each keeps working and
	// says once what to type instead. No RemoveAfter, for the reason in main.go:
	// a version named here is a promise about a release nobody has planned.
	apply, simulate := findVerb(cmd, "apply"), findVerb(cmd, "simulate")
	cmd.AddCommand(
		common.AliasCommand(apply, "deploy", common.Deprecation{
			Old: "drift file deploy", New: "drift file apply",
		}),
		common.AliasCommand(simulate, "diff", common.Deprecation{
			Old: "drift file diff", New: "drift file simulate",
		}),
	)
	return cmd
}

// findVerb returns a registered subcommand by name, panicking if it is absent.
//
// Panics rather than returning an error: this runs at command-tree construction
// with a name written three lines above, so a miss is a typo in this file and
// not a condition any user can reach. A nil alias target would instead fail at
// the moment someone typed the old spelling.
func findVerb(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	panic("file: no subcommand named " + name)
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
		return "", fmt.Errorf("no Driftfile at %s — pass a path, or run this from a project directory", abs)
	}
	return abs, nil
}

// ─── lint ───────────────────────────────────────────────────────────────────

func getLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint [path]",
		Short: "Validate a Driftfile and exit non-zero if it is wrong",
		Long: "Parses and validates a Driftfile exactly as a deploy would, without needing " +
			"an account or a network. Exits non-zero on the first invalid file, so it can " +
			"gate a CI job.",
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
			m, perr := project.ParseDriftfile(path)
			if perr != nil {
				return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), perr)
			}

			// The second half of "exactly as a deploy would": every declared
			// handler has to resolve to a callable in its element.
			//
			// The schema cannot ask this — it validates a DOCUMENT, and whether
			// `handler: PostUsers` is really in ./atomic is a question only this
			// machine can answer. Without it a Driftfile lints green and then fails
			// at deploy, which is the one thing a CI gate exists to prevent.
			//
			// Skipped when the source tree is absent rather than reported: a
			// Driftfile is legitimately linted on its own (a manifest handed over
			// for review, `drift slice create --from Driftfile` in an empty
			// directory), and calling that invalid would refuse a file that is fine.
			if _, serr := os.Stat(m.ResolvePath("atomic")); serr == nil {
				els, berr := atomic_cmd.BuildElements(project.FunctionSpecs(m))
				if berr != nil {
					return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), berr)
				}
				// The third half: a booking the schema accepts but the function's
				// LANGUAGE does not. Only reachable once the elements are built,
				// because that is where the language was detected — the document
				// never says it.
				if cerr := project.CheckCompiledBookings(m, els); cerr != nil {
					return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), cerr)
				}
			}

			envs := ""
			if n := len(m.EnvironmentNames()); n > 0 {
				envs = fmt.Sprintf(", %d environment(s)", n)
			}
			fmt.Printf("%s %s is valid (project %q%s)\n",
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
