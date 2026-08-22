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
	"strings"

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
			Name:     fn.Str("name"),
			Handler:  fn.Str("handler"),
			Element:  fn.Str("element"),
			Dir:      dir,
			Auth:     fn.Str("auth"),
			Stream:   fn.Str("stream"),
			Response: fn.Str("response"),
			Secrets:  fn.Strings("secrets"),
		})
	}
	return specs
}

// CheckCompiledBookings refuses a compiled function booked under the floor its
// own language runtime needs, on this machine, before anything is uploaded.
//
// This is the half of the booking rule the schema cannot state. `pattern` already
// refuses anything outside 8MB-256MB offline; the compiled floor needs the
// LANGUAGE, and the Driftfile never says what language a function is written in —
// it is detected from the source beside it. So the bound is read from the schema
// (CompiledMemoryFloor) and applied here, where `elements` has already bound each
// function to the element whose language was detected.
//
// Why it is worth catching here at all, given the api refuses it anyway: a
// booking under the floor does not fail cleanly at run time. The reservation
// clamps to the pool, so the function admits one invocation at a time rather than
// none — single requests return 200 and the slice only gives way under
// concurrency. The api turning that into a 400 is what makes it visible, and this
// moves the same answer to the laptop.
//
// Reports every offending function rather than the first: a tutorial Driftfile
// booking three Go functions at 8MB should be fixed in one pass, not three.
func CheckCompiledBookings(m *Manifest, elements []atomic_cmd.Element) error {
	floor := CompiledMemoryFloor()
	if floor.Bytes <= 0 {
		return nil
	}

	// The declared booking, by function name. Read from the manifest rather than
	// the spec because a FunctionSpec carries what it takes to BUILD a function,
	// and memory is not one of those things.
	declared := map[string]string{}
	for _, fn := range m.Slice().Entries("name", "atomic", "functions") {
		declared[fn.Str("name")] = fn.Str("memory")
	}

	var bad []string
	for _, el := range elements {
		if !floor.Applies(el.Lang) {
			continue
		}
		for _, f := range el.Funcs {
			raw := declared[f.Spec.Name]
			if raw == "" {
				continue // a missing booking is already reported, with its own message
			}
			bytes, err := parseSizeBytes(raw)
			if err != nil || bytes >= floor.Bytes {
				continue
			}
			bad = append(bad, fmt.Sprintf("  %s booked %s, in %s", f.Spec.Name, raw, el.Lang))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf(
		"a compiled function needs at least %s, and %d of these book less:\n%s\n\n"+
			"A compiled language carries a private language runtime inside every invocation, "+
			"and the pool is charged what an invocation is measured to cost — that runtime "+
			"included. The interpreted languages share one language server each and can book "+
			"as little as 8MB.\n"+
			"Raise each of them to %s or higher, or run `drift file benchmark` against a "+
			"deployed slice to size them from what they actually hold.",
		floor.Declared, len(bad), strings.Join(bad, "\n"), floor.Declared)
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
