package atomic_cmd

// sink_test.go: what the local sink leaves in the runner's directory.
//
// `drift file run` stages every function into a directory that is baked into the
// runner image. The runtime reads that directory as a user of its own, never as
// the person who ran `drift`, so a file the build wrote for its owner alone
// (the wrappers it generates are written 0600) is a function that fails at load
// with "permission denied". The build archives each file with the mode it has,
// and the sink must not carry that mode over: it places files readable by
// everyone, and keeps the executable bit of what was executable.

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withUmask sets the process umask for one test and restores it afterwards. No
// test of this package runs in parallel, so nothing else creates a file meanwhile.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	previous := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(previous) })
}

// stagedFunction lays out what the interpreted build path archives for one
// function: the wrapper it generates (0600), the user's code in a directory
// created 0700, and a script that was executable (0700). The modes are set
// explicitly, so the umask of the machine running the test decides nothing here.
func stagedFunction(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, mode := range map[string]fs.FileMode{
		"app.js":        0o600,
		"lib/helper.js": 0o600,
		"bin/run.sh":    0o700,
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("// "+rel+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"lib", "bin"} {
		if err := os.Chmod(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// archived is the archive the build hands the sink for the staged tree.
func archived(t *testing.T, staged string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "function.tar.gz")
	if err := createTarGz(staged, archive); err != nil {
		t.Fatalf("createTarGz: %v", err)
	}
	return archive
}

// extractedFunction runs the sink's extraction over the staged tree's archive
// and returns the directory it extracted into.
func extractedFunction(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "slot")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(archived(t, stagedFunction(t)), dst); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	return dst
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("what the sink extracted is missing %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func wantMode(t *testing.T, root, rel string, want fs.FileMode) {
	t.Helper()
	if got := modeOf(t, filepath.Join(root, rel)); got != want {
		t.Errorf("%s was extracted as %v, want %v", rel, got, want)
	}
}

// A file the build wrote for its owner alone lands readable by everyone, and so
// does the directory above it.
func TestExtractTarGz_AFileTheBuildWroteForItsOwnerLandsReadableByEveryone(t *testing.T) {
	dst := extractedFunction(t)

	wantMode(t, dst, "app.js", 0o644)
	wantMode(t, dst, "lib/helper.js", 0o644)
	wantMode(t, dst, "lib", 0o755)
	wantMode(t, dst, "bin", 0o755)
}

// The executable bit survives, for everyone: a script the runtime starts is
// started by a user who is not its owner. A file that was not executable does
// not become one.
func TestExtractTarGz_KeepsTheExecutableBitAndAddsNone(t *testing.T) {
	dst := extractedFunction(t)

	wantMode(t, dst, "bin/run.sh", 0o755)
	if got := modeOf(t, filepath.Join(dst, "app.js")); got&0o111 != 0 {
		t.Errorf("app.js was not executable and was extracted as %v", got)
	}
}

// The mode the sink asks for is narrowed by the umask of whoever runs `drift`,
// and the runtime's user is neither that person nor in their group. A machine
// that sets a strict umask would otherwise place files only their owner reads.
func TestExtractTarGz_TheUmaskDoesNotNarrowWhatLands(t *testing.T) {
	staged := stagedFunction(t)
	archive := archived(t, staged)
	dst := filepath.Join(t.TempDir(), "slot")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	withUmask(t, 0o077)
	if err := extractTarGz(archive, dst); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	wantMode(t, dst, "app.js", 0o644)
	wantMode(t, dst, "lib/helper.js", 0o644)
	wantMode(t, dst, "bin/run.sh", 0o755)
	wantMode(t, dst, "lib", 0o755)
	wantMode(t, dst, "bin", 0o755)
}

// Through the sink itself: every file and directory of an interpreted function's
// slot can be read by a user who is not its owner, so the runtime can load it.
func TestLocalSlotSink_APlacedInterpretedSlotIsReadableByAnotherUser(t *testing.T) {
	withUmask(t, 0o022)
	runner := t.TempDir()
	art := FuncArtifact{
		Name: "hello", Method: "get", Language: "node", Backend: "default",
		SourcePath: archived(t, stagedFunction(t)),
	}
	if err := localSlotSink(runner)(art); err != nil {
		t.Fatalf("localSlotSink: %v", err)
	}

	slot := filepath.Join(runner, slotDirName(art.Backend, art.Method, art.Name))
	seen := map[string]bool{}
	err := filepath.WalkDir(slot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(slot, path)
		if err != nil {
			return err
		}
		seen[rel] = true
		perm := modeOf(t, path)
		if d.IsDir() && perm&0o005 != 0o005 {
			t.Errorf("directory %q is %v: another user cannot enter it", rel, perm)
		}
		if !d.IsDir() && perm&0o004 == 0 {
			t.Errorf("file %q is %v: another user cannot read it", rel, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"app.js", "lib/helper.js", "bin/run.sh", "metadata.json"} {
		if !seen[rel] {
			t.Errorf("the slot holds no %s, so the walk did not check it", rel)
		}
	}
}
