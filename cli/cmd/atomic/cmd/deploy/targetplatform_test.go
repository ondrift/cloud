package atomic_cmd

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// hostPlatform is the docker platform of the machine running the tests, in the
// spelling targetPlatform returns. The tests build for the OTHER architecture
// when they need a mismatch, so they hold on either kind of laptop.
func hostPlatform() string { return "linux/" + runtime.GOARCH }

func otherPlatform() string {
	if runtime.GOARCH == "amd64" {
		return "linux/arm64"
	}
	return "linux/amd64"
}

func archOf(platform string) string {
	return map[string]string{"linux/arm64": "aarch64", "linux/amd64": "x86_64"}[platform]
}

// withSliceReport runs a test with the slice's runtime report replaced by body,
// resets what a previous test resolved, and returns how many times it was asked.
func withSliceReport(t *testing.T, body string, err error) *int {
	t.Helper()
	t.Setenv("DRIFT_TARGET_PLATFORM", "")
	calls := 0
	prevFetch, prevOut := fetchSliceRuntime, emulationNotice
	fetchSliceRuntime = func() ([]byte, error) {
		calls++
		return []byte(body), err
	}
	emulationNotice = &bytes.Buffer{}
	resetTargetPlatform()
	t.Cleanup(func() {
		fetchSliceRuntime, emulationNotice = prevFetch, prevOut
		resetTargetPlatform()
	})
	return &calls
}

// THE ONE THAT MATTERS: the build target is what the slice says it runs on, for
// either architecture, and not a constant.
func TestTheBuildTargetIsTheArchitectureTheSliceReports(t *testing.T) {
	for arch, want := range map[string]string{
		"x86_64":  "linux/amd64",
		"aarch64": "linux/arm64",
	} {
		withSliceReport(t, `{"num_cpu":2,"arch":"`+arch+`"}`, nil)
		if got := targetPlatform(); got != want {
			t.Errorf("a slice reporting %s: targetPlatform() = %q, want %q", arch, got, want)
		}
	}
}

// The explicit override outranks the slice, and asks the slice nothing: it is
// for air-gapped or experimental use, where the slice may not be reachable.
func TestTheOverrideOutranksTheSliceAndIsNotAskedFor(t *testing.T) {
	calls := withSliceReport(t, `{"arch":"x86_64"}`, nil)
	t.Setenv("DRIFT_TARGET_PLATFORM", "linux/arm64")

	if got := targetPlatform(); got != "linux/arm64" {
		t.Errorf("targetPlatform() = %q, want the override", got)
	}
	if *calls != 0 {
		t.Errorf("the slice was asked %d time(s) although the build target was overridden", *calls)
	}
}

// THE FALLBACK: a slice that reports nothing usable, an older one with no
// `arch`, an unreachable api and an architecture we do not build for all leave
// today's behaviour exactly, so the CLI keeps working against any slice.
func TestAnUnusableReportFallsBackToTheDefault(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		err  error
	}{
		"an older slice with no arch": {`{"num_cpu":2}`, nil},
		"an unreachable api":          {"", errors.New("connection refused")},
		"a body that is not JSON":     {"<html>", nil},
		"an architecture we skip":     {`{"arch":"riscv64"}`, nil},
		"an empty arch":               {`{"arch":""}`, nil},
	} {
		withSliceReport(t, tc.body, tc.err)
		if got := targetPlatform(); got != defaultTargetPlatform {
			t.Errorf("%s: targetPlatform() = %q, want the default %q", name, got, defaultTargetPlatform)
		}
	}
}

// One lookup per run, however many functions build: the answer cannot change
// between two functions of the same deploy, and a Go element links its
// functions concurrently.
func TestTheSliceIsAskedOncePerRun(t *testing.T) {
	calls := withSliceReport(t, `{"arch":"x86_64"}`, nil)

	for i := 0; i < 5; i++ {
		targetPlatform()
	}
	if *calls != 1 {
		t.Errorf("the slice was asked %d times for one run, want 1", *calls)
	}
}

// A local run builds for the machine that runs it, and has no slice to ask. It
// must neither query one nor take its answer, or `drift file run` on an x86
// laptop would build for whatever the logged-in account's slice is.
func TestALocalRunDoesNotAskTheSlice(t *testing.T) {
	calls := withSliceReport(t, `{"arch":"`+archOf(otherPlatform())+`"}`, nil)

	stagingLocally = true
	t.Cleanup(func() { stagingLocally = false })

	if got := targetPlatform(); got != defaultTargetPlatform {
		t.Errorf("a local run took %q from a slice, want the default %q", got, defaultTargetPlatform)
	}
	if *calls != 0 {
		t.Errorf("a local run asked the slice %d time(s)", *calls)
	}
}

// A build for the other architecture runs under emulation: slow, and needing
// binfmt, which reads as a hang rather than as a cause. It is said once, and
// not at all when target and host agree.
func TestAMismatchedTargetIsStatedOnce(t *testing.T) {
	withSliceReport(t, `{"arch":"`+archOf(otherPlatform())+`"}`, nil)
	targetPlatform()
	targetPlatform()
	out := emulationNotice.(*bytes.Buffer).String()
	if strings.Count(out, "emulation") != 1 || !strings.Contains(out, otherPlatform()) {
		t.Errorf("expected one emulation notice naming %s, got %q", otherPlatform(), out)
	}

	withSliceReport(t, `{"arch":"`+archOf(hostPlatform())+`"}`, nil)
	targetPlatform()
	if out := emulationNotice.(*bytes.Buffer).String(); out != "" {
		t.Errorf("a native build printed a notice: %q", out)
	}
}
