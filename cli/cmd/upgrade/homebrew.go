package upgrade

import (
	"fmt"

	"github.com/ondrift/cloud/cli/common"
)

// homebrewUpgradeNotice handles `drift upgrade` for a Homebrew-installed binary.
//
// It deliberately does NOT run `brew upgrade` itself. Invoking somebody's
// package manager on their behalf is a surprise: `brew upgrade drift` can pull
// in a Homebrew auto-update and touch unrelated formulae, which is not what
// "upgrade the drift CLI" led them to expect. Printing the exact command costs
// one paste and keeps the user in charge of their own machine.
//
// It still performs the version CHECK, because that part is genuinely useful
// and needs no package manager — knowing whether an upgrade is even available
// is most of the value of the command.
func homebrewUpgradeNotice(path, requested string) error {
	fmt.Printf("This drift was installed with Homebrew.\n")
	fmt.Printf("  binary: %s\n\n", path)

	if requested != "" {
		// Homebrew has no general "install this old version" story for a tap
		// formula — the formula only ever describes the current release. Say so
		// rather than printing a command that will not work.
		fmt.Printf("  Homebrew always installs the version the formula carries, so it can't\n")
		fmt.Printf("  pin %s. To run a specific version, install that one directly:\n\n", requested)
		fmt.Printf("    go install %s@%s\n\n", common.CLIModuleBase+"/cmd/drift", normalizeVersion(requested))
		fmt.Printf("  %s\n", common.Hint("that puts a second drift in GOPATH/bin — whichever comes first on PATH wins"))
		return nil
	}

	current := ""
	if rel, err := common.FetchLatestCLIRelease(); err == nil {
		current = rel.Tag
	}

	if current != "" {
		fmt.Printf("  Latest release: %s\n", current)
	}
	fmt.Printf("  Upgrade it with:\n\n")
	fmt.Printf("    brew upgrade drift\n\n")
	fmt.Printf("  %s\n", common.Hint("running `go install` instead would NOT replace this binary — "+
		"it would add a second one in GOPATH/bin and leave Homebrew reporting the old version"))
	return nil
}
