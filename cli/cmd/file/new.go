package file

// new.go — scaffold a Driftfile that deploys.
//
// The starter file fills in the knobs that are required IN PRACTICE rather than
// the minimum the format allows, because the gap between those two is where the
// golden-path run kept stalling.
//
// Every function now declares its own `memory` and nothing is defaulted, so the
// scaffold ships one complete function rather than an empty list: `functions: []`
// is technically valid and teaches nothing, and a user's first edit should be
// changing a real entry rather than inventing the shape of one.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getNewCmd() *cobra.Command {
	var (
		name   string
		canvas string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "new [path]",
		Short: "Write a starter Driftfile",
		Long: "Creates a Driftfile with the knobs that are required in practice already " +
			"filled in, so the first deploy is not a validation error.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "Driftfile"
			if len(args) > 0 && args[0] != "" {
				target = args[0]
			}
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if st, serr := os.Stat(abs); serr == nil && st.IsDir() {
				abs = filepath.Join(abs, "Driftfile")
			}
			if _, serr := os.Stat(abs); serr == nil && !force {
				return fmt.Errorf("%s already exists — pass --force to overwrite it", shortPath(abs))
			}

			if name == "" {
				// Default to the directory name, which is what the project is
				// called everywhere else the user thinks about it.
				name = filepath.Base(filepath.Dir(abs))
			}
			if !nameLooksValid(name) {
				return fmt.Errorf("project name %q must be 1–32 lowercase letters, numbers or hyphens "+
					"(not starting or ending with one) — pass --name", name)
			}

			if werr := os.WriteFile(abs, []byte(starterDriftfile(name, canvas)), 0o600); werr != nil {
				return werr
			}
			fmt.Printf("%s wrote %s\n", common.Hint("✓"), shortPath(abs))
			fmt.Printf("  %s\n", common.Hint("drift file lint    # check it"))
			fmt.Printf("  %s\n", common.Hint("drift file explain # see what it provisions"))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (default: the directory name)")
	cmd.Flags().StringVar(&canvas, "canvas", "", "path to a static site directory to serve")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing Driftfile")
	return cmd
}

// nameLooksValid mirrors the platform's identifier rule. It is a local copy for
// a fast message and nothing more — the parser and the server both enforce it
// again, so this being out of step costs a confusing hint, never a bad deploy.
func nameLooksValid(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

func starterDriftfile(name, canvasDir string) string {
	var b strings.Builder
	b.WriteString("# " + name + " — a Drift project.\n")
	b.WriteString("# Every knob below is optional unless noted. `drift file explain` shows\n")
	b.WriteString("# what this resolves to; `drift file lint` checks it without deploying.\n\n")
	b.WriteString("name: " + name + "\n\n")

	if canvasDir != "" {
		b.WriteString("# Static site. The short form is a bare path.\n")
		b.WriteString("canvas: " + canvasDir + "\n\n")
	}

	b.WriteString("atomic:\n")
	b.WriteString("  # Every function you run, each with the memory it books. Both fields are\n")
	b.WriteString("  # required and neither has a default: the figure is that function's own\n")
	b.WriteString("  # pool — its concurrent invocations together stay inside it — and it is\n")
	b.WriteString("  # what the function is billed at.\n")
	b.WriteString("  #\n")
	b.WriteString("  # The name is the route it answers on: `get:ping`, `post:auth/login`.\n")
	b.WriteString("  #\n")
	b.WriteString("  # Size it from what the function actually costs rather than guessing —\n")
	b.WriteString("  # `drift project benchmark` measures it, and booking more than you need\n")
	b.WriteString("  # is paid for, while booking less means invocations are refused under\n")
	b.WriteString("  # load. 8MB–256MB.\n")
	b.WriteString("  functions:\n")
	b.WriteString("    - { name: \"get:hello\", memory: 32MB }\n\n")

	b.WriteString("# Per-environment overrides. Anything set here replaces the base for that\n")
	b.WriteString("# environment — including a 0 or a false, which is the point of the block.\n")
	b.WriteString("# `prod` (or `production`) deploys under the bare project name; any other\n")
	b.WriteString("# environment deploys as <name>-<environment>.\n")
	b.WriteString("environments:\n")
	b.WriteString("  prod: {}\n")
	b.WriteString("  staging:\n")
	b.WriteString("    atomic:\n")
	b.WriteString("      # An override is a partial: name only what differs. Restating a\n")
	b.WriteString("      # function here replaces its booking for this environment alone.\n")
	b.WriteString("      functions:\n")
	b.WriteString("        - { name: \"get:hello\", memory: 16MB }\n")
	return b.String()
}
