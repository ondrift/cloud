package atomic_cmd

// sdkdigest.go — the SDK release a build resolves, as part of a function's
// deploy digest.
//
// A build resolves the Drift SDK at build time rather than carrying a copy: the
// vendoring fast path downloads the newest tagged release, and the container
// path installs it from the SDK's repository. So the same source can ship a
// different SDK on two days, and a digest made of the source alone cannot tell.
// Publishing an SDK fix then left `drift apply` printing `(unchanged)` beside a
// function still running the broken copy, and only `--force` cured it.
//
// The release is therefore one more input to the digest. It is the same lookup
// the build itself uses (sdkResolveTag), so DRIFT_SDK_TAG pins the digest and the
// build together.
//
// The lookup is best-effort and never blocks a deploy. A failure (offline,
// GitHub's unauthenticated rate limit) or a slow answer mixes nothing in, which
// leaves the digest exactly as an earlier CLI wrote it: an unreachable GitHub
// never redeploys a project, and the next run that can resolve a release simply
// notices the change then.

import (
	"sync"
	"time"
)

// sdkDigestWait bounds how long fingerprinting waits for the release lookup. The
// build's own lookup allows 30 seconds; a digest only decides whether to skip, so
// it must cost far less than the build it might save.
var sdkDigestWait = 5 * time.Second

var (
	sdkDigestMu       sync.Mutex
	sdkDigestResolved bool
	sdkDigestTag      string
)

// sdkTagForDigest is the SDK release to fold into a digest, or "" when it could
// not be learned. Resolved once per run: the answer cannot change between two
// functions of one apply, and the lookup is a network request.
func sdkTagForDigest() string {
	sdkDigestMu.Lock()
	defer sdkDigestMu.Unlock()
	if sdkDigestResolved {
		return sdkDigestTag
	}
	sdkDigestResolved = true

	type answer struct {
		tag string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		tag, err := sdkResolveTag()
		done <- answer{tag, err}
	}()
	select {
	case a := <-done:
		if a.err == nil {
			sdkDigestTag = a.tag
		}
	case <-time.After(sdkDigestWait):
	}
	return sdkDigestTag
}

// resetSDKDigestTag forgets what the run resolved. Only tests need it: a real
// run is one process and one answer.
func resetSDKDigestTag() {
	sdkDigestMu.Lock()
	defer sdkDigestMu.Unlock()
	sdkDigestResolved = false
	sdkDigestTag = ""
}
