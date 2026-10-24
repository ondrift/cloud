package atomic_cmd

// targetplatform.go — which architecture a deploy builds for.
//
// A function is compiled on the developer's machine and executed on a slice, so
// the right target is a fact about the slice and cannot be read off this machine.
// The slice reports it: `arch` in its runtime report, served at
// /ops/slice/runtime. The first build of a run asks once and every later build
// reuses the answer.
//
// The answer is best-effort. A slice that predates the field, an api that cannot
// be reached and an architecture this CLI does not build for all leave the build
// on defaultTargetPlatform, which is what every deploy used before the slice said
// anything, so no slice becomes undeployable by this lookup.
//
// A local run (`drift file run`, `drift atomic run`) has no slice and builds for
// the machine that runs it; it neither asks nor takes an answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

var (
	// stagingLocally is true while StageElementsLocally builds for a local run.
	stagingLocally bool

	targetMu       sync.Mutex
	targetResolved bool
	resolvedTarget string
)

// emulationNotice is where a host/target mismatch is stated. A variable so a test
// can read it.
var emulationNotice io.Writer = os.Stderr

// fetchSliceRuntime reads the active slice's runtime report. A variable so a test
// can stand in for the platform.
var fetchSliceRuntime = func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := common.DoRequestWithContext(ctx, http.MethodGet, common.APIBaseURL+"/ops/slice/runtime", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime report answered HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// remoteTargetPlatform is the platform a deploy to a slice builds for, resolved
// once per run.
func remoteTargetPlatform() string {
	targetMu.Lock()
	defer targetMu.Unlock()
	if targetResolved {
		return resolvedTarget
	}
	targetResolved = true
	resolvedTarget = defaultTargetPlatform
	if body, err := fetchSliceRuntime(); err == nil {
		var report struct {
			Arch string `json:"arch"`
		}
		if json.Unmarshal(body, &report) == nil {
			if p := platformForSliceArch(report.Arch); p != "" {
				resolvedTarget = p
			}
		}
	}
	noteEmulation(resolvedTarget)
	return resolvedTarget
}

// platformForSliceArch maps the slice's spelling (Rust's `std::env::consts::ARCH`)
// to docker's. An architecture this CLI does not build for yields "".
func platformForSliceArch(arch string) string {
	switch arch {
	case "aarch64", "arm64":
		return "linux/arm64"
	case "x86_64", "amd64":
		return "linux/amd64"
	}
	return ""
}

// noteEmulation says so, once, when the build runs for an architecture other than
// this machine's. docker runs it under emulation: correct, slow, and dependent on
// binfmt being registered, which reads as a hang rather than as a cause.
//
// The host architecture is read here to describe the build and never to choose
// it; the target stays a property of the slice.
func noteEmulation(platform string) {
	if platform == "linux/"+runtime.GOARCH {
		return
	}
	fmt.Fprintf(emulationNotice, "building for %s on a %s host: this runs under emulation and is slower\n",
		platform, "linux/"+runtime.GOARCH)
}

// resetTargetPlatform forgets what the run resolved. Only tests need it: a real
// run is one process and one answer.
func resetTargetPlatform() {
	targetMu.Lock()
	defer targetMu.Unlock()
	targetResolved = false
	resolvedTarget = ""
}
