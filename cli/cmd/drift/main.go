package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	account "github.com/ondrift/cloud/cli/cmd/account"
	atomic "github.com/ondrift/cloud/cli/cmd/atomic"
	backbone "github.com/ondrift/cloud/cli/cmd/backbone"
	canvas "github.com/ondrift/cloud/cli/cmd/canvas"
	deed "github.com/ondrift/cloud/cli/cmd/deed"
	doctor "github.com/ondrift/cloud/cli/cmd/doctor"
	file "github.com/ondrift/cloud/cli/cmd/file"
	migrate "github.com/ondrift/cloud/cli/cmd/migrate"
	portal "github.com/ondrift/cloud/cli/cmd/portal"
	project "github.com/ondrift/cloud/cli/cmd/project"
	slice "github.com/ondrift/cloud/cli/cmd/slice"
	upgrade "github.com/ondrift/cloud/cli/cmd/upgrade"
	wallet "github.com/ondrift/cloud/cli/cmd/wallet"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// version is stamped by the release build:
//
//	go build -ldflags "-X main.version=v1.0.0"
//
// Empty is the honest default, because `go install` does NOT apply ldflags —
// and `drift upgrade` on a go-install binary runs exactly that. This used to
// default to a hardcoded "v0.1.1", so every upgraded binary reported v0.1.1
// forever: `drift upgrade` compared that against the latest release, found it
// behind, reinstalled, and produced another binary reporting v0.1.1. The
// command never converged, and the "you're already on the latest" branch was
// unreachable for every user who installed the documented way.
var version = ""

// resolveVersion reports what this binary actually is.
//
// The release stamp wins when present. Failing that, Go records the module
// version it installed inside the binary itself, which is precisely the case
// ldflags cannot cover — `go install ...@v0.4.0` yields Main.Version "v0.4.0".
// A build from a working tree has neither and says so rather than inventing a
// release number (#CLI-STANDARDUSAGE-ZMMKC6).
func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func main() {
	// Resolved once, so `--version`, the dashboard header and `drift upgrade`
	// cannot disagree about which binary this is.
	os.Exit(run(newRootCmd(resolveVersion()), os.Stderr))
}

// run executes the command tree and returns the status the process exits with: 0
// for success, the status an error carries when it is a common.ExitStatusError
// (`drift plan --exit-code` reports what it found that way), and 1 for any other
// error. What an error says is said once, to stderr.
func run(root *cobra.Command, stderr io.Writer) int {
	err := root.Execute()
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	return common.ExitStatus(err)
}

// newRootCmd assembles the whole command tree.
//
// Extracted from main so a test can hold the tree and ask questions of it —
// specifically, whether every remedy the error registry suggests resolves to a
// command that exists. A remedy naming a command nobody implemented is worse
// than none: it sends a stuck user somewhere that fails again.
func newRootCmd(shownVersion string) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "drift",
		Short:   "Drift is a minimalist cloud hosting service.",
		Version: shownVersion,
		// Three numbers, because they move independently. The CLI version is
		// this binary. The format it IMPLEMENTS is compiled in. The format on
		// this MACHINE is whatever the platform last served, which can be ahead
		// of both. Printing only the binary's version makes "why won't my
		// Driftfile deploy" unanswerable without guesswork, and printing only
		// the machine's copy answers it with a number this code never claimed.
		//
		// Deliberately no network call: `--version` is what you run when
		// something is already wrong, and it must answer instantly and offline.
		// The comparison that acts on the difference happens where it is
		// actionable — when a Driftfile is linted or applied.
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		// Bare `drift` in a terminal launches the full-screen dashboard; in a
		// pipe/CI (non-TTY) it falls back to help so scripts don't hang on a TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return cmd.Help()
			}
			return portal.Run(shownVersion)
		},
	}

	rootCmd.SetVersionTemplate(fmt.Sprintf(
		"drift %s\ndriftfile format implemented: %s\ndriftfile format on this machine: %s\n",
		shownVersion, common.ImplementedDriftfileFormat, common.DriftfileSchemaVersionOrNone()))

	rootCmd.AddGroup(&cobra.Group{
		ID:    "services",
		Title: "Services:",
	})

	rootCmd.AddGroup(&cobra.Group{
		ID:    "account",
		Title: "Account:",
	})

	rootCmd.AddGroup(&cobra.Group{
		ID:    "project",
		Title: "Project:",
	})

	// One noun for the Driftfile. `drift project` keeps working as a deprecated
	// spelling that re-runs under `drift file`, so nothing anyone has typed or
	// scripted breaks.
	//
	// No RemoveAfter: naming a version would promise a date this has not chosen,
	// and a promise the release then misses teaches people to ignore the notice.
	// The alias goes when the old spelling stops showing up, and the notice says
	// what to type rather than when to panic.
	fileCmd := file.GetCmd()

	// `drift plan`: what applying the Driftfile would change, at the top level
	// because it is about the whole slice the file declares.
	planCmd := project.GetPlanCmd()
	planCmd.GroupID = "project"
	applyCmd := project.GetApplyCmd()
	applyCmd.GroupID = "project"

	// `drift size`: the measured peaks written back into the Driftfile.
	sizeCmd := project.GetSizeCmd()
	sizeCmd.GroupID = "project"

	rootCmd.AddCommand(
		planCmd,
		applyCmd,
		sizeCmd,

		// File (everything a Driftfile does — lint it, apply it, run it)
		fileCmd,
		common.AliasCommand(fileCmd, "project", common.Deprecation{
			Old: "drift project",
			New: "drift file",
		}),

		// Doctor — the one place to go when something is wrong. Offline by
		// design: the moment you need it is the moment something else failed.
		doctor.GetCmd(),

		// Migrate (read-only lift-off from another cloud, e.g. Azure)
		migrate.GetCmd(),

		// Atomic functions
		atomic.GetCmd(),

		// Canvas (static sites)
		canvas.GetCmd(),

		// Backbone primitives (secrets, queues, blobs)
		backbone.GetCmd(),

		// Deed (identity: KeyAuth, JWT, Vault, Link, Pocket) — status only
		deed.GetCmd(),

		// Slice lifecycle (create, list, use, delete, upgrade)
		slice.GetCmd(),

		// Account (signup, login, usage, upgrade)
		account.GetAccountCmd(),

		// Wallet (the euro balance: balance, history, topup)
		wallet.GetCmd(),

		// Self-update: reinstall the CLI at latest (or a pinned version)
		upgrade.GetCmd(),
	)

	refuseUnknownVerbs(rootCmd)
	return rootCmd
}

// unknownVerbAnnotation marks a group whose run function refuseUnknownVerbs
// installed, so a test can tell it from a command's own.
const unknownVerbAnnotation = "drift/refuses-unknown-verbs"

// removedVerbs are the Driftfile verbs that are gone, by the group's path, with
// what does the job now. A script still running one fails and is told what to
// type instead.
var removedVerbs = map[string]map[string]string{
	"drift file": {
		"apply":    "`drift apply` applies the Driftfile, and `drift plan` shows what it would change",
		"deploy":   "`drift apply` applies the Driftfile, and `drift plan` shows what it would change",
		"simulate": "`drift plan` shows what applying the Driftfile would change, and what it would cost",
		"diff":     "`drift plan` shows what applying the Driftfile would change, and what it would cost",
		"migrate":  "the Driftfile is HCL: `drift file new` writes a starter, and `drift file explain` lists every block it can hold",
	},
	"drift atomic": {
		"deploy": "`drift apply` builds and ships every function and worker whose source or declaration changed, and leaves the rest alone; `drift plan` shows what it would change",
	},
}

// refuseUnknownVerbs gives every group without a run function one: bare, it
// prints the group's help; with a verb the group does not have, it is an error.
//
// Cobra's own answer to `drift file nosuchverb` is the group's help and exit 0,
// because a group with no run function treats any argument as a request for
// help. Only the root refuses an unknown command. So `drift file apply`, which
// was a deploy, became a no-op that passed in CI.
//
// Unknown flags are let through to the run function, so `drift file apply
// --plan` is answered with what replaced `drift file apply` rather than with a
// complaint about --plan.
func refuseUnknownVerbs(root *cobra.Command) {
	var visit func(c *cobra.Command)
	visit = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			visit(child)
		}
		if c == root || !c.HasSubCommands() || c.Run != nil || c.RunE != nil {
			return
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[unknownVerbAnnotation] = "true"
		c.FParseErrWhitelist.UnknownFlags = true
		c.Args = cobra.ArbitraryArgs
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if instead, ok := removedVerbs[cmd.CommandPath()][args[0]]; ok {
				return fmt.Errorf("there is no %q: %s", cmd.CommandPath()+" "+args[0], instead)
			}
			return fmt.Errorf("unknown command %q for %q. Run '%s --help' for the commands it has",
				args[0], cmd.CommandPath(), cmd.CommandPath())
		}
	}
	visit(root)
}
