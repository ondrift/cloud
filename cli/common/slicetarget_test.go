package common

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withDriftfileSlice stands in for the project package, which owns the Driftfile
// grammar: the answer for any directory is what the test says it is.
func withDriftfileSlice(t *testing.T, fn func(dir string) (string, string, bool, error)) {
	t.Helper()
	prev := DriftfileSlice
	DriftfileSlice = fn
	t.Cleanup(func() {
		DriftfileSlice = prev
		resolveMu.Lock()
		resolveCache = map[string]resolved{}
		resolveMu.Unlock()
	})
	resolveMu.Lock()
	resolveCache = map[string]resolved{}
	resolveMu.Unlock()
}

// DRIFT_SLICE is the explicit override and wins over a Driftfile underfoot: it is
// the one way a script, or a person in an unrelated directory, names a slice.
func TestTheEnvironmentsSliceBeatsTheDriftfile(t *testing.T) {
	withDriftfileSlice(t, func(string) (string, string, bool, error) { return "from-the-file", "/p/Driftfile", true, nil })

	t.Setenv(SliceEnv, "the-named-one")
	got, err := ResolveSlice()
	if err != nil || got.Name != "the-named-one" || got.From != SliceFromEnv {
		t.Errorf("ResolveSlice() = %+v, %v; want the environment's slice", got, err)
	}
}

// Whitespace is trimmed, because a value pasted into CI config arrives with a
// trailing newline more often than not, and an untrimmed name goes into a URL
// authority, where it is a 400 at best. A variable that is only whitespace is set
// to nothing, so the Driftfile still answers.
func TestTheEnvironmentsSliceIsTrimmedAndBlankFallsThroughToTheDriftfile(t *testing.T) {
	withDriftfileSlice(t, func(string) (string, string, bool, error) { return "from-the-file", "/p/Driftfile", true, nil })

	t.Setenv(SliceEnv, "  spaced  \n")
	if got := GetActiveSlice(); got != "spaced" {
		t.Errorf("got %q, want the trimmed value", got)
	}
	t.Setenv(SliceEnv, "   ")
	if got := GetActiveSlice(); got != "from-the-file" {
		t.Errorf("a blank %s overrode the Driftfile: got %q", SliceEnv, got)
	}
}

// THE POINT OF #1296. Nothing is stored between commands, so a slice chosen in
// one project cannot decide what a command in another does: the slice is the one
// the Driftfile in front of you names.
func TestACommandInsideAProjectActsOnThatProjectsSlice(t *testing.T) {
	t.Setenv(SliceEnv, "")
	t.Setenv("HOME", t.TempDir())
	withDriftfileSlice(t, func(dir string) (string, string, bool, error) {
		return filepath.Base(dir), filepath.Join(dir, "Driftfile"), true, nil
	})

	a, b := t.TempDir(), t.TempDir()
	t.Chdir(a)
	first, _ := ResolveSlice()
	t.Chdir(b)
	second, _ := ResolveSlice()

	if first.Name != filepath.Base(a) || second.Name != filepath.Base(b) {
		t.Errorf("resolved %q then %q; each directory should name its own slice (%q, %q)",
			first.Name, second.Name, filepath.Base(a), filepath.Base(b))
	}
	if first.From != SliceFromDriftfile {
		t.Errorf("From = %q, want the Driftfile", first.From)
	}
}

// Outside any project, with nothing in the environment, there is no slice, and the
// refusal says how to name one.
func TestNoSliceIsNamedOutsideAProject(t *testing.T) {
	t.Setenv(SliceEnv, "")
	t.Setenv("HOME", t.TempDir())
	withDriftfileSlice(t, func(string) (string, string, bool, error) { return "", "", false, nil })
	t.Chdir(t.TempDir())

	if got := GetActiveSlice(); got != "" {
		t.Fatalf("GetActiveSlice() = %q with no environment and no Driftfile", got)
	}
	_, err := RequireActiveSlice()
	if err == nil {
		t.Fatal("RequireActiveSlice() succeeded with no slice named")
	}
	for _, want := range []string{SliceEnv, "Driftfile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A Driftfile that cannot name one slice is said so, rather than reported as "no
// slice": the file is there, and the reason is in it.
func TestADriftfileThatNamesNoSliceIsSaidSo(t *testing.T) {
	t.Setenv(SliceEnv, "")
	t.Setenv("HOME", t.TempDir())
	withDriftfileSlice(t, func(string) (string, string, bool, error) {
		return "", "/p/Driftfile", true, errors.New("it declares 2 slice blocks")
	})
	t.Chdir(t.TempDir())

	_, err := RequireActiveSlice()
	if err == nil || !strings.Contains(err.Error(), "/p/Driftfile") || !strings.Contains(err.Error(), "2 slice blocks") {
		t.Errorf("RequireActiveSlice() = %v; want it to name the file and why", err)
	}
}

// A session file written before this change still carries active_slice. It is
// read past and never obeyed.
func TestAStoredActiveSliceIsNoLongerObeyed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(SliceEnv, "")
	t.Setenv(AccountEnv, "")
	withDriftfileSlice(t, func(string) (string, string, bool, error) { return "", "", false, nil })

	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"current":"alice","accounts":{"alice":{"token":"x","refresh_token":"y","active_slice":"stale"}}}`
	if err := os.WriteFile(filepath.Join(home, ".drift", "session.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	if got := GetActiveSlice(); got != "" {
		t.Errorf("a stored active_slice decided the target: %q", got)
	}
}
