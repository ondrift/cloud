package slice

import (
	"os"
	"path/filepath"
	"testing"
)

// withNonTerminalStdin replaces os.Stdin with a regular file, which is what CI
// and `drift slice resize < /dev/null` both look like.
//
// The handoff's own copy lives beside it in `common`, where the guard it drives
// now lives. Two private test helpers rather than one exported one: a helper
// exported only so another package's tests can call it is production surface
// that exists for the test suite.
func withNonTerminalStdin(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = prev
		_ = f.Close()
	})
}
