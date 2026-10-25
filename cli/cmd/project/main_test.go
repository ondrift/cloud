package project

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps this package's tests off the network.
//
// A test that runs `drift apply` through to a real deploy vendors the Drift SDK
// into the function it builds, and that resolves the latest SDK tag from GitHub's
// unauthenticated API: 60 requests an hour to one address, which a shared CI box
// spends. The tests then failed with "GitHub returned 403 Forbidden", on a diff
// that had nothing to do with it. They vendor the SDK from this checkout instead
// (DRIFT_SDK_SOURCE), which is also the SDK the CLI under test is released with.
func TestMain(m *testing.M) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stat(filepath.Join(root, "sdk", "package.json")); err != nil {
		fmt.Fprintf(os.Stderr, "the SDK is expected beside the CLI at %s: %v\n", root, err)
		os.Exit(1)
	}
	if os.Getenv("DRIFT_SDK_SOURCE") == "" {
		os.Setenv("DRIFT_SDK_SOURCE", root) // #nosec G104 -- a test process
	}
	os.Exit(m.Run())
}
