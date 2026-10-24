package atomic_cmd

import (
	"errors"
	"testing"
	"time"
)

// pinDigestSDKTag replaces the SDK release lookup for one test and resets what a
// previous test resolved. It returns how many times the lookup ran.
func pinDigestSDKTag(t *testing.T, tag string, err error) *int {
	t.Helper()
	calls := 0
	prev := sdkResolveTag
	sdkResolveTag = func() (string, error) {
		calls++
		return tag, err
	}
	resetSDKDigestTag()
	t.Cleanup(func() {
		sdkResolveTag = prev
		resetSDKDigestTag()
	})
	return &calls
}

var someSpec = FunctionSpec{Name: "post:expense", Handler: "PostExpense", Element: "default"}

// THE ONE THAT MATTERS: publishing a new SDK release moves the digest, so the
// next deploy rebuilds the functions that resolve it instead of printing
// `(unchanged)` beside a stale vendored copy.
func TestANewSDKReleaseMovesTheDeployDigest(t *testing.T) {
	pinDigestSDKTag(t, "sdk/v0.9.0", nil)
	before := DeployDigest("build-digest", someSpec)

	pinDigestSDKTag(t, "sdk/v0.9.1", nil)
	after := DeployDigest("build-digest", someSpec)

	if before == after {
		t.Fatalf("a new SDK release did not move the digest (%s): the function would be skipped as unchanged", after)
	}
}

// The same release is the same digest, or every apply would redeploy everything.
func TestTheSameSDKReleaseIsTheSameDigest(t *testing.T) {
	pinDigestSDKTag(t, "sdk/v0.9.0", nil)
	a := DeployDigest("build-digest", someSpec)
	pinDigestSDKTag(t, "sdk/v0.9.0", nil)
	b := DeployDigest("build-digest", someSpec)

	if a != b {
		t.Fatalf("the digest is not stable for one SDK release: %s != %s", a, b)
	}
}

// One lookup per run, however many functions are fingerprinted: the answer
// cannot change between two functions of one apply.
func TestTheSDKReleaseIsLookedUpOncePerRun(t *testing.T) {
	calls := pinDigestSDKTag(t, "sdk/v0.9.0", nil)
	for i := 0; i < 5; i++ {
		DeployDigest("build-digest", someSpec)
	}
	if *calls != 1 {
		t.Errorf("the SDK release was looked up %d times for one run, want 1", *calls)
	}
}

// THE FALLBACK. A lookup that fails (offline, rate-limited) or does not answer
// in time mixes NOTHING in, so the digest is the one every earlier CLI wrote and
// an unreachable GitHub never redeploys a project.
func TestAnUnresolvedSDKReleaseLeavesTheDigestAsItWas(t *testing.T) {
	pinDigestSDKTag(t, "", errors.New("resolver unavailable"))
	want := DeployDigest("build-digest", someSpec)

	t.Run("a failed lookup", func(t *testing.T) {
		pinDigestSDKTag(t, "", errors.New("GitHub returned 403 rate limit exceeded"))
		if got := DeployDigest("build-digest", someSpec); got != want {
			t.Errorf("a failed lookup changed the digest: %s != %s", got, want)
		}
	})

	t.Run("a lookup that never answers", func(t *testing.T) {
		pinDigestSDKTag(t, "", nil)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		sdkResolveTag = func() (string, error) { <-release; return "sdk/v9.9.9", nil }
		resetSDKDigestTag()
		prevWait := sdkDigestWait
		sdkDigestWait = 20 * time.Millisecond
		t.Cleanup(func() { sdkDigestWait = prevWait })

		start := time.Now()
		got := DeployDigest("build-digest", someSpec)
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("a hung lookup stalled the digest for %s", took)
		}
		if got != want {
			t.Errorf("a lookup that never answered changed the digest: %s != %s", got, want)
		}
	})
}

// An unknown build digest is still never a matchable value.
func TestAnUnknownBuildDigestStaysUnmatchable(t *testing.T) {
	pinDigestSDKTag(t, "sdk/v0.9.0", nil)
	if got := DeployDigest("", someSpec); got != "" {
		t.Errorf("an empty build digest produced %q, want the empty string", got)
	}
}
