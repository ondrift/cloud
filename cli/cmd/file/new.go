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

	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
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
			// hits the free-tier refusal, which at the time read "only one free
			// hacker slice is allowed per account" and named no way out. That
			// message has since been rewritten to say a second slice is allowed and
			// priced; this block is what stops the user meeting it at all.
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
			// NAME THE COMMAND THAT WRITES THE SOURCE, because this file declares
			// a function that does not exist yet.
			//
			// The scaffolded entry is `route: hello, method: get, handler:
			// GetHello`, and nothing on disk defines GetHello — a Driftfile
			// declares functions, it does not create them. So `drift file new`
			// followed by `drift file apply` fails at handler resolution, and the
			// failure names the missing handler rather than the command that would
			// produce it. A new user's second command dead-ends on their first.
			//
			// The command below is not an example: it is the one that writes source
			// matching THIS entry exactly — same route, same method, and
			// `handlerName` derives the same `GetHello` from them. The two halves
			// are generated from `starterFn`, so they cannot drift apart while the
			// test that compares them holds.
			fmt.Printf("  %s\n", common.Hint(starterFnCommand()+" # write the source it declares"))
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

// The ONE function the starter Driftfile declares, and the only place its route,
// method and language are written down.
//
// Two things are generated from these three values: the `atomic.functions` entry
// in the scaffolded file, and the `drift atomic new` command printed beside it.
// They have to agree — the entry names a handler that only that exact command
// produces — and a second copy of "hello"/"get" in either half is how they stop
// agreeing. `TestStarterEntryAndItsCommandAgree` holds them together against the
// real handler-name derivation rather than a restatement of it.
const (
	starterRoute  = "hello"
	starterMethod = "get"
	starterLang   = "go"
)

// starterFnCommand is the `drift atomic new` invocation that writes source for
// the function the starter Driftfile declares.
func starterFnCommand() string {
	return fmt.Sprintf("drift atomic new %s -l %s -m %s", starterRoute, starterLang, starterMethod)
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
	b.WriteString("    - route: " + starterRoute + "\n")
	b.WriteString("      method: " + starterMethod + "\n")
	// Derived, never written out. This handler exists only once
	// `starterFnCommand()` has been run, and that command's own naming rule is
	// the one that decides what it will be called.
	// Derived, never written out. This handler exists only once
	// `starterFnCommand()` has been run, and that command's own naming rule is
	// the one that decides what it will be called.
	b.WriteString("      handler: " + atomic_cmd_new.HandlerName(starterMethod, starterRoute, starterLang) + "\n\n")

	b.WriteString("# Per-environment overrides. Anything set here replaces the base for that\n")
	b.WriteString("# environment — including a 0 or a false, which is the point of the block.\n")
	b.WriteString("# `prod` (or `production`) deploys into the slice named above; any other\n")
	b.WriteString("# environment deploys into <slice>-<environment>, which must already exist.\n")
	b.WriteString("environments:\n")
	b.WriteString("  prod: {}\n")
	return b.String()
}
