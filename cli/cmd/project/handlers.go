// handlers.go — the Driftfile's `atomic.functions` list, resolved into what the
// deploy paths consume.
//
// This is the ONE place a manifest entry becomes a deployable function. Every
// count, every collision check and every build reads the result of this file,
// so "what does this project expose" has a single answer rather than one per
// caller.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// FunctionSpecs resolves every declared function into a deployable spec, with
// its source directory made absolute.
//
// The directory follows from the element unless the entry overrides it: the
// default element is the flat files under `atomic/`, a named element is
// `atomic/<element>`. It is never derived from the function's NAME — a name is
// a route, holding `/` and `:`, and the one-folder-per-function layout that
// derivation assumed is not the one most projects use.
func FunctionSpecs(m *Manifest) []atomic_cmd.FunctionSpec {
	atomicRoot := m.ResolvePath("atomic")

	entries := m.Slice().Entries("name", "atomic", "functions")
	specs := make([]atomic_cmd.FunctionSpec, 0, len(entries))
	for _, fn := range entries {
		dir := fn.Str("dir")
		if dir == "" {
			dir = atomic_cmd.ElementDir(atomicRoot, fn.Str("element"))
		} else {
			dir = m.ResolvePath(dir)
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		specs = append(specs, atomic_cmd.FunctionSpec{
			Name:    fn.Str("name"),
			Handler: fn.Str("handler"),
			Element: fn.Str("element"),
			Dir:     dir,
			Auth:    fn.Str("auth"),
			Stream:  fn.Str("stream"),
			Secrets: fn.Strings("secrets"),
		})
	}
	return specs
}

// FindDriftfileFor locates the project a directory belongs to, by walking UP
// from it to the first Driftfile.
//
// From the target rather than the working directory, because `drift atomic
// deploy ./atomic/billing` names a folder INSIDE a project and the project is
// what has to be found. Reading only `./Driftfile` meant the command worked
// from the project root and nowhere else — an absolute path from another
// directory failed with "no Driftfile", which is true of the cwd and beside the
// point. Running it from the root still resolves to the same file.
func FindDriftfileFor(dir string) (string, error) {
	at, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(at, driftfileName)
		if st, serr := os.Stat(candidate); serr == nil && !st.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(at)
		if parent == at {
			return "", fmt.Errorf(
				"no %s in %s or any directory above it — a function is declared in the "+
					"project's manifest, so there has to be one to deploy from",
				driftfileName, dir)
		}
		at = parent
	}
}

// FunctionSpecsInDir resolves the functions the project declares in one
// directory, for `drift atomic deploy <dir>` — which ships a subset of the
// manifest rather than something the manifest has never heard of.
//
// A directory the Driftfile does not name is an error, and has to be: without a
// declaration there is no memory booking, no gate and no handler, so there is
// nothing to deploy and nothing the slice could admit work against.
func FunctionSpecsInDir(dir string) ([]atomic_cmd.FunctionSpec, error) {
	path, err := FindDriftfileFor(dir)
	if err != nil {
		return nil, err
	}
	m, err := ParseDriftfile(path)
	if err != nil {
		return nil, err
	}
	if _, err := m.SelectEnvironment("", false); err != nil {
		return nil, err
	}

	want, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dir, err)
	}

	var out []atomic_cmd.FunctionSpec
	for _, s := range FunctionSpecs(m) {
		if s.Dir == want {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"%s declares no functions in %s.\n"+
				"       A function is declared in the Driftfile — its trigger, the handler that\n"+
				"       serves it, the memory it books and its gate — so there is nothing here to\n"+
				"       deploy until one names this directory.",
			filepath.Base(path), dir)
	}
	return out, nil
}

// CountAtomicFunctions returns how many functions the deploy will ship.
//
// An Atomic function IS a declared entry. Callables the manifest does not name
// are helpers: free, unrouted, and uncounted.
func CountAtomicFunctions(m *Manifest) (int, error) {
	return len(m.Slice().Entries("name", "atomic", "functions")), nil
}

// CountScheduledFunctions returns how many scheduled jobs the deploy will
// register — the count used to size the slice's envelope, and what
// tier.CentsPerScheduledJob is charged against.
func CountScheduledFunctions(m *Manifest) (int, error) {
	n := 0
	for _, fn := range m.Slice().Entries("name", "atomic", "functions") {
		if fn.Str("cron") != "" {
			n++
		}
	}
	return n, nil
}
