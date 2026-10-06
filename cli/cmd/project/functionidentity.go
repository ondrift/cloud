package project

// functionidentity.go — one identity for a Pulse, however it was spelled.
//
// A Pulse declares `route` and `method`. The retired spelling is a single
// `name: post:auth/challenge`, and it keeps parsing.
//
// # Why this normalises instead of the readers being repointed
//
// The composite `method:route` string IS the booking key, and it is spoken by
// three things outside this binary: the slice composes it as
// lower(method) + ":" + route and looks it up exactly, the operator renders the
// same string into the slice's budget envelope, and the slice parses it back.
// A key the pool map does not hold is NOT refused — the lookup falls back to the
// shared pool, with no log line and no metric — so a spelling change that
// altered that string by one byte would silently unbook every function.
//
// Both spellings therefore converge on one string here, and the ten readers
// downstream keep reading the one they already read. That also bounds the blast
// radius: those accessors are TOTAL, so a reader left on a key that stopped
// being written yields "" with no error anywhere. `alerts.go` is the sharp end —
// it names each alert `<function>-<index>`, so every declared alert would become
// "-0"/"-1" while every live alert fell into the delete loop, and one apply would
// wipe the slice's alert registry.

import (
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// nameDeprecation is what a Driftfile written in the retired spelling is told,
// once per run however many entries use it.
//
// Emitted here rather than through driftfileKeyDeprecations because that walker
// RENAMES a key and this is a SPLIT — one string becomes two, and the derivation
// belongs to the normaliser. Going through Warn still records it in the
// deprecation registry, so it lists with the rest.
//
// No RemoveAfter: naming a version promises a release nobody has planned.
var nameDeprecation = common.Deprecation{
	Old:     functionNamePath,
	New:     "atomic.pulses[].route + atomic.pulses[].method",
	Because: common.CapitalizeFirst(common.MigrateHint) + ".",
}

// normaliseFunctionIdentities gives every Pulse entry both spellings, agreeing
// with each other.
//
// Runs after schema validation and after the key walker, so it only ever sees a
// legal document with `atomic.functions` already renamed to `atomic.pulses`: an
// entry reaching here has either the pair or the retired string, and the pair's
// method is one of the eight the enum admits.
//
// A Field needs nothing here. Its identity is its `name`, which is already the
// one string the slot is booked under once FieldSpecs prefixes it.
func normaliseFunctionIdentities(doc Node) {
	normaliseIdentities(doc, nameDeprecation.Warn)
}

// normaliseIdentities is normaliseFunctionIdentities with the notice chosen by
// the caller, so `drift file migrate` can run the same normalisation silently.
func normaliseIdentities(doc Node, retired func()) {
	for _, fn := range doc.Nodes("atomic", pulsesKey) {
		route, method := fn.Str("route"), fn.Str("method")

		// The current spelling wins when a document carries both. Writing both
		// is a document contradicting itself, and the pair is what it means —
		// deriving from the retired string instead would make the old name win.
		if route != "" && method != "" {
			fn["name"] = strings.ToLower(method) + ":" + route
			continue
		}

		name := fn.Str("name")
		if name == "" {
			continue
		}
		retired()

		// Split on the FIRST colon, the same cut FunctionSpec.Wire makes, so a
		// route carrying colons of its own — `get:groups/:id` — keeps them.
		m, r, found := strings.Cut(name, ":")
		if !found {
			continue
		}
		fn["method"], fn["route"] = m, r
	}
}
