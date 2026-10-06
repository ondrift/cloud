package project

import (
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// Key-level deprecations for the Driftfile.
//
// JSON Schema draft-07 with `additionalProperties: false` has two outcomes,
// legal or error — it cannot say "deprecated but working". So a key removed from
// `properties` becomes an error for every existing document on the next api
// deploy, with no notice, which the deprecate-alias-then-remove rule forbids.
// The key stays in the schema and the CLIENT says something about it, which is
// what makes keeping it cheap.
//
// This runs AFTER schema validation and BEFORE checkLocalPaths. Both halves of
// that matter: validating first means the walker only ever sees a legal
// document, and running before checkLocalPaths means that pass reads one
// spelling rather than two — it consumes several of the keys an alias rewrites.
//
// It is deliberately a separate pass. checkLocalPaths carries a charter that it
// may ask the filesystem a question and may never ask whether a value is
// well-formed; folding key rewriting into it re-creates the duplication that
// charter exists to end.

// driftfileKeyDeprecations is every retired Driftfile key this CLI knows about.
//
// It is a variable rather than a constant so a test can drive the walker through
// ParseDriftfile with a set of its own, which is the only way to check the hook
// point itself rather than the walker in isolation.
//
// A key belongs here when it is a RENAME or an IGNORE — one key becoming one
// other key, or none. A key that SPLITS is not expressible by this walker:
// `atomic.functions[].name` becomes a route and a method, so it is normalised in
// functionidentity.go instead, and emits its notice through the same Deprecation
// type so it still lists with these.
var driftfileKeyDeprecations = append([]common.KeyDeprecation{
	// Atomic runs two kinds of work, and the list a request/response unit is
	// declared in is named for the kind it holds: a Pulse is one call and one
	// result, beside the long-running Fields in `atomic.fields`. A rename in
	// place, so each overlay renames its own key and the merge sees one spelling.
	//
	// FIRST, because every other entry under this list is addressed by the new
	// spelling: the walker visits the registry in order, so the rename has to
	// land before `atomic.pulses[].memory` is looked for.
	common.KeyDeprecationFor("atomic.functions", common.KeyAlias, common.Deprecation{
		Old: "atomic.functions",
		New: "atomic.pulses",
	}),
	// The identity is a REFERENCE to a slice `drift slice create` made, not a name
	// this file mints. A straight rename, so the walker rewrites it and every
	// reader downstream sees one spelling.
	common.KeyDeprecationFor("name", common.KeyAlias, common.Deprecation{
		Old: "name",
		New: "slice",
	}),
	// A collection the manifest seeds or expires is a REFERENCE to one the slice
	// already holds, so the key names the slot rather than creating a collection.
	common.KeyDeprecationFor("backbone.nosql[].name", common.KeyAlias, common.Deprecation{
		Old: "backbone.nosql[].name",
		New: "backbone.nosql[].slot",
	}),
	// Where a site mounts is a path, not a route — a route is a function's.
	common.KeyDeprecationFor("canvas.sites[].route", common.KeyAlias, common.Deprecation{
		Old: "canvas.sites[].route",
		New: "canvas.sites[].path",
	}),

	// `edge` is the one home of every fact about how a slice is reached: what it
	// may call out to, the hosts it answers on, how fast it may be called. These
	// three were written elsewhere first, and each MOVES there unchanged.
	common.KeyDeprecationFor("atomic.egress", common.KeyAlias, common.Deprecation{
		Old: "atomic.egress",
		New: "edge.egress",
	}),
	common.KeyDeprecationFor("domains", common.KeyAlias, common.Deprecation{
		Old: "domains",
		New: "edge.domains",
	}),
	// An alias rather than an ignored shape key: `edge.rate_limit` SETS the
	// slice's requests per minute, within the tier's ceiling, so the value an old
	// file carries now takes effect.
	common.KeyDeprecationFor("atomic.rate_limit", common.KeyAlias, common.Deprecation{
		Old: "atomic.rate_limit",
		New: "edge.rate_limit",
	}),
}, shapeKeys...)

// shapeKeys are the capacity keys the slice form owns.
//
// Every one is IGNORED rather than aliased, because none has a target here: the
// value did not move to another key in this file, it moved to a different owner
// entirely. The walker warns and leaves the value where it is — nothing reads
// it, the schema still accepts it, and removing it would change what
// `drift file lint` is looking at.
//
// One notice per PATH, not per occurrence: `Warn` keys on `Old`, so prorata's
// nineteen `memory` declarations produce one line.
//
// They stay in the schema. This is the TOLERATE step; removing them is a later
// change, gated on no reachable Driftfile still writing the old shape — a
// countable fact while the fleet is eight slices, not a judgement.
var shapeKeys = []common.KeyDeprecation{
	ignoredShapeKey("log_retention", "Log retention is a slice setting"),
	ignoredShapeKey("backup_retention", "Backup retention is a slice setting"),

	ignoredShapeKey("atomic.atomic_size", "Atomic storage is a slice setting"),
	ignoredShapeKey("atomic.function_timeout", "The function timeout is a slice setting"),
	ignoredShapeKey("atomic.deploy_history", "Deploy history depth is a slice setting"),
	// A Pulse's booking is the sharpest of these: it is that Pulse's own
	// admission pool AND what it is billed at, so it is chosen where the slice is
	// sized rather than where the Pulse is declared.
	//
	// A Field has no entry here because the format never gave it a `memory`
	// key: its booking has always been the slice shape's.
	ignoredShapeKey("atomic.pulses[].memory", "A pulse's memory booking is a slice setting"),

	ignoredShapeKey("backbone.blob_max_size", "Blob size limits are a slice setting"),
	ignoredShapeKey("backbone.blob_max_count", "Blob count limits are a slice setting"),
	ignoredShapeKey("backbone.queue_max_depth", "Queue depth is a slice setting"),
	ignoredShapeKey("backbone.secret_max_size", "Secret size limits are a slice setting"),
	ignoredShapeKey("backbone.locks", "The lock budget is a slice setting"),
	ignoredShapeKey("backbone.realtime_connections", "The realtime budget is a slice setting"),

	// The per-item sizes. Each entry still NAMES the slot it seeds; what it no
	// longer does is say how big that slot is.
	ignoredShapeKey("backbone.nosql[].size", "A collection's size is a slice setting"),
	ignoredShapeKey("backbone.sql[].size", "A database's size is a slice setting"),
	ignoredShapeKey("backbone.blobs[].size", "A bucket's size is a slice setting"),

	ignoredShapeKey("canvas.canvas_size", "Canvas storage is a slice setting"),
}

// keyNotices is who hears about what the walker meets. A person hears through
// the shared once-per-name notices; `drift file migrate` runs the same walk
// silently, as the reference for what the file means, and only counts what it
// would have said.
type keyNotices struct {
	retired       func(common.KeyDeprecation)
	bothSpellings func(common.KeyDeprecation)
}

// toThePerson is the notices `drift file apply` prints.
var toThePerson = keyNotices{
	retired:       common.KeyDeprecation.Warn,
	bothSpellings: common.KeyDeprecation.WarnBothSpellings,
}

// ignoredShapeKey builds one capacity-key deprecation, so the entries cannot
// drift into as many slightly different sentences.
//
// RemoveAfter is a condition rather than a version, matching the house style: a
// version named in a notice promises a release nobody has planned.
func ignoredShapeKey(path, because string) common.KeyDeprecation {
	return common.KeyDeprecationFor(path, common.KeyIgnored, common.Deprecation{
		Old:         path,
		RemoveAfter: "every live slice's shape is owned by the slice form",
		Because:     because + ", chosen on the form `drift slice resize` draws; this file does not set it, and the value here is ignored.",
	})
}

// applyKeyDeprecations walks each declared path, warns once per path, and
// rewrites the aliases in place — at the document root and inside every
// `environments.<env>` overlay.
//
// The overlays are walked because they are MERGED onto the base. An overlay left
// in the old spelling reaches SelectEnvironment beside a base in the new one,
// the deep merge keeps both, and a reader of the new spelling misses the
// override. Each overlay is walked as a root of its own, so a move lands inside
// that overlay, where the merge picks it up.
//
// A document with no `environments` key — the resolved slice, which has the
// siblings removed — walks its root alone.
func applyKeyDeprecations(doc Node, keys []common.KeyDeprecation) {
	walkKeyDeprecations(doc, keys, toThePerson)
}

// walkKeyDeprecations is applyKeyDeprecations with the listener chosen by the
// caller, so one walk serves both the notices and migrate's silent reference.
func walkKeyDeprecations(doc Node, keys []common.KeyDeprecation, tell keyNotices) {
	for _, root := range documentRoots(doc) {
		for _, key := range keys {
			parent, leaf, ok := splitKeyPath(key.Path)
			if !ok {
				continue
			}
			for _, holder := range holdersAt(root, parent) {
				applyKeyTo(root, holder, parent, leaf, key, tell)
			}
		}
	}
}

// documentRoots is the document itself followed by every `environments.<env>`
// overlay that is an object: each is a root of its own for the walkers.
func documentRoots(doc Node) []Node {
	roots := []Node{doc}
	for _, env := range doc.Keys("environments") {
		if overlay, ok := asNode(doc.Sub("environments")[env]); ok {
			roots = append(roots, overlay)
		}
	}
	return roots
}

// applyKeyTo acts on one object that actually carries the retired key. root is
// the document (or overlay) the holder belongs to, which is where a move to a
// different parent resolves its destination.
func applyKeyTo(root, holder Node, parent, leaf string, key common.KeyDeprecation, tell keyNotices) {
	value, present := holder[leaf]
	if !present {
		return
	}

	// Warn before rewriting, so the notice is about the document the person
	// wrote rather than the one this pass produced.
	tell.retired(key)

	if key.Kind != common.KeyAlias || key.New == "" {
		// IGNORED: the value has no target here. Left exactly where it is —
		// nothing downstream reads it, the schema still accepts it, and removing
		// it would change what `drift file lint` is looking at.
		return
	}

	// New is a PATH, because that is what the notice has to say — "use
	// backbone.nosql[].slot" locates the key, "use slot" does not. Split it into
	// the container and the leaf rather than writing the path as a key: a nested
	// alias otherwise creates a key literally named `backbone.nosql[].slot`,
	// which every reader misses and no error reports.
	newParent, newLeaf, ok := splitKeyPath(key.New)
	if !ok {
		return
	}

	// The same parent is a rename inside this holder, which is the only shape
	// that can address list elements: each element renames its own key.
	// A different parent is a MOVE, resolved from the root and created where it
	// is missing — `domains` → `edge.domains` in a file with no `edge:` block.
	dest := holder
	if newParent != parent {
		dest, ok = objectAt(root, newParent)
		if !ok {
			// Nowhere to put it: a non-object sits on the destination path, or
			// the path runs through a list. The schema is what refuses that
			// document; dropping the value here would only hide it.
			return
		}
	}

	// A document stating BOTH spellings is contradicting itself, and the new one
	// is what it means. Overwriting it with the retired value would make the
	// deprecated key win, which is backwards — and dropping the old value
	// silently would leave its author believing it is in force.
	if _, already := dest[newLeaf]; already {
		tell.bothSpellings(key)
		delete(holder, leaf)
		return
	}
	dest[newLeaf] = value
	delete(holder, leaf)
}

// objectAt resolves a dotted path of OBJECTS from root, creating each one that
// is absent or null, and reports false when a segment holds something else or
// addresses a list.
//
// A list has no single element to move a key into, so `[]` is refused rather
// than guessed at; TestEveryCrossParentAliasAddressesNoList keeps the registered
// aliases clear of that shape.
func objectAt(root Node, path string) (Node, bool) {
	if path == "" {
		return root, true
	}
	cur := root
	for _, segment := range strings.Split(path, ".") {
		if strings.HasSuffix(segment, "[]") {
			return nil, false
		}
		existing, present := cur[segment]
		if !present || existing == nil {
			next := Node{}
			cur[segment] = next
			cur = next
			continue
		}
		next, ok := asNode(existing)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// splitKeyPath separates a path into the container it addresses and the leaf key
// inside it: `atomic.functions[].memory` is (`atomic.functions[]`, `memory`).
func splitKeyPath(path string) (parent, leaf string, ok bool) {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return "", path, path != ""
	}
	return path[:idx], path[idx+1:], idx+1 < len(path)
}

// holdersAt resolves a dotted parent path to every object that could carry the
// leaf, expanding `[]` into each element of a list.
//
// An element that is not a map is SKIPPED rather than reported. Both entry forms
// are live — `canvas: ["./canvas"]` leaves a bare string where a map would be,
// and the section expander does not touch it — and a bare string has no key to
// rewrite, so skipping is the correct reading rather than a gap.
func holdersAt(doc Node, parent string) []Node {
	holders := []Node{doc}
	if parent == "" {
		return holders
	}

	for _, segment := range strings.Split(parent, ".") {
		key, isList := strings.CutSuffix(segment, "[]")

		var next []Node
		for _, holder := range holders {
			value, present := holder[key]
			if !present {
				continue
			}
			if !isList {
				if sub, ok := asNode(value); ok {
					next = append(next, sub)
				}
				continue
			}
			items, ok := value.([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				if sub, ok := asNode(item); ok {
					next = append(next, sub)
				}
			}
		}
		holders = next
		if len(holders) == 0 {
			return nil
		}
	}
	return holders
}
