package upgrade

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// fetchLatest is the seam the tests replace. The notice's whole job is to
// compare two versions and say the right thing about them, and that is not
// testable through a function that reaches GitHub.
var fetchLatest = common.FetchLatestCLIRelease

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
// is most of the value of the command. That sentence was in this comment while
// the code below did no such thing: it fetched the latest tag, printed it, and
// told every caller to run `brew upgrade drift` whether or not they were
// already on it. The `go install` path had the comparison all along, so the two
// halves of one command disagreed about what "upgrade" means
// (#CLI-STANDARDUSAGE-1TBYAS).
func homebrewUpgradeNotice(out io.Writer, path, current, requested string) error {
	fmt.Fprintf(out, "This drift was installed with Homebrew.\n")
	fmt.Fprintf(out, "  binary: %s\n\n", path)

	if requested != "" {
		// Homebrew has no general "install this old version" story for a tap
		// formula — the formula only ever describes the current release. Say so
		// rather than printing a command that will not work.
		fmt.Fprintf(out, "  Homebrew always installs the version the formula carries, so it can't\n")
		fmt.Fprintf(out, "  pin %s. To run a specific version, install that one directly:\n\n", requested)
		fmt.Fprintf(out, "    go install %s@%s\n\n", common.CLIModuleBase+"/cmd/drift", normalizeVersion(requested))
		fmt.Fprintf(out, "  %s\n", common.Hint("that puts a second drift in GOPATH/bin — whichever comes first on PATH wins"))
		return nil
	}

	latest := ""
	if rel, err := fetchLatest(); err == nil {
		latest = rel.Tag
	}

	// Up to date is a different answer, not a quieter version of the same one.
	// Printing "upgrade it with: brew upgrade drift" to someone already on the
	// newest release tells them to do work that will do nothing, and it teaches
	// them that this command's output is noise — so the next time it DOES matter
	// they will not read it.
	if latest != "" && current != "" && common.CompareVersions(current, latest) >= 0 {
		fmt.Fprintf(out, "  %s You're already on the latest release (%s).\n", common.Check(), current)
		return nil
	}

	switch {
	case latest != "" && current != "":
		fmt.Fprintf(out, "  %s → %s\n", current, latest)
	case latest != "":
		fmt.Fprintf(out, "  Latest release: %s\n", latest)
	default:
		// The network call failed, so "is there something newer" is unanswered.
		// Say that rather than implying an upgrade is waiting.
		fmt.Fprintf(out, "  Couldn't reach GitHub to check for a newer release.\n")
	}
	fmt.Fprintf(out, "  Upgrade it with:\n\n")
	fmt.Fprintf(out, "    brew upgrade drift\n\n")
	fmt.Fprintf(out, "  %s\n", common.Hint("running `go install` instead would NOT replace this binary — "+
		"it would add a second one in GOPATH/bin and leave Homebrew reporting the old version"))
	return nil
}
