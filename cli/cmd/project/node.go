// node.go — the compiled Driftfile document, as data.
//
// `Manifest` holds the document the HCL Driftfile compiles to (compile.go):
// the slice's resources as the platform's schema describes them. There are no
// Go structs for `atomic`, `backbone` or `canvas` in it, because the platform
// publishes the definition of that document and a second one here could only
// disagree with it.
//
// # An unrecognised key is the SCHEMA's to refuse
//
// Nothing here rejects one. The compile validates the document against the
// schema the platform serves (`schema.go`), whose sections are
// `additionalProperties: false`, so a key the platform does not accept is
// refused there, by the side that defines what a key is.
package project

// Node is one object in the Driftfile document.
//
// Accessors are total — a missing or wrongly-typed path yields the zero value
// rather than an error. That is safe because every document read through them
// has been compiled and validated first; anything that reads a Node from
// anywhere else needs validation in front of it, or a typo becomes a silent
// zero.
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
// value for its type, which is how an empty `allow = []` is told apart from no
// `allow` at all.
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

// Int returns an integer value, or 0. The compiler writes int, and JSON
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

// Nodes returns a list of objects, skipping any element that is not one.
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

// Clone returns a deep copy, so a pass that rewrites the slice never mutates the
// document it read.
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

// asNode accepts both shapes an object arrives in: Node from our own code,
// map[string]any from the compiler or a JSON decode.
func asNode(v any) (Node, bool) {
	switch t := v.(type) {
	case Node:
		return t, true
	case map[string]any:
		return Node(t), true
	}
	return nil, false
}

// Entries reads a list whose elements may be a bare STRING or a map, and yields
// them uniformly as Nodes — a bare string becoming {scalarKey: value}.
//
// The compiler writes `backbone.queues` as bare names and every other list as
// objects; reading them all through here keeps one accessor for every list the
// document has. scalarKey is what a bare string MEANS for that list: a queue's
// is its `name`.
func (n Node) Entries(scalarKey string, path ...string) []Node {
	items := n.List(path...)
	out := make([]Node, 0, len(items))
	for _, it := range items {
		if sub, ok := asNode(it); ok {
			out = append(out, sub)
			continue
		}
		if s, ok := it.(string); ok {
			out = append(out, Node{scalarKey: s})
		}
	}
	return out
}

// StrMap reads a map whose values are plain strings — `backbone.secrets`.
// A non-string value is skipped rather than coerced, because the schema has
// already decided what belongs there.
func (n Node) StrMap(path ...string) map[string]string {
	src := n.Sub(path...)
	out := make(map[string]string, len(src))
	for k, v := range src {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// EntryMap is Entries for a MAP whose VALUES may be a bare string or a map,
// such as `backbone.cache`.
func (n Node) EntryMap(scalarKey string, path ...string) map[string]Node {
	src := n.Sub(path...)
	out := make(map[string]Node, len(src))
	for k, v := range src {
		if sub, ok := asNode(v); ok {
			out[k] = sub
			continue
		}
		if s, ok := v.(string); ok {
			out[k] = Node{scalarKey: s}
		}
	}
	return out
}
