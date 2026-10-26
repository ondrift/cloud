package project

// functionidentity.go — one identity for a Function.
//
// A Function's label is its method and route, and the compiler writes them as
// `method` and `route`. Every reader downstream reads the composite
// `method:route` from `name`, so it is composed here, once.
//
// The composite string IS the booking key, and it is spoken by three things
// outside this binary: the slice composes it as lower(method) + ":" + route and
// looks it up exactly, the operator renders the same string into the slice's
// budget envelope, and the slice parses it back. A key the pool map does not
// hold is NOT refused — the lookup falls back to the shared pool, with no log
// line and no metric — so this composition has to agree with the slice's byte
// for byte.

import "strings"

// normaliseFunctionIdentities gives every Function entry its booking key in
// `name`, composed from its method and route.
//
// A Worker needs nothing here. Its identity is its `name`, which is already the
// one string the slot is booked under once WorkerSpecs prefixes it.
func normaliseFunctionIdentities(doc Node) {
	for _, fn := range doc.Nodes("atomic", functionsKey) {
		route, method := fn.Str("route"), fn.Str("method")
		if route != "" && method != "" {
			fn["name"] = strings.ToLower(method) + ":" + route
		}
	}
}
