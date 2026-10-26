package project

// backends.go: `atomic.backends`, the container of Atomic's work, resolved into
// the two lists every reader downstream consumes.
//
// A backend is a folder holding one language's flat source files and one
// dependency manifest: `./atomic/<name>`, or `./atomic` itself for the backend
// named `default`. A Function or a Worker is declared inside the backend its handler
// lives in, so the Driftfile mirrors the filesystem.
//
// # It flattens rather than teaching every reader a second shape
//
// The build, the upload, the reference check, the idle report, the schedules
// and the digest all read `atomic.functions` and `atomic.workers` entries, each
// carrying its `backend`, and each of them stays as it is. Flattening here, once,
// is what keeps "which slots does this slice run" a single answer: every backend
// entry is appended to the top-level list it belongs to, carrying its backend
// and the backend's `dir` unless it has its own. `atomic.backends` is then
// removed, so nothing can count an entry twice.
//
// # What the schema cannot say
//
// JSON Schema cannot compare one entry with another, so the rules that do are
// here and run after flattening, across the whole slice: a backend is declared
// once, and a booking key (a Function's `method:route`, a Worker's `worker:<name>`)
// is unique across every backend, because the slice books slots by that key
// alone and not per backend.

import (
	"fmt"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// backendsKey is the list of backends under `atomic`.
const backendsKey = "backends"

// normaliseBackends flattens a slice's `atomic.backends` into its top-level
// `atomic.functions` and `atomic.workers`.
//
// It refuses, before changing anything, a backend declared twice. The checks
// that compare entries with one another run after the identity split, in
// checkSlotIdentities, because a Function's booking key is only composed there.
func normaliseBackends(doc Node) error {
	atomic, ok := asNode(doc.Get("atomic"))
	if !ok {
		return nil
	}
	backends := atomic.Nodes(backendsKey)

	if errs := checkBackendDeclarations(backends); len(errs) > 0 {
		return errs
	}

	for _, el := range backends {
		name := el.Str("name")
		for _, list := range []string{functionsKey, workersKey} {
			for _, entry := range el.Nodes(list) {
				flat := entry.Clone()
				if name != atomic_cmd.DefaultBackendName {
					flat["backend"] = name
				}
				inheritBackend(flat, el)
				atomic[list] = append(atomic.List(list), flat)
			}
		}
	}
	delete(atomic, backendsKey)
	return nil
}

// checkBackendDeclarations refuses a backend named twice in `backends`: each
// backend is declared in one place.
func checkBackendDeclarations(backends []Node) ParseErrors {
	var errs ParseErrors
	count := map[string]int{}
	var order []string
	for _, el := range backends {
		name := el.Str("name")
		if count[name] == 0 {
			order = append(order, name)
		}
		count[name]++
	}
	for _, name := range order {
		if count[name] > 1 {
			errs = append(errs, fmt.Sprintf("the backend %q is declared %d times; a backend is one "+
				"folder, so declare it once and list all of its functions and workers there", name, count[name]))
		}
	}
	return errs
}

// inheritBackend gives one entry its backend's `dir`, unless the entry has its
// own. A backend declares nothing else for its entries: each Function and Worker
// states its own `env` and `secrets`.
func inheritBackend(entry, el Node) {
	if !entry.Has("dir") && el.Str("dir") != "" {
		entry["dir"] = el.Str("dir")
	}
}

// checkSlotIdentities refuses a resolved slice in which two slots share a
// booking key, or a Worker shares its name with a Function route, wherever in the
// slice they are declared.
//
// Read after the backends are flattened and the identity split has composed
// each Function's `method:route`.
//
// A route is unique across the whole slice and not per backend: the slice
// books, admits and routes by the key alone, so two `post:rpc` slots would
// shadow each other with the last deployed answering for both. A Worker and a
// Function route sharing a bare name have different booking keys (`worker:x`
// against `get:x`), but the slice keys logs, metrics, alerts and triggers by the
// bare name, and two slots answering to one name there would share all four.
func checkSlotIdentities(doc Node) error {
	var errs ParseErrors
	errs = append(errs, duplicateKeys("function", FunctionEntries(doc), func(e Node) string { return e.Str("name") })...)
	errs = append(errs, duplicateKeys("worker", WorkerEntries(doc), func(e Node) string { return e.Str("name") })...)

	routes := map[string]bool{}
	for _, p := range FunctionEntries(doc) {
		if r := p.Str("route"); r != "" {
			routes[r] = true
		}
	}
	reported := map[string]bool{}
	for _, f := range WorkerEntries(doc) {
		if name := f.Str("name"); routes[name] && !reported[name] {
			reported[name] = true
			errs = append(errs, fmt.Sprintf("the Worker %q and the Function route %q would share logs and metrics; rename one", name, name))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// duplicateKeys reports every identity two or more entries of one list share,
// naming the backends they are declared in.
func duplicateKeys(noun string, entries []Node, key func(Node) string) []string {
	where := map[string][]string{}
	var order []string
	for _, e := range entries {
		k := key(e)
		if k == "" {
			continue
		}
		if _, seen := where[k]; !seen {
			order = append(order, k)
		}
		backend := e.Str("backend")
		if backend == "" {
			backend = atomic_cmd.DefaultBackendName
		}
		where[k] = append(where[k], backend)
	}

	var out []string
	for _, k := range order {
		in := where[k]
		if len(in) < 2 {
			continue
		}
		out = append(out, fmt.Sprintf("the %s %q is declared %d times (in %s); a booking key is unique across "+
			"the whole slice, not per backend, so declare it once", noun, k, len(in), backendList(in)))
	}
	sort.Strings(out)
	return out
}

// backendList names the backends an identity is declared in, once each, in
// the order they were met: "the backend evm", "the backends evm and bench".
func backendList(in []string) string {
	var names []string
	seen := map[string]bool{}
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 1 {
		return "the backend " + names[0]
	}
	return "the backends " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
