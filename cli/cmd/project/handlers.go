// handlers.go — the Driftfile's `atomic.pulses` and `atomic.fields` lists,
// resolved into what the deploy paths consume.
//
// This is the ONE place a manifest entry becomes a deployable slot. Every
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

// The two lists Atomic's work is declared in, under `atomic`.
//
// `pulses` is the request/response unit, one call and one result. `fields` is
// the long-running worker the slice keeps exactly one of. Both hold every
// element's entries by the time anything here reads them: elements.go flattens
// `atomic.elements` into them, each entry carrying its element.
const (
	pulsesKey = "pulses"
	fieldsKey = "fields"
)

// PulseEntries is every `atomic.pulses` entry of a resolved document.
func PulseEntries(doc Node) []Node { return doc.Entries("name", "atomic", pulsesKey) }

// FieldEntries is every `atomic.fields` entry of a resolved document.
func FieldEntries(doc Node) []Node { return doc.Entries("name", "atomic", fieldsKey) }

// FunctionSpecs resolves every declared Pulse into a deployable spec, with its
// source directory made absolute.
//
// The directory follows from the element unless the entry overrides it: the
// default element is the flat files under `atomic/`, a named element is
// `atomic/<element>`. It is never derived from the Pulse's NAME — a name is a
// route, holding `/` and `:`, and the one-folder-per-function layout that
// derivation assumed is not the one most projects use.
func FunctionSpecs(m *Manifest) []atomic_cmd.FunctionSpec {
	entries := PulseEntries(m.Slice())
	specs := make([]atomic_cmd.FunctionSpec, 0, len(entries))
	for _, fn := range entries {
		specs = append(specs, atomic_cmd.FunctionSpec{
			Name:       fn.Str("name"),
			Handler:    fn.Str("handler"),
			Element:    fn.Str("element"),
			Dir:        entryDir(m, fn),
			Auth:       fn.Str("auth"),
			Stream:     fn.Str("stream"),
			Response:   fn.Str("response"),
			Secrets:    fn.Strings("secrets"),
			Env:        fn.StrMap("env"),
			Concurrent: fn.Bool("concurrent"),
			PollMS:     fn.Int("poll_ms"),
			MaxRetry:   fn.Int("max_retry"),
		})
	}
	return specs
}

// FieldSpecs resolves every declared Field into a deployable spec.
//
// A Field is a slot whose method is `field`, so its spec is a FunctionSpec named
// by its booking key, `field:<name>`. Wire splits that into the method and name
// the operator takes, the digest, the collision check, the reference check and
// the idle report all read it as they read a Pulse's, and IsField is what the
// build reads to give it the Field entry point instead of a Pulse's. A Field has
// no gate, stream, reply shape or schedule: nothing calls it.
func FieldSpecs(m *Manifest) []atomic_cmd.FunctionSpec {
	entries := FieldEntries(m.Slice())
	specs := make([]atomic_cmd.FunctionSpec, 0, len(entries))
	for _, f := range entries {
		specs = append(specs, atomic_cmd.FunctionSpec{
			Name:    atomic_cmd.FieldKey(f.Str("name")),
			Handler: f.Str("handler"),
			Element: f.Str("element"),
			Dir:     entryDir(m, f),
			Secrets: f.Strings("secrets"),
			Env:     f.StrMap("env"),
		})
	}
	return specs
}

// AtomicSpecs is every slot the Driftfile declares: its Pulses, then its
// Fields. It is what a build, a deploy, the collision check and the slice
// reference check read, because each of those is about slots, and a Field is
// one.
func AtomicSpecs(m *Manifest) []atomic_cmd.FunctionSpec {
	return append(FunctionSpecs(m), FieldSpecs(m)...)
}

// entryDir is the absolute source directory of one Pulse or Field entry: its
// own `dir` when it declares one, its element's otherwise.
func entryDir(m *Manifest, entry Node) string {
	dir := entry.Str("dir")
	if dir == "" {
		dir = atomic_cmd.ElementDir(m.ResolvePath("atomic"), entry.Str("element"))
	} else {
		dir = m.ResolvePath(dir)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

// CheckCompiledBookings is CheckSlotBookings against the bookings the Driftfile
// writes: the offline check `drift file lint` makes.
func CheckCompiledBookings(c *Compiled, elements []atomic_cmd.Element) error {
	return CheckSlotBookings(c.Manifest, elements, &LiveSlice{Config: c.Shape})
}

// CheckSlotBookings refuses a compiled Pulse or Field booked under the floor its
// own language runtime needs, on this machine, before anything is uploaded.
//
// The bookings are the given slice's: the live one, or the shape the Driftfile
// compiles to. A slot it does not book is not checked.
//
// This is the half of the booking rule the schema cannot state. The compiler
// already refuses a booking outside the platform's range; the compiled floor
// needs the LANGUAGE, and the Driftfile never says what language a slot is
// written in: it is detected from the source beside it. So the bound is read from
// the schema (CompiledMemoryFloor) and applied here, where `elements` has already
// bound each slot to the element whose language was detected.
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
func CheckSlotBookings(m *Manifest, elements []atomic_cmd.Element, live *LiveSlice) error {
	floor := CompiledMemoryFloor()
	if floor.Bytes <= 0 {
		return nil
	}

	// The booking, by slot, read from the slice rather than the spec because a
	// FunctionSpec carries what it takes to BUILD a slot, and memory is not one
	// of those things.
	booked := map[string]int{}
	if live != nil {
		for _, s := range live.Config.Atomic.Functions {
			if s.MemoryBytes > 0 {
				booked[s.Name] = s.MemoryBytes
			}
		}
	}

	var bad []string
	for _, el := range elements {
		if !floor.Applies(el.Lang) {
			continue
		}
		for _, f := range el.Funcs {
			bytes := booked[f.Spec.Name]
			if bytes == 0 || bytes >= floor.Bytes {
				continue // no booking in hand, or enough
			}
			bad = append(bad, fmt.Sprintf("  %s booked %s, in %s", f.Spec.Name, sizeLabel(bytes), el.Lang))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf(
		"a compiled pulse or field needs at least %s, and %d of these book less:\n%s\n\n"+
			"A compiled language carries a private language runtime inside every invocation, "+
			"and the pool is charged what an invocation is measured to cost — that runtime "+
			"included. The interpreted languages share one language server each and can book "+
			"as little as 8MiB.\n"+
			"Raise each one's memory in the Driftfile to %s or higher, or run `drift size` against a "+
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

	// THE SCHEDULES THIS MANIFEST DECLARES, published for the deploy that is
	// about to read them.
	//
	// Without this line `drift atomic deploy` shipped every pulse with its
	// `cron` dropped: the schedule registry was empty because only the full
	// apply ever filled it, so the same Driftfile produced a schedule under one
	// command and none under the other, silently.
	//
	// It belongs here because this is the function that reads the Driftfile on
	// the single-directory path. `atomic deploy` is documented as deploying "a
	// subset of the manifest and never something the manifest has not seen" — a
	// subset of the functions, not a subset of what each function IS.
	//
	// Publishing the WHOLE manifest's schedules, not just the ones in `dir`, is
	// deliberate: the registry is keyed by function name and only the functions
	// being deployed ever look themselves up in it, so a wider map cannot reach a
	// function this command is not shipping.
	atomic_cmd.SetDeclaredSchedules(declaredSchedules(m))

	// The declared queues, for `drift atomic run`: the function runs against
	// the SDK's in-process store, which creates them so a declared queue nothing
	// has pushed to reads empty there as it does on a slice. See queues.go in
	// the deploy package.
	atomic_cmd.SetDeclaredQueues(DeclaredQueues(m))

	want, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dir, err)
	}

	var out []atomic_cmd.FunctionSpec
	for _, s := range AtomicSpecs(m) {
		if s.Dir == want {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"%s declares no pulses or fields in %s.\n"+
				"       A pulse or field is declared in the Driftfile (the handler that runs it,\n"+
				"       the memory it books and, for a pulse, its trigger and gate), so there is\n"+
				"       nothing here to deploy until one names this directory.",
			filepath.Base(path), dir)
	}
	return out, nil
}

// CountAtomicFunctions returns how many slots the deploy will ship: every Pulse
// and every Field.
//
// An Atomic slot IS a declared entry. Callables the manifest does not name are
// helpers: free, unrouted, and uncounted.
func CountAtomicFunctions(m *Manifest) (int, error) {
	return len(PulseEntries(m.Slice())) + len(FieldEntries(m.Slice())), nil
}

// CountScheduledFunctions returns how many scheduled jobs the deploy will
// register — the count used to size the slice's envelope, and what
// tier.CentsPerScheduledJob is charged against. Only a Pulse carries a `cron:`.
func CountScheduledFunctions(m *Manifest) (int, error) {
	n := 0
	for _, fn := range PulseEntries(m.Slice()) {
		if fn.Str("cron") != "" {
			n++
		}
	}
	return n, nil
}
