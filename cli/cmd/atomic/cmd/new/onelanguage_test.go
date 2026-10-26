// onelanguage_test.go — a backend is ONE language, and the scaffolder is where
// a user finds that out.
//
// The rule is load-bearing and invisible: a backend has exactly one dependency
// manifest (go.mod / requirements.txt / package.json / …), so two languages in
// one folder cannot both resolve. Nothing in the layout announces that a
// subfolder is also a language boundary, and the docs site says so in prose that
// a user adding a second language has not necessarily read.
//
// `runNew` refuses the mix and names the way out. That refusal was enforced by
// code no test held, which is the shape where a rule quietly stops being
// enforced during a refactor and nothing says so until a user mixes two
// languages and gets a deploy-time failure instead of a one-line message.
package atomic_cmd_new

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inAProjectWith puts the test in a scratch project whose default backend
// already holds one source file, and returns the atomic dir.
//
// t.Chdir rather than a helper of our own: runNew resolves its target through
// resolveAtomicDir(), which reads the working directory, so the test has to
// actually be somewhere.
func inAProjectWith(t *testing.T, filename string) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	atomicDir := filepath.Join(root, "atomic")
	if err := os.MkdirAll(atomicDir, 0o750); err != nil {
		t.Fatalf("create atomic/: %v", err)
	}
	if err := os.WriteFile(filepath.Join(atomicDir, filename), []byte("// existing\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", filename, err)
	}
	return atomicDir
}

// THE ONE THAT MATTERS. A Node backend, and a Python function aimed at it.
//
// The refusal has to happen BEFORE anything is written: a half-scaffolded
// backend with two manifests is worse than the refusal, because the next
// command the user runs is a deploy that fails somewhere else entirely.
func TestASecondLanguageInOneBackendIsRefusedAndTheWayOutIsNamed(t *testing.T) {
	atomicDir := inAProjectWith(t, "orders.js")

	err := runNew("predict", "python", "get", "", "", "")
	if err == nil {
		t.Fatal("a Python function was added to a Node backend without complaint; " +
			"the backend now has two languages and one manifest slot")
	}

	// The message has to carry three things, or it is a refusal the reader
	// cannot act on: what the backend already is, that language decides the
	// boundary, and the flag that starts a new one.
	for _, want := range []string{"node", "different backend", "--backend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never mentions %q:\n  %s", want, err)
		}
	}

	// NOTHING WAS WRITTEN. The check runs before the mkdir and the write, and
	// this is what proves it rather than assuming the order.
	entries, rerr := os.ReadDir(atomicDir)
	if rerr != nil {
		t.Fatalf("read atomic/: %v", rerr)
	}
	for _, e := range entries {
		if e.Name() != "orders.js" {
			t.Errorf("the refused scaffold still left %s behind", e.Name())
		}
	}
}

// THE CONTROL, and without it the test above passes for a scaffolder that
// refuses everything. The same language in the same backend is the common case
// and must still work.
func TestAnotherFunctionInTheSameLanguageJoinsTheBackend(t *testing.T) {
	atomicDir := inAProjectWith(t, "orders.js")

	if err := runNew("status", "node", "get", "", "", ""); err != nil {
		t.Fatalf("a second Node function was refused by its own backend: %v", err)
	}
	if _, err := os.Stat(filepath.Join(atomicDir, "status.js")); err != nil {
		t.Errorf("the scaffolder reported success and wrote no source: %v", err)
	}
}

// THE WAY OUT ACTUALLY WORKS. The refusal names `--backend`, so naming one has
// to be the thing that succeeds — a message pointing at a flag that does not
// resolve the problem is worse than no message.
func TestNamingABackendIsWhatLetsASecondLanguageIn(t *testing.T) {
	atomicDir := inAProjectWith(t, "orders.js")

	if err := runNew("predict", "python", "get", "", "", "ml"); err != nil {
		t.Fatalf("the way out the refusal names did not work: %v", err)
	}

	// Its own folder, its own manifest. Both halves matter: the folder is the
	// boundary and the manifest is why the boundary exists.
	for _, want := range []string{"predict.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(atomicDir, "ml", want)); err != nil {
			t.Errorf("the new backend has no %s: %v", want, err)
		}
	}
	// And the original backend is untouched.
	if _, err := os.Stat(filepath.Join(atomicDir, "orders.js")); err != nil {
		t.Errorf("starting a new backend disturbed the old one: %v", err)
	}
}

// backendLang is what the refusal reads, so its blind spots are the rule's
// blind spots. An empty backend admits any language; a directory that is not
// there yet is empty, not an error.
func TestAnEmptyOrAbsentBackendAdmitsAnyLanguage(t *testing.T) {
	root := t.TempDir()
	if got := backendLang(filepath.Join(root, "never-created")); got != "" {
		t.Errorf("an absent backend reported language %q; a new project would be refused its first function", got)
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := backendLang(empty); got != "" {
		t.Errorf("an empty backend reported language %q", got)
	}
}

// A MANIFEST IS NOT A SOURCE FILE. `go.mod`, `package.json` and friends sit in
// the backend beside the handlers, and reading one as the backend's language
// would make a backend with a manifest and no source refuse its own language.
func TestTheBackendsLanguageComesFromSourceNotFromItsManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "el")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "go.mod", ".env", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := backendLang(dir); got != "" {
		t.Errorf("a manifest was read as the backend's language (%q), so a backend "+
			"holding only manifests would refuse the language it is for", got)
	}
}
