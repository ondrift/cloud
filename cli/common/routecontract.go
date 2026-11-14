// routecontract.go — comparing the route contract the platform states with the
// one this binary speaks.
//
// The api's paths are a public contract held by a CLI that people install and
// upgrade on their own schedule. The api states which version of that contract it
// serves in X-Drift-API-Version on every control-plane reply, and this file is the
// client's half. It follows the Driftfile format's shape (driftfile.go): the
// server states, the client decides.
//
//   - ONLY THE MAJOR DECIDES. Inside a major, routes are only ever added, and a
//     binary that works keeps working.
//   - A PLATFORM AHEAD of this binary is the one skew worth a word: a path this
//     binary knows may have been removed, and the first sign of it would be a bare
//     404 that reads like a typo.
//   - A NEWER BINARY against an older platform is fine, so a lower major says
//     nothing.
//   - A reply that states no version, or states one that is not a plain integer,
//     is UNKNOWN, and unknown says nothing.
//
// Nothing here blocks, retries or rewrites a request or a response. The line is
// information at the point where it is actionable, and the platform stays the
// only thing that refuses a request.
package common

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ImplementedRouteContract is the major of the api's route contract this binary
// was built to speak.
//
// The api's own APIVersion (src/edge/api/version.go in cloud/platform) and this
// constant name the same contract and move together: the api raises its number
// when a route is removed or changes meaning, and the CLI release that speaks the
// new set raises this one.
const ImplementedRouteContract = 1

// APIVersionHeader carries the api's route-contract major on every control-plane
// reply, including the 401 and the 404 a client holding the wrong route set gets.
const APIVersionHeader = "X-Drift-API-Version"

// platformAPIVersion reads the major a reply states, or 0 when it states none or
// states something that is not a plain integer of at least 1.
func platformAPIVersion(h http.Header) int {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get(APIVersionHeader)))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// platformAhead reports whether a stated major is higher than this binary's. An
// unknown version (0) is never ahead.
func platformAhead(platform int) bool {
	return platform > ImplementedRouteContract
}

// NewerPlatformNotice is the line a person sees when the platform speaks a higher
// major than this binary. It names both numbers and the one command that fixes it.
func NewerPlatformNotice(platform int) string {
	return fmt.Sprintf("This drift is older than the platform: it speaks API version %d and the platform speaks %d. "+
		"Run `drift upgrade`.", ImplementedRouteContract, platform)
}

// isBareNotFound reports whether a 404's detail names no thing: empty, the api's
// own answer for a path it does not serve (exactly "not found"), or a router's
// default ("404 page not found"). A 404 that names what it could not find, such
// as "slice not found: demo", is the platform having found the route and missed
// the thing.
//
// Recognised by those words because the platform sends no machine-readable key on
// a route miss, the same footing as isSessionRevoked. A slice's own bare "not
// found", relayed through the api, reads the same, which is why the sentence built
// on this says "may".
func isBareNotFound(detail string) bool {
	switch strings.ToLower(strings.TrimRight(strings.TrimSpace(detail), ".")) {
	case "", "not found", "404 page not found":
		return true
	}
	return false
}

// routeContractNoticeOnce keeps the notice to one per process, which is one per
// command: a deploy makes many requests and each of them carries the header.
var routeContractNoticeOnce sync.Once

// noteRouteContract compares the major a reply states with this binary's and, when
// the platform is ahead, says so on stderr once. It reads one header and touches
// neither the status nor the body, so the caller sees the response unchanged.
func noteRouteContract(resp *http.Response) {
	if resp == nil {
		return
	}
	platform := platformAPIVersion(resp.Header)
	if !platformAhead(platform) {
		return
	}
	routeContractNoticeOnce.Do(func() {
		fmt.Fprintln(os.Stderr, NewerPlatformNotice(platform))
	})
}
