package project

// concurrency.go — a Pulse declares only that it is concurrent.
//
// Driftfile 1.23.0 replaced `concurrency: N` with `concurrent: true`: whether a
// handler is safe to run in parallel is the author's to say, and how many of
// its calls run at once is the slice's to decide. The count stays accepted,
// and FunctionSpec reads it as concurrent above 1 (FunctionSpec.IsConcurrent);
// this file says, once per run, what to write instead.

import "github.com/ondrift/cloud/cli/common"

// concurrencyDeprecation is what a Driftfile still writing the count is told.
//
// Emitted here rather than through driftfileKeyDeprecations because that
// walker RENAMES a key and copies its value, and this one changes type: a
// number becomes a flag. Going through Warn still records it in the
// deprecation registry, so it lists with the rest.
//
// No RemoveAfter: naming a version promises a release nobody has planned.
var concurrencyDeprecation = common.Deprecation{
	Old: "atomic.pulses[].concurrency",
	New: "atomic.pulses[].concurrent",
	Because: "The slice decides how many calls of a Pulse run at once; " +
		"`concurrency` above 1 reads as `concurrent: true`, and 1 as `false`.",
}

// warnDeprecatedConcurrency calls retired once if any Pulse of doc, top level
// or flattened out of an element, writes the deprecated count.
func warnDeprecatedConcurrency(doc Node, retired func()) {
	for _, fn := range PulseEntries(doc) {
		if fn.Has("concurrency") {
			retired()
			return
		}
	}
}
