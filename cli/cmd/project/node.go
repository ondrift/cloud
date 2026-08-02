// node.go — the Driftfile as data, for the one job that needs it.
//
// # What this is used for TODAY
//
// The ENVIRONMENT OVERLAY, and nothing else. `Manifest.mergeEnvironment` merges an
// environment's block onto the base as a document and decodes the result, because
// two bugs are unfixable in a typed merge and disappear in a document one:
//
//   - **Values that could not be overridden to zero.** Four hand-written mergers
//     gated on `if overlay.X != ""` / `!= 0`, so `deploy_history: 0` or
//     `wildcard: false` in an environment block was indistinguishable from an
//     absent key and was discarded — the Driftfile said one thing and the slice
//     was another, with no error anywhere. YAML gives key PRESENCE; a struct does
//     not (#CLI-STANDARDUSAGE-T9914R).
//   - **Merges that silently forgot.** Those mergers went field by field, so a new
//     Driftfile field was not overridable until someone added a clause — no
//     compiler error, no failing test. `mergeCanvas` covered two fields.
//     DeepMerge cannot have that bug because it does not know what a field is.
//
// # What this is NOT, however much the shape suggests it
//
// The Driftfile is still **decoded into typed structs**, and `ParseDriftfile` still
// re-decodes with `KnownFields(true)` to reject an unrecognised key. Carrying the
// whole file as a document — so the platform could add a field an installed CLI has
// never been taught, instead of that being a hard error — is a plausible direction
// and is NOT what happens.
//
// Nor is anything validated against the schema. The CLI fetches and caches it
// (`common/driftfile.go`) and reads it for no purpose yet; there is no JSON-schema
// library in this module. `drift file lint` (#CLI-STANDARDUSAGE-RKN51F) is where
// that would land.
//
// This paragraph exists because the file previously described both of those as
// already true. A comment in the present tense about work that has not happened is
// worse than no comment: it tells the next reader that the bug in front of them
// cannot exist.
package project

// Node is one object in the Driftfile document.
//
// Accessors are total — a missing or wrongly-typed path yields the zero value
// rather than an error. That is safe for the overlay merge, which reads no values
// (DeepMerge only walks keys), and it is why these accessors have no callers on the
// deploy path: the typed parse is what reads values, and it errors properly.
//
// Anything that starts READING through these accessors needs validation in front of
// it first, or a typo becomes a silent zero.
type Node map[string]any

// Get walks a path and returns the raw value, or nil.
func (n Node) Get(path ...string) any {
	cur := any(n)
	for _, key := range path {
		m, ok := asNode(cur)
		if !ok {
			return nil
		}
		cur, ok = m[key]
		if !ok {
			return nil
		}
	}
	return cur
}

// Has reports whether a path is present — including when its value is the zero
// value for its type. This is the distinction a struct could not make, and the
// reason environment overrides can now set something back to 0 or false.
func (n Node) Has(path ...string) bool {
	if len(path) == 0 {
		return false
	}
	parent, ok := asNode(n.Get(path[:len(path)-1]...))
	if !ok {
		return false
	}
	_, present := parent[path[len(path)-1]]
	return present
}

// Str returns a string value, or "".
func (n Node) Str(path ...string) string {
	s, _ := n.Get(path...).(string)
	return s
}

// Int returns an integer value, or 0. YAML numbers decode as int, and JSON
// round-trips can produce float64, so both are accepted.
func (n Node) Int(path ...string) int {
	switch v := n.Get(path...).(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// Bool returns a boolean value, or false.
func (n Node) Bool(path ...string) bool {
	b, _ := n.Get(path...).(bool)
	return b
}

// Sub returns a nested object, or an EMPTY Node — never nil — so callers can
// chain, range, and len() without a nil check at every level. Writes go
// through Set; assigning into the result of a missed Sub would land in a
// throwaway.
func (n Node) Sub(path ...string) Node {
	if m, ok := asNode(n.Get(path...)); ok {
		return m
	}
	return Node{}
}

// List returns a raw list, or nil.
func (n Node) List(path ...string) []any {
	l, _ := n.Get(path...).([]any)
	return l
}

// Nodes returns a list of objects, skipping any element that is not one. Every
// list the Driftfile takes has already been normalised by expandEntryShorthands
// at parse time, so a bare string has become an object before this runs.
func (n Node) Nodes(path ...string) []Node {
	raw := n.List(path...)
	out := make([]Node, 0, len(raw))
	for _, item := range raw {
		if m, ok := asNode(item); ok {
			out = append(out, m)
		}
	}
	return out
}

// Strings returns a list of strings, skipping non-strings.
func (n Node) Strings(path ...string) []string {
	raw := n.List(path...)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Keys returns the keys of a nested object. Order is not guaranteed; callers
// that render to a user sort them.
func (n Node) Keys(path ...string) []string {
	m := n.Sub(path...)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Set writes a value at a path, creating intermediate objects as needed.
func (n Node) Set(value any, path ...string) {
	if len(path) == 0 {
		return
	}
	cur := n
	for _, key := range path[:len(path)-1] {
		next, ok := asNode(cur[key])
		if !ok {
			next = Node{}
			cur[key] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

// Clone returns a deep copy, so a merge never mutates the document it read.
func (n Node) Clone() Node {
	out := make(Node, len(n))
	for k, v := range n {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case Node:
		return t.Clone()
	case map[string]any:
		return Node(t).Clone()
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return v
	}
}

// DeepMerge returns base with overlay applied on top.
//
// The rules, and why:
//
//   - A key PRESENT in overlay wins, whatever its value. Presence is the
//     signal, not truthiness — that is what makes `deploy_history: 0` and
//     `wildcard: false` expressible in an environment override, which the
//     struct-based mergers could not do.
//   - Two objects merge recursively, so an environment can override
//     `atomic.function_memory` without restating the rest of `atomic`.
//   - A list REPLACES wholesale. An environment that lists functions means
//     exactly those functions; element-wise merging of a list has no
//     defensible identity rule and would surprise more than it helped. This
//     matches what the old mergers did (`if len(overlay.X) > 0`), minus their
//     inability to express an intentionally empty list.
func DeepMerge(base, overlay Node) Node {
	out := base.Clone()
	for key, ov := range overlay {
		if sub, ok := asNode(ov); ok {
			if existing, ok := asNode(out[key]); ok {
				out[key] = DeepMerge(existing, sub)
				continue
			}
		}
		out[key] = cloneValue(ov)
	}
	return out
}

// asNode accepts both shapes a decoded object arrives in: Node from our own
// code, map[string]any from a YAML or JSON decode.
func asNode(v any) (Node, bool) {
	switch t := v.(type) {
	case Node:
		return t, true
	case map[string]any:
		return Node(t), true
	}
	return nil, false
}
