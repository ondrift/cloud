package upgrade

import (
	"errors"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// withLatest points the notice at a fixed "latest release" instead of GitHub.
// Passing "" makes the fetch fail, which is the offline case.
func withLatest(t *testing.T, tag string) {
	t.Helper()
	prev := fetchLatest
	fetchLatest = func() (common.LatestRelease, error) {
		if tag == "" {
			return common.LatestRelease{}, errors.New("no network")
		}
		return common.LatestRelease{Tag: tag}, nil
	}
	t.Cleanup(func() { fetchLatest = prev })
}

func notice(t *testing.T, path, current, requested string) string {
	t.Helper()
	var sb strings.Builder
	if err := homebrewUpgradeNotice(&sb, path, current, requested); err != nil {
		t.Fatalf("homebrewUpgradeNotice: %v", err)
	}
	return sb.String()
}

// THE bug. `drift upgrade` on a Homebrew binary already running the newest
// release still printed "Upgrade it with: brew upgrade drift" — every time,
// forever. Reproduced by hand on this machine at v0.3.0, the latest release,
// immediately after a successful `brew upgrade`.
//
// The cause is that runUpgrade returns down the Homebrew path BEFORE reaching
// resolveLabel, which is where "you're already on the latest" lives. So the
// `go install` half of one command knew, and the Homebrew half did not.
//
// Telling someone to run a command that will do nothing is worse than saying
// nothing: it teaches them the output is noise, and the next time an upgrade
// genuinely matters they will not read it.
func TestHomebrewNotice_SaysNothingToDoWhenAlreadyLatest(t *testing.T) {
	withLatest(t, "v0.3.0")
	out := notice(t, "/opt/homebrew/Cellar/drift/0.3.0/bin/drift", "v0.3.0", "")

	if !strings.Contains(out, "already on the latest release (v0.3.0)") {
		t.Errorf("must say the version is current.\ngot:\n%s", out)
	}
	if strings.Contains(out, "brew upgrade drift") {
		t.Errorf("must NOT print an upgrade command when there is nothing to upgrade.\ngot:\n%s", out)
	}
}

// Ahead of the latest tag — a local build, or a tag not yet published. Still
// nothing to do, and definitely not a downgrade instruction.
func TestHomebrewNotice_AheadOfLatestIsAlsoNothingToDo(t *testing.T) {
	withLatest(t, "v0.3.0")
	out := notice(t, "/opt/homebrew/Cellar/drift/0.4.0/bin/drift", "v0.4.0", "")

	if !strings.Contains(out, "already on the latest release") {
		t.Errorf("a version ahead of the latest tag has nothing to upgrade to.\ngot:\n%s", out)
	}
	if strings.Contains(out, "brew upgrade drift") {
		t.Errorf("must not tell a user ahead of the release to upgrade.\ngot:\n%s", out)
	}
}

// The case the command exists for. Behind the latest release, so the command
// must appear — and both versions with it, since "0.2.1 → 0.3.0" is what tells
// the user whether they care.
func TestHomebrewNotice_ShowsTheUpgradeWhenBehind(t *testing.T) {
	withLatest(t, "v0.3.0")
	out := notice(t, "/opt/homebrew/Cellar/drift/0.2.1/bin/drift", "v0.2.1", "")

	for _, want := range []string{"v0.2.1 → v0.3.0", "brew upgrade drift", "go install"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in the behind-latest notice.\ngot:\n%s", want, out)
		}
	}
	if strings.Contains(out, "already on the latest") {
		t.Errorf("v0.2.1 is not the latest.\ngot:\n%s", out)
	}
}

// Offline, the honest answer is "I don't know", not "an upgrade is waiting".
// The command is still printed because it is the one thing that remains true
// without a network — but nothing here may imply a newer version exists.
func TestHomebrewNotice_UnreachableGitHubIsNotAnUpgradeSignal(t *testing.T) {
	withLatest(t, "")
	out := notice(t, "/opt/homebrew/Cellar/drift/0.3.0/bin/drift", "v0.3.0", "")

	if !strings.Contains(out, "Couldn't reach GitHub") {
		t.Errorf("an unanswered check must say so.\ngot:\n%s", out)
	}
	if strings.Contains(out, "already on the latest") {
		t.Errorf("must not claim currency it could not verify.\ngot:\n%s", out)
	}
	if !strings.Contains(out, "brew upgrade drift") {
		t.Errorf("the command is still the right one to print.\ngot:\n%s", out)
	}
}

// A pinned version is a different question, answered before any of the above:
// Homebrew formulae carry one version, so the answer is `go install`, and no
// latest-release comparison applies.
func TestHomebrewNotice_PinnedVersionExplainsTheFormulaLimit(t *testing.T) {
	withLatest(t, "v0.3.0")
	out := notice(t, "/opt/homebrew/Cellar/drift/0.3.0/bin/drift", "v0.3.0", "v0.1.9")

	if !strings.Contains(out, "go install") || !strings.Contains(out, "v0.1.9") {
		t.Errorf("a pin must be answered with the install that can do it.\ngot:\n%s", out)
	}
	if strings.Contains(out, "brew upgrade drift") || strings.Contains(out, "already on the latest") {
		t.Errorf("a pin request is not a latest-release check.\ngot:\n%s", out)
	}
}
