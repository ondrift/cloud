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
	ignoredShapeKey("atomic.rate_limit", "The rate limit is a slice setting"),
	ignoredShapeKey("atomic.deploy_history", "Deploy history depth is a slice setting"),
	// A function's booking is the sharpest of these: it is that function's own
	// admission pool AND what it is billed at, so it is chosen where the slice is
	// sized rather than where the function is declared.
	ignoredShapeKey("atomic.functions[].memory", "A function's memory booking is a slice setting"),

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

// ignoredShapeKey builds one capacity-key deprecation, so seventeen entries
// cannot drift into seventeen slightly different sentences.
//
// RemoveAfter is a condition rather than a version, matching the house style: a
// version named in a notice promises a release nobody has planned.
func ignoredShapeKey(path, because string) common.KeyDeprecation {
	return common.KeyDeprecationFor(path, common.KeyIgnored, common.Deprecation{
		Old:         path,
		RemoveAfter: "every live slice's shape is owned by the slice form",
		Because:     because + ", chosen on the form `drift slice resize` draws — this file does not set it, and the value here is ignored.",
	})
}

// applyKeyDeprecations walks each declared path, warns once per path, and
// rewrites the aliases in place.
func applyKeyDeprecations(doc Node, keys []common.KeyDeprecation) {
	for _, key := range keys {
		parent, leaf, ok := splitKeyPath(key.Path)
		if !ok {
			continue
		}
		for _, holder := range holdersAt(doc, parent) {
			applyKeyTo(holder, leaf, key)
		}
	}
}

// applyKeyTo acts on one object that actually carries the retired key.
func applyKeyTo(holder Node, leaf string, key common.KeyDeprecation) {
	value, present := holder[leaf]
	if !present {
		return
	}

	// Warn before rewriting, so the notice is about the document the person
	// wrote rather than the one this pass produced.
	key.Deprecation.Warn()

	if key.Kind != common.KeyAlias || key.New == "" {
		// IGNORED: the value has no target here. Left exactly where it is —
		// nothing downstream reads it, the schema still accepts it, and removing
		// it would change what `drift file lint` is looking at.
		return
	}

	// New is a PATH, because that is what the notice has to say — "use
	// backbone.nosql[].slot" locates the key, "use slot" does not. The holder is
	// keyed by leaf, so take the leaf off it rather than writing the path as a
	// key: a nested alias otherwise creates a key literally named
	// `backbone.nosql[].slot`, which every reader misses and no error reports.
	_, newLeaf, ok := splitKeyPath(key.New)
	if !ok {
		return
	}

	// A document stating BOTH spellings is contradicting itself, and the new one
	// is what it means. Overwriting it with the retired value would make the
	// deprecated key win, which is backwards.
	if _, already := holder[newLeaf]; already {
		delete(holder, leaf)
		return
	}
	holder[newLeaf] = value
	delete(holder, leaf)
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
