package main

import (
	_ "embed"
	"fmt"
	"os"
	"runtime/debug"

	account "github.com/ondrift/cloud/cli/cmd/account"
	atomic "github.com/ondrift/cloud/cli/cmd/atomic"
	backbone "github.com/ondrift/cloud/cli/cmd/backbone"
	canvas "github.com/ondrift/cloud/cli/cmd/canvas"
	deed "github.com/ondrift/cloud/cli/cmd/deed"
	file "github.com/ondrift/cloud/cli/cmd/file"
	migrate "github.com/ondrift/cloud/cli/cmd/migrate"
	portal "github.com/ondrift/cloud/cli/cmd/portal"
	slice "github.com/ondrift/cloud/cli/cmd/slice"
	upgrade "github.com/ondrift/cloud/cli/cmd/upgrade"
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
	shownVersion := resolveVersion()

	rootCmd := &cobra.Command{
		Use:     "drift",
		Short:   "Drift is a minimalist cloud hosting service.",
		Version: shownVersion,
		// Two numbers, because they move independently. The CLI version is
		// this binary; the Driftfile version is the manifest format it
		// implements, which the platform serves and can be ahead of. Reporting
		// only the first makes "why won't my Driftfile deploy" unanswerable
		// without guesswork.
		//
		// Deliberately no network call: `--version` is what you run when
		// something is already wrong, and it must answer instantly and offline.
		// The comparison against what the platform serves happens where it is
		// actionable — at deploy and lint.
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		// Bare `drift` in a terminal launches the full-screen dashboard; in a
		// pipe/CI (non-TTY) it falls back to help so scripts don't hang on a TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return cmd.Help()
			}
			return portal.Run(shownVersion, nil)
		},
	}

	rootCmd.SetVersionTemplate(fmt.Sprintf(
		"drift %s\ndriftfile schema %s\n", shownVersion, common.DriftfileSchemaVersionOrNone()))

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

	rootCmd.AddCommand(
		// File (everything a Driftfile does — lint it, apply it, run it)
		fileCmd,
		common.AliasCommand(fileCmd, "project", common.Deprecation{
			Old: "drift project",
			New: "drift file",
		}),

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

		// Self-update: reinstall the CLI at latest (or a pinned version)
		upgrade.GetCmd(),

		// Portal — interactive TUI dashboard over your slices/functions/data
		portal.GetCmd(version),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
