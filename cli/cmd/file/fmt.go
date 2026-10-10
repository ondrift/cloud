package file

// fmt.go — canonical layout, so Driftfile diffs stay readable.
//
// HCL has one canonical layout, and hclwrite writes it: two-space indentation,
// the `=` of neighbouring attributes aligned, and spacing around operators.
// Comments, blank lines and the order of everything stay exactly where the
// person put them, because only whitespace changes. A formatter that reorders
// or drops a comment is worse than none, and it is the kind of loss only
// noticed later, in a diff, by someone else.

import (
	"bytes"
	"fmt"
	"os"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/cobra"
)

func getFmtCmd() *cobra.Command {
	var write bool
	cmd := &cobra.Command{
		Use:   "fmt [path]",
		Short: "Rewrite a Driftfile in the canonical layout",
		Long: "Prints the Driftfile in HCL's canonical layout: two-space indentation and aligned " +
			"`=`. Comments, blank lines and the order of everything are kept; only whitespace " +
			"changes. Use --write to update the file in place.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — the user named this file
			if rerr != nil {
				return rerr
			}
			out, ferr := formatDriftfile(path, src)
			if ferr != nil {
				return fmt.Errorf("%s: %w", shortPath(path), ferr)
			}
			if !write {
				fmt.Print(string(out))
				return nil
			}
			if bytes.Equal(src, out) {
				fmt.Printf("%s %s already formatted\n", common.Hint("·"), shortPath(path))
				return nil
			}
			// 0o600 rather than preserving the original mode: a Driftfile names
			// the variables its secrets are read from and lives beside code, and
			// widening a file's permissions as a side effect of formatting it
			// would be a surprising way to expose one.
			if werr := os.WriteFile(path, out, 0o600); werr != nil {
				return werr
			}
			fmt.Printf("%s %s formatted\n", common.Hint("✓"), shortPath(path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "rewrite the file in place instead of printing it")
	return cmd
}

// formatDriftfile lays the file out canonically. A file that does not parse
// is refused rather than formatted: hclwrite formats tokens, and laying out a
// broken file would move the error the person is about to read.
func formatDriftfile(path string, src []byte) ([]byte, error) {
	if err := project.RefuseYAML(path, src); err != nil {
		return nil, err
	}
	if _, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos); diags.HasErrors() {
		return nil, diags
	}
	return hclwrite.Format(src), nil
}
