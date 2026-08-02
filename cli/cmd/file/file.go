// Package file is `drift file` — working on a Driftfile directly, without a
// slice, a session, or a network round trip.
//
// The Driftfile used to be validated only as a side effect of
// `drift project deploy` / `--plan`. So "is line 9 a typo?" required an account,
// a login and a call to the platform, and the answer arrived as a failed deploy.
// Every question the golden-path run actually hit — `nosql_storage` not being a
// field, `queues:` taking a shape the spec did not, an element being per-language,
// `function_memory` being required in practice — is answerable on the laptop, in
// milliseconds (#CLI-STANDARDUSAGE-RKN51F).
//
// # What it validates against, and the limit of that
//
// `ParseDriftfile` — the same parse and the same 46 validation rules a deploy
// runs, so `lint` gives exactly the answer a deploy would, minus the deploy.
//
// It does NOT validate against the schema the platform serves. The CLI fetches and
// caches that (`common/driftfile.go`) and nothing reads it yet. The consequence is
// worth stating plainly rather than discovering: this validates the CLI's own
// encoding of the format, so a Driftfile using a field the platform has added but
// this CLI has not learned is reported as an error. That limit already governs
// `drift project deploy` — `ParseDriftfile` re-decodes with `KnownFields(true)` —
// so `lint` does not introduce it, it surfaces it earlier. Moving both onto the
// served schema is the follow-up, and it is a real change rather than a tidy-up.
package file

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// GetCmd returns the `drift file` command group.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "file",
		Short:   "Work on a Driftfile without deploying it",
		Long:    "Lint, format, explain or scaffold a Driftfile. Offline — no account, no slice, no network.",
		Example: "  drift file lint\n  drift file explain --env staging\n  drift file fmt --write\n  drift file new",
		GroupID: "project",
	}
	cmd.AddCommand(getLintCmd(), getExplainCmd(), getFmtCmd(), getNewCmd())
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
			m, perr := project.ParseDriftfile(path)
			if perr != nil {
				return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), perr)
			}
			envs := ""
			if n := len(m.Environments); n > 0 {
				envs = fmt.Sprintf(", %d environment(s)", n)
			}
			fmt.Printf("%s %s is valid (project %q%s)\n",
				common.Hint("✓"), shortPath(path), m.Slice.Name, envs)
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
