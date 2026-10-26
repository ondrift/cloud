package common

// slicetarget.go — which slice a command acts on.
//
// A command that does not read a Driftfile itself (`atomic list`, `backbone
// secret set`, `slice restart`, …) still has to act on one slice. It is told in
// one of two ways, and nothing is remembered between commands:
//
//	DRIFT_SLICE=<name>     → that slice. The explicit override, and the only way
//	                         to name a slice from outside any project: a script,
//	                         a pipeline, a terminal in the home directory.
//	otherwise              → the slice the nearest Driftfile declares, found by
//	                         walking up from the working directory, so a command
//	                         run anywhere inside a project acts on that project's
//	                         own slice.
//
// There used to be a third, stored tier set by `drift slice use`. It was sticky
// and global, so a `use` made days ago in one project silently decided what a
// command run in an unrelated project did: the command succeeded, against the
// wrong slice. A slice is now named by something that is in front of you, either
// the variable or the file you are standing in.
//
// A Driftfile that declares environments names its default slice here. An
// environment's own slice is reached with DRIFT_SLICE, or by the commands that
// take the environment themselves (`drift plan staging`, `drift apply staging`).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DriftfileSlice reads the slice the Driftfile nearest dir declares. It is set by
// the package that owns the Driftfile's grammar, because that package imports
// this one. found is false when no Driftfile is on the way up from dir; an error
// is a Driftfile that is there and cannot name one slice.
var DriftfileSlice func(dir string) (name, path string, found bool, err error)

// Where a resolved slice came from.
const (
	SliceFromEnv       = "DRIFT_SLICE"
	SliceFromDriftfile = "Driftfile"
)

// SliceTarget is the slice a command acts on and how it was found.
type SliceTarget struct {
	Name string
	// From is SliceFromEnv or SliceFromDriftfile.
	From string
	// Path is the Driftfile, when From is SliceFromDriftfile.
	Path string
}

type resolved struct {
	target SliceTarget
	err    error
}

var (
	resolveMu    sync.Mutex
	resolveCache = map[string]resolved{}
)

// ResolveSlice says which slice this invocation acts on, and why. The error is
// non-nil when a Driftfile is in reach and cannot name one slice, which is worth
// saying rather than falling through to "no slice".
func ResolveSlice() (SliceTarget, error) {
	if s := strings.TrimSpace(os.Getenv(SliceEnv)); s != "" {
		return SliceTarget{Name: s, From: SliceFromEnv}, nil
	}
	if DriftfileSlice == nil {
		return SliceTarget{}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return SliceTarget{}, nil
	}

	// Every request asks for the slice (the X-Slice header), and reading the file
	// each time would parse it once per HTTP call. The answer cannot change
	// during one command unless the file does, and a command that rewrites it
	// names the slice itself.
	resolveMu.Lock()
	defer resolveMu.Unlock()
	if r, ok := resolveCache[cwd]; ok {
		return r.target, r.err
	}
	name, path, found, err := DriftfileSlice(cwd)
	r := resolved{}
	switch {
	case err != nil:
		r.err = fmt.Errorf("%s names no slice: %w", path, err)
	case found:
		r.target = SliceTarget{Name: name, From: SliceFromDriftfile, Path: path}
	}
	resolveCache[cwd] = r
	return r.target, r.err
}

// GetActiveSlice is the slice this invocation acts on, or "" when it has none.
func GetActiveSlice() string {
	t, _ := ResolveSlice()
	return t.Name
}

var noteOnce sync.Once

// RequireActiveSlice returns the slice this invocation acts on, or an error that
// says how to name one.
//
// When the slice came from the Driftfile rather than from DRIFT_SLICE, it says so
// once on stderr: that is the resolution nobody typed, and a command that acts on
// a slice without ever naming it owes the reader the name.
func RequireActiveSlice() (string, error) {
	t, err := ResolveSlice()
	if err != nil {
		return "", err
	}
	if t.Name == "" {
		return "", fmt.Errorf("no slice to act on: set %s=<name>, or run this inside a project whose Driftfile "+
			"names one (`drift file new` writes it, `drift apply` creates the slice)", SliceEnv)
	}
	if t.From == SliceFromDriftfile {
		noteOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "  slice %s, from %s\n", t.Name, displayPath(t.Path))
		})
	}
	return t.Name, nil
}

// displayPath shows a path relative to the working directory when it is under
// it, and as given otherwise.
func displayPath(p string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "./" + rel
	}
	return p
}
