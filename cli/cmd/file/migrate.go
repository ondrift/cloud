package file

// migrate.go: `drift file migrate`, bringing a Driftfile in line with the
// current spelling of the format.
//
// `drift file apply` already reads every retired key as its replacement, and
// says so on every run. This is the command that notice names: it rewrites the
// file once, so the notice stops. The rewriting is cmd/project's, beside the
// walker it has to agree with (project.MigrateDriftfile); this file is the
// verb around it.
//
// # Shown, then written
//
// Without --write it prints a unified diff and writes nothing. It exits 0
// either way: this CLI has one failure exit, 1, for a command that could not do
// what it was asked, and a file still written in a legal older spelling is not
// that. Printing the change is the whole of what was asked, as with `drift file
// fmt` without --write.
//
// # Validated on both sides
//
// The file is validated against the platform's schema before it is touched,
// exactly as `drift file lint` validates it, and so is the rewritten file
// before it is shown or written. Like lint, it refuses when this machine holds
// no schema rather than claiming a result it has not checked.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getMigrateCmd() *cobra.Command {
	var write bool
	cmd := &cobra.Command{
		Use:   "migrate [path]",
		Short: "Rewrite a Driftfile's retired keys into the current spelling",
		Long: "Shows, as a diff, the changes that bring a Driftfile in line with the current format: " +
			"every deprecated key moved to its replacement, at the top level and inside every " +
			"environment. Nothing is written unless --write is passed.\n\n" +
			"Only the retired keys change. Comments, key order, blank lines and formatting everywhere " +
			"else stay exactly as they are, and comments on a moved key move with it. A pulse or field " +
			"tagged with `element:` moves into that element under `atomic.elements`, with the comments " +
			"above it, and loses the tag. A key that has " +
			"no replacement in this file is left where it is and reported with what to do instead.\n\n" +
			"Exits 0 when the file is already current, when it shows the changes, and when it writes " +
			"them; non-zero only when it cannot rewrite the file, which it then leaves untouched.",
		Example:      "  drift file migrate\n  drift file migrate --write\n  drift file migrate ./api/Driftfile",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			return runMigrate(cmd.OutOrStdout(), cmd.ErrOrStderr(), path, write)
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write the changes to the file instead of showing them")
	return cmd
}

// runMigrate is the command with its streams and path supplied, so a test
// drives exactly what a person runs.
//
// The diff and the result go to out; what is reported about the file (each
// change in words, the keys left in place) goes to errOut, so the diff on its
// own can be redirected into a patch.
func runMigrate(out, errOut io.Writer, path string, write bool) error {
	name := shortPath(path)

	// The same two gates lint has, for the same reasons: no schema means no
	// claim about validity, and a format skew is named before any message it
	// would otherwise disguise.
	if !project.SchemaAvailable() {
		return project.ErrNoSchema
	}
	if err := common.ReportDriftfileFormatSkew(errOut); err != nil {
		return err
	}

	src, err := os.ReadFile(path) // #nosec G304: the user named this file
	if err != nil {
		return err
	}
	if verr := project.ValidateDriftfileSource(src); verr != nil {
		return fmt.Errorf("%s\n\n%w\n\nmigrate rewrites a Driftfile the format accepts; fix the above first "+
			"(`drift file lint` reports the same)", common.Hint(name), verr)
	}

	mig, err := project.MigrateDriftfile(src)
	if err != nil {
		return fmt.Errorf("%s\n\n%w", common.Hint(name), err)
	}

	if !mig.Changed() {
		if len(mig.Kept) == 0 {
			fmt.Fprintf(out, "%s %s is current; nothing to change\n", common.Hint("✓"), name)
		} else {
			fmt.Fprintf(out, "%s %s has no key migrate can rewrite; nothing to change\n", common.Hint("✓"), name)
		}
		reportKept(errOut, mig.Kept)
		return nil
	}

	if verr := project.ValidateDriftfileSource(mig.Output); verr != nil {
		return fmt.Errorf("the rewritten %s does not validate, so nothing was written; this is a bug in "+
			"drift file migrate:\n\n%w", name, verr)
	}

	if !write {
		patchName := strings.TrimPrefix(filepath.ToSlash(name), "/")
		fmt.Fprint(out, unifiedDiff("a/"+patchName, "b/"+patchName, src, mig.Output))
		fmt.Fprintf(errOut, "%s %s to rewrite in %s:\n", common.Hint("·"), plural(len(mig.Steps), "key", "keys"), name)
		reportSteps(errOut, mig.Steps)
		reportKept(errOut, mig.Kept)
		fmt.Fprintf(errOut, "%s nothing written; run `drift file migrate --write` to make these changes\n",
			common.Hint("·"))
		return nil
	}

	// 0o600 for a file this creates, as fmt writes it; an existing file keeps
	// its own mode, because WriteFile only sets one on creation.
	if werr := os.WriteFile(path, mig.Output, 0o600); werr != nil {
		return werr
	}
	fmt.Fprintf(out, "%s %s updated: %s\n", common.Hint("✓"), name, plural(len(mig.Steps), "key", "keys")+" rewritten")
	reportSteps(out, mig.Steps)
	reportKept(errOut, mig.Kept)
	return nil
}

// reportSteps lists each rewritten key with the line it was on.
func reportSteps(w io.Writer, steps []project.MigrationStep) {
	for _, s := range steps {
		fmt.Fprintf(w, "  line %-4d %s → %s\n", s.Line, s.Old, s.New)
	}
}

// reportKept names every retired key migrate cannot move, and what to do about
// it. Never silent: the person reads that the value is ignored and where it
// is set now, and decides when the line goes.
func reportKept(w io.Writer, kept []project.KeptKey) {
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(w, "%s left in place, because there is nothing in this file to move %s to:\n",
		common.Hint("·"), pronoun(len(kept)))
	for _, k := range kept {
		fmt.Fprintf(w, "  line %-4d %s\n            %s\n", k.Line, k.Path, k.Advice)
	}
	fmt.Fprintf(w, "  `drift file apply` names each of these until its line is deleted.\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
