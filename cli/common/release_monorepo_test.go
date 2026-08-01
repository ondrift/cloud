package common

import "testing"

// Release tags are prefixed by module directory in the monorepo, because Go
// resolves a subdirectory module by prefixed tag. Selection has to strip that
// before parsing, or every real release is invisible and `drift upgrade`
// reports "up to date" forever.
func TestLatestSemverTag_AcceptsThePrefixedCLITag(t *testing.T) {
	if got := latestSemverTag([]string{"cli/v0.1.0"}); got != "v0.1.0" {
		t.Fatalf("a prefixed CLI tag must resolve to its bare version, got %q", got)
	}
}

// THE cross-module hazard. sdk/ tags live in the same namespace, and the SDK
// is on its own version line — offering one as the CLI's next version would
// have `drift upgrade` try to install a version of the CLI that does not exist.
func TestLatestSemverTag_NeverSelectsAnotherModulesTag(t *testing.T) {
	got := latestSemverTag([]string{"cli/v0.1.0", "sdk/v9.9.9"})
	if got != "v0.1.0" {
		t.Fatalf("only cli/ tags may be selected — sdk/v9.9.9 is a different module's version line, got %q", got)
	}
	if got := latestSemverTag([]string{"sdk/v9.9.9"}); got != "" {
		t.Errorf("with no cli/ tag at all the answer is 'none', not another module's, got %q", got)
	}
}

// Tags cut before the monorepo move are bare. They must keep resolving, or the
// first upgrade check after the move reports nothing available.
func TestLatestSemverTag_StillAcceptsPreMoveBareTags(t *testing.T) {
	if got := latestSemverTag([]string{"v2.10.0"}); got != "v2.10.0" {
		t.Fatalf("a pre-move bare tag must still resolve, got %q", got)
	}
}

// Ordering across both shapes, since the repo now contains a mix.
func TestLatestSemverTag_PicksTheHighestAcrossBothShapes(t *testing.T) {
	got := latestSemverTag([]string{"v0.9.0", "cli/v0.1.0", "sdk/v4.1.2", "cli/v0.2.0"})
	if got != "v0.9.0" {
		t.Fatalf("the highest CLI version wins regardless of tag shape, got %q", got)
	}
}

// Non-version tags are ignored, prefixed or not.
func TestLatestSemverTag_IgnoresNonVersionTags(t *testing.T) {
	for _, in := range [][]string{
		{"cli/nightly"}, {"latest"}, {"cli/v1.2"}, {"release-2026"}, {},
	} {
		if got := latestSemverTag(in); got != "" {
			t.Errorf("%v must select nothing, got %q", in, got)
		}
	}
}

// The CLI is released from the monorepo now. A stale repo here fails silently:
// ondrift/cli still exists and still answers, it just never gains a tag.
func TestCLIRepo_PointsAtTheMonorepo(t *testing.T) {
	if CLIRepo != "ondrift/cloud" {
		t.Errorf("CLIRepo must be the monorepo, got %q", CLIRepo)
	}
}
