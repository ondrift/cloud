package file

// new.go — scaffold a Driftfile that deploys.
//
// The starter file writes what the format requires (the slice's billing, each
// pulse's handler and memory) and comments what it does not, because the gap
// between a file that parses and one that deploys is where a first run stalls.
//
// The scaffold ships one complete pulse rather than an empty element: an empty
// element is refused and teaches nothing, and a user's first edit should be
// changing a real block rather than inventing the shape of one.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
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
		Long: "Creates a Driftfile declaring a slice and one pulse, with everything the format " +
			"requires already written, so the first `drift plan` is not a validation error.",
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
			// `drift apply` creates the slice a Driftfile names when it does not
			// exist, so a file naming the directory would buy a second slice beside
			// the one the account already made. Naming the active slice applies the
			// file into that one.
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
				fmt.Printf("  %s\n", common.Hint("no active slice — run `drift slice use <name>` and re-run, or edit the slice block by hand"))
			}
			// NAME THE COMMAND THAT WRITES THE SOURCE, because this file declares
			// a pulse that does not exist yet.
			//
			// The scaffolded block is `pulse "GET /hello"` with `handler =
			// "GetHello"`, and nothing on disk defines GetHello — a Driftfile
			// declares pulses, it does not create them. So `drift file new`
			// followed by `drift plan` fails at the element's folder, and the
			// failure names what is missing rather than the command that would
			// produce it. A new user's second command dead-ends on their first.
			//
			// The command below is not an example: it is the one that writes source
			// matching THIS block exactly — same route, same method, and
			// `HandlerName` derives the same `GetHello` from them. The two halves
			// are generated from the starter constants, so they cannot drift apart
			// while the test that compares them holds.
			fmt.Printf("  %s\n", common.Hint(starterFnCommand()+" # write the source it declares"))
			fmt.Printf("  %s\n", common.Hint("drift file lint    # check it"))
			fmt.Printf("  %s\n", common.Hint("drift plan         # see what applying it creates, and what it costs"))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "slice name (default: your active slice, else the directory name)")
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

// The ONE pulse the starter Driftfile declares, and the only place its route,
// method, language and booking are written down.
//
// Two things are generated from these values: the pulse block in the
// scaffolded file, and the `drift atomic new` command printed beside it. They
// have to agree — the block names a handler that only that exact command
// produces — and a second copy of "hello"/"get" in either half is how they stop
// agreeing. `TestStarterEntryAndItsCommandAgree` holds them together against the
// real handler-name derivation rather than a restatement of it.
const (
	starterRoute  = "hello"
	starterMethod = "get"
	starterLang   = "go"
	// starterMemory is above the floor a compiled language needs, so the
	// starter's Go pulse is not refused by the language it is written in.
	starterMemory = "32MiB"
)

// starterFnCommand is the `drift atomic new` invocation that writes source for
// the function the starter Driftfile declares.
func starterFnCommand() string {
	return fmt.Sprintf("drift atomic new %s -l %s -m %s", starterRoute, starterLang, starterMethod)
}

func starterDriftfile(name, canvasDir string) string {
	var b strings.Builder
	b.WriteString("# " + name + ": a Drift project.\n")
	b.WriteString("# `drift file explain` lists every block this file can hold, `drift file lint`\n")
	b.WriteString("# checks it without deploying, and `drift plan` shows what applying it does and\n")
	b.WriteString("# what that costs.\n\n")

	b.WriteString("slice \"" + name + "\" {\n")
	b.WriteString("  # How the slice is paid for: \"free\" for a shape inside the free slice,\n")
	b.WriteString("  # or \"monthly\".\n")
	b.WriteString("  billing = \"free\"\n")
	b.WriteString("}\n\n")

	b.WriteString("# An element is one folder of one language's source: \"default\" is ./atomic\n")
	b.WriteString("# itself, and any other name is ./atomic/<name>. Each pulse and field is\n")
	b.WriteString("# declared inside the element its handler lives in; nothing in your source\n")
	b.WriteString("# declares one.\n")
	b.WriteString("element \"" + atomic_cmd.DefaultElementName + "\" {\n")
	b.WriteString("  # A pulse answers one call with one result. It is labelled with its method\n")
	b.WriteString("  # and route, or with QUEUE and the queue it drains: \"QUEUE orders\".\n")
	b.WriteString("  # Long-running workers are `field` blocks beside it.\n")
	b.WriteString("  pulse \"" + strings.ToUpper(starterMethod) + " /" + starterRoute + "\" {\n")
	// Derived, never written out. This handler exists only once
	// `starterFnCommand()` has been run, and that command's own naming rule is
	// the one that decides what it will be called.
	b.WriteString("    handler = \"" + atomic_cmd_new.HandlerName(starterMethod, starterRoute, starterLang) + "\"\n")
	b.WriteString("    # The memory it books: its own pool, and what it is billed at.\n")
	b.WriteString("    # `drift size` rewrites it from what the slice measured.\n")
	b.WriteString("    memory = \"" + starterMemory + "\"\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")

	if canvasDir != "" {
		b.WriteString("\n# A static site, served at the root.\n")
		b.WriteString("site \"/\" {\n")
		b.WriteString("  dir = \"" + canvasDir + "\"\n")
		b.WriteString("}\n")
	}

	b.WriteString("\n# An environment is a slice of its own, <slice>-<name> unless it names one:\n")
	b.WriteString("#\n")
	b.WriteString("#   environment \"staging\" {\n")
	b.WriteString("#     billing = \"free\"\n")
	b.WriteString("#   }\n")
	b.WriteString("#\n")
	b.WriteString("# `drift plan staging` and `drift apply staging` work on it.\n")
	return b.String()
}
