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

			// THE ACTIVE SLICE FIRST, and the directory name only as a fallback.
			//
			// A Driftfile names the slice it deploys INTO — it does not mint one.
			// Defaulting to the directory wrote a file naming a slice that does not
			// exist, so the two commands a new account is told to run stopped
			// composing: `drift slice create hello` followed by `drift file new` in
			// ~/work/myapp writes `myapp`, and `drift file apply` then answers
			// "slice myapp does not exist — create it first". Following THAT advice
			// hits "only one free hacker slice is allowed per account", and nothing
			// printed anywhere names the way out.
			//
			// Reproduce it by reverting this block: create a slice, then scaffold in
			// a directory named anything else.
			chosen := "the directory name"
			if name == "" {
				if active := common.GetActiveSlice(); active != "" && nameLooksValid(active) {
					name, chosen = active, "your active slice"
				} else {
					name = filepath.Base(filepath.Dir(abs))
				}
			} else {
				chosen = "--name"
			}
			if !nameLooksValid(name) {
				return fmt.Errorf("project name %q must be 1–32 lowercase letters, numbers or hyphens "+
					"(not starting or ending with one) — pass --name", name)
			}

			if werr := os.WriteFile(abs, []byte(starterDriftfile(name, canvas)), 0o600); werr != nil {
				return werr
			}
			// Say WHICH slice, and WHY. The name decides where a deploy lands, and
			// a scaffolder that picks one silently is how the mismatch above went
			// unnoticed until apply refused it.
			fmt.Printf("%s wrote %s — slice %q, from %s\n",
				common.Hint("✓"), shortPath(abs), name, chosen)
			if chosen == "the directory name" {
				fmt.Printf("  %s\n", common.Hint("no active slice — run `drift slice use <name>` and re-run, or edit `slice:` by hand"))
			}
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
	b.WriteString("slice: " + name + "\n\n")

	if canvasDir != "" {
		b.WriteString("# Static site. The short form is a bare path.\n")
		b.WriteString("canvas: " + canvasDir + "\n\n")
	}

	b.WriteString("atomic:\n")
	b.WriteString("  # Every function you run. This list is the whole declaration — what the\n")
	b.WriteString("  # slice exposes, what serves it, and what guards it. Nothing in your\n")
	b.WriteString("  # source declares a function; the code is just code.\n")
	b.WriteString("  #\n")
	b.WriteString("  #   route    the path it answers on, without a leading slash: `ping`,\n")
	b.WriteString("  #            `auth/challenge`, `groups/:id`\n")
	b.WriteString("  #   method   get, post, put, patch, delete, head, options — or `queue`\n")
	b.WriteString("  #            for one the slice invokes from a Backbone queue\n")
	b.WriteString("  #   handler  the callable in your source, found in the element's folder\n")
	b.WriteString("  #   auth     `none` (the default) or `apikey`\n")
	b.WriteString("  #   secrets  the Backbone secrets this one function may read\n")
	b.WriteString("  #\n")
	b.WriteString("  # Memory is a SLICE setting, chosen on the form `drift slice resize`\n")
	b.WriteString("  # draws — not a key in this file. `drift file benchmark` reports what\n")
	b.WriteString("  # each function has actually cost.\n")
	b.WriteString("  functions:\n")
	b.WriteString("    - route: hello\n")
	b.WriteString("      method: get\n")
	b.WriteString("      handler: GetHello\n\n")

	b.WriteString("# Per-environment overrides. Anything set here replaces the base for that\n")
	b.WriteString("# environment — including a 0 or a false, which is the point of the block.\n")
	b.WriteString("# `prod` (or `production`) deploys into the slice named above; any other\n")
	b.WriteString("# environment deploys into <slice>-<environment>, which must already exist.\n")
	b.WriteString("environments:\n")
	b.WriteString("  prod: {}\n")
	return b.String()
}
