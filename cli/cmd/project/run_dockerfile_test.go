package project

import (
	"strings"
	"testing"
)

// The functions must land in the directory the runner image names. The
// sandbox lets a function execute only beneath the runner paths the platform
// mounts, so a Dockerfile that copies them elsewhere, or that overrides
// RUNNER_DIR with a path of its own, builds cleanly and then fails every
// compiled function at its first invocation.
func TestThinDockerfile_CopiesIntoThePathsTheImageNames(t *testing.T) {
	df := thinDockerfile("ondrift/runner:test")
	lines := strings.Split(strings.TrimSpace(df), "\n")

	want := []string{
		"FROM ondrift/runner:test",
		"COPY runner $RUNNER_DIR",
		"COPY canvas $CANVAS_DATA_DIR",
	}
	if len(lines) != len(want) {
		t.Fatalf("the Dockerfile has %d lines, want %d:\n%s", len(lines), len(want), df)
	}
	for i, w := range want {
		if strings.TrimSpace(lines[i]) != w {
			t.Errorf("line %d is %q, want %q", i+1, lines[i], w)
		}
	}
}

// The image is the one owner of its paths. Any ENV here replaces the image's
// value with a second copy that the sandbox's rules never see.
func TestThinDockerfile_SetsNoPathOfItsOwn(t *testing.T) {
	df := thinDockerfile("ondrift/runner:test")
	for _, line := range strings.Split(df, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.EqualFold(fields[0], "ENV") {
			t.Errorf("the Dockerfile sets %q; the runner image owns its paths", line)
		}
	}
	for _, path := range []string{"/var/runner", "/var/atomic", "/var/canvas", "/data"} {
		if strings.Contains(df, path) {
			t.Errorf("the Dockerfile names %s; it must name the image's variable instead", path)
		}
	}
}
