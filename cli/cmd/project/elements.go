package project

// elements.go: `atomic.elements`, the container of Atomic's work, resolved into
// the two lists every reader downstream consumes.
//
// An element is a folder holding one language's flat source files and one
// dependency manifest: `./atomic/<name>`, or `./atomic` itself for the element
// named `default`. A Pulse or a Field is declared inside the element its handler
// lives in, so the Driftfile mirrors the filesystem.
//
// # It flattens rather than teaching every reader a second shape
//
// The build, the upload, the reference check, the idle report, the schedules
// and the digest all read `atomic.pulses` and `atomic.fields` entries, each
// carrying its `element`, and each of them stays as it is. Flattening here, once,
// is what keeps "which slots does this slice run" a single answer: every element
// entry is appended to the top-level list it belongs to, carrying its element,
// the element's `dir` unless it has its own, its `env` merged over the
// element's, and the union of both `secrets` lists. `atomic.elements` is then
// removed, so nothing can count an entry twice.
//
// # What the schema cannot say
//
// JSON Schema cannot compare one entry with another, so the rules that do are
// here and run after flattening, across the whole slice: an element is declared
// once, and a booking key (a Pulse's `method:route`, a Field's `field:<name>`)
// is unique across every element, because the slice books slots by that key
// alone and not per element.

import (
	"fmt"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// elementsKey is the list of elements under `atomic`.
const elementsKey = "elements"

// normaliseElements flattens a slice's `atomic.elements` into its top-level
// `atomic.pulses` and `atomic.fields`.
//
// It refuses, before changing anything, an element declared twice. The checks
// that compare entries with one another run after the identity split, in
// checkSlotIdentities, because a Pulse's booking key is only composed there.
func normaliseElements(doc Node) error {
	atomic, ok := asNode(doc.Get("atomic"))
	if !ok {
		return nil
	}
	elements := atomic.Nodes(elementsKey)

	if errs := checkElementDeclarations(elements); len(errs) > 0 {
		return errs
	}

	for _, el := range elements {
		name := el.Str("name")
		for _, list := range []string{pulsesKey, fieldsKey} {
			for _, entry := range el.Nodes(list) {
				flat := entry.Clone()
				if name != atomic_cmd.DefaultElementName {
					flat["element"] = name
				}
				inheritElement(flat, el)
				atomic[list] = append(atomic.List(list), flat)
			}
		}
	}
	delete(atomic, elementsKey)
	return nil
}

// checkElementDeclarations refuses an element named twice in `elements`: each
// element is declared in one place.
func checkElementDeclarations(elements []Node) ParseErrors {
	var errs ParseErrors
	count := map[string]int{}
	var order []string
	for _, el := range elements {
		name := el.Str("name")
		if count[name] == 0 {
			order = append(order, name)
		}
		count[name]++
	}
	for _, name := range order {
		if count[name] > 1 {
			errs = append(errs, fmt.Sprintf("the element %q is declared %d times; an element is one "+
				"folder, so declare it once and list all of its pulses and fields there", name, count[name]))
		}
	}
	return errs
}

// inheritElement gives one entry what its element declares for all of them:
// the element's `dir` unless the entry has its own, the element's `env` with
// the entry's own keys winning, and the union of both `secrets` lists, the
// element's first.
func inheritElement(entry, el Node) {
	if !entry.Has("dir") && el.Str("dir") != "" {
		entry["dir"] = el.Str("dir")
	}

	if env := el.Sub("env"); len(env) > 0 {
		merged := env.Clone()
		for k, v := range entry.Sub("env") {
			merged[k] = v
		}
		entry["env"] = map[string]any(merged)
	}

	if secrets := el.Strings("secrets"); len(secrets) > 0 {
		seen := map[string]bool{}
		var union []any
		for _, s := range append(secrets, entry.Strings("secrets")...) {
			if !seen[s] {
				seen[s] = true
				union = append(union, s)
			}
		}
		entry["secrets"] = union
	}
}

// checkSlotIdentities refuses a resolved slice in which two slots share a
// booking key, or a Field shares its name with a Pulse route, wherever in the
// slice they are declared.
//
// Read after the elements are flattened and the identity split has composed
// each Pulse's `method:route`.
//
// A route is unique across the whole slice and not per element: the slice
// books, admits and routes by the key alone, so two `post:rpc` slots would
// shadow each other with the last deployed answering for both. A Field and a
// Pulse route sharing a bare name have different booking keys (`field:x`
// against `get:x`), but the slice keys logs, metrics, alerts and triggers by the
// bare name, and two slots answering to one name there would share all four.
func checkSlotIdentities(doc Node) error {
	var errs ParseErrors
	errs = append(errs, duplicateKeys("pulse", PulseEntries(doc), func(e Node) string { return e.Str("name") })...)
	errs = append(errs, duplicateKeys("field", FieldEntries(doc), func(e Node) string { return e.Str("name") })...)

	routes := map[string]bool{}
	for _, p := range PulseEntries(doc) {
		if r := p.Str("route"); r != "" {
			routes[r] = true
		}
	}
	reported := map[string]bool{}
	for _, f := range FieldEntries(doc) {
		if name := f.Str("name"); routes[name] && !reported[name] {
			reported[name] = true
			errs = append(errs, fmt.Sprintf("the Field %q and the Pulse route %q would share logs and metrics; rename one", name, name))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// duplicateKeys reports every identity two or more entries of one list share,
// naming the elements they are declared in.
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
		element := e.Str("element")
		if element == "" {
			element = atomic_cmd.DefaultElementName
		}
		where[k] = append(where[k], element)
	}

	var out []string
	for _, k := range order {
		in := where[k]
		if len(in) < 2 {
			continue
		}
		out = append(out, fmt.Sprintf("the %s %q is declared %d times (in %s); a booking key is unique across "+
			"the whole slice, not per element, so declare it once", noun, k, len(in), elementList(in)))
	}
	sort.Strings(out)
	return out
}

// elementList names the elements an identity is declared in, once each, in
// the order they were met: "the element evm", "the elements evm and bench".
func elementList(in []string) string {
	var names []string
	seen := map[string]bool{}
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 1 {
		return "the element " + names[0]
	}
	return "the elements " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
