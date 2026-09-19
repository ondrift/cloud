package azure

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// runFileApply used to shell out through TWO stacked deprecated spellings —
// `drift project deploy`, where `project` re-enters under `file` and `deploy`
// re-enters under `apply` — while the line printed right above it already
// said `drift file apply` (flow enumeration finding PLM-77). Proven with a
// fake `drift` on PATH that records the args it was actually invoked with,
// since the real function shells out via exec.LookPath rather than calling
// anything in-process.
func TestRunFileApply_InvokesTheCurrentSpellingNotTwoDeprecatedOnes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable shim below is POSIX shell, not tested on windows")
	}

	binDir := t.TempDir()
	recorded := filepath.Join(t.TempDir(), "args.txt")
	script := "#!/bin/sh\necho \"$@\" > " + recorded + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "drift"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture, needs +x
		t.Fatalf("writing the fake drift binary: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	if err := runFileApply(dir); err != nil {
		t.Fatalf("runFileApply: %v", err)
	}

	got, err := os.ReadFile(recorded) // #nosec G304 -- path built from t.TempDir() above
	if err != nil {
		t.Fatalf("the fake drift binary was never invoked: %v", err)
	}
	if want, have := "file apply\n", string(got); have != want {
		t.Errorf("invoked drift with args %q, want %q — two deprecation notices for a command "+
			"the operator never typed", have, want)
	}
}
