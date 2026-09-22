package file

// schemawalk.go — walking the Driftfile JSON Schema, for `drift file explain`.
//
// The schema is not a bare type definition. Its own $comment says the
// documentation lives in it, and it carries `description`, `default`,
// `examples`, `enum`, `required` and `pattern` throughout — so explaining a key
// is RENDERING data the machine already holds, not restating it. That is the
// whole point: the prose cannot drift from the format, because they are one
// document.
//
// # Three shapes stand between a path and the fields under it
//
// $ref, 24 times. A naive walk into `properties.atomic.properties` finds
// `{"$ref": …}` and reports no fields at all — not an error, just an empty
// answer, which is the worst kind.
//
// allOf, and this one is easy to miss because the card that asked for this
// command did not mention it. `properties.atomic` is `allOf: [{$ref:
// atomicSection}, {required: [functions]}]` — the fields are behind the ref and
// the REQUIRED list is in a sibling branch, so a walker that handles $ref alone
// finds the fields and reports none of them as required.
//
// oneOf, 9 times, and it is where the shorthands live: `queues: [validate]` and
// the object form are both legal. Rendering one branch makes this command
// contradict what `drift file lint` accepts, so both are shown.
//
// # Arrays are walked through, not into
//
// `backbone.nosql` is an array of objects, and a user asking about
// `backbone.nosql.size` means the field on an entry. kubectl behaves the same
// way, and the alternative — making people write `nosql.items.size` — exposes
// a JSON Schema keyword in a tool whose job is to hide it.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxFlatten bounds $ref following. The schema is a tree today, but a $ref
// cycle is one edit away and an unbounded resolver answers it with a stack
// overflow rather than a message.
const maxFlatten = 32

// schemaNode is one subschema, kept as a generic map because the document is
// the authority and this file must not hold a second model of it. A struct here
// would need a field per keyword and would silently drop the next one added.
type schemaNode map[string]any

// parseSchema reads the cached schema document.
func parseSchema(raw []byte) (schemaNode, error) {
	var root schemaNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("the cached Driftfile schema is not valid JSON: %w", err)
	}
	return root, nil
}

// sub returns a child map, or nil when the key is absent or not a map.
func sub(n schemaNode, key string) schemaNode {
	if n == nil {
		return nil
	}
	if m, ok := n[key].(map[string]any); ok {
		return schemaNode(m)
	}
	return nil
}

// str returns a string field, or "".
func str(n schemaNode, key string) string {
	if n == nil {
		return ""
	}
	s, _ := n[key].(string)
	return s
}

// strList returns a []string field, or nil. JSON numbers and bools in an enum
// are rendered with %v rather than dropped — an enum of ints is legal and
// silently omitting its values would be worse than printing them.
func strList(n schemaNode, key string) []string {
	if n == nil {
		return nil
	}
	raw, ok := n[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// resolveRef follows a local "#/definitions/x" pointer. Only local refs are
// followed: the Driftfile schema has no remote ones, and a resolver that
// reached the network would make an offline command online.
func resolveRef(root schemaNode, ref string) schemaNode {
	const prefix = "#/"
	if !strings.HasPrefix(ref, prefix) {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(strings.TrimPrefix(ref, prefix), "/") {
		if seg == "" {
			continue
		}
		cur = sub(cur, seg)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// flatten turns a subschema into the effective one: $ref followed, allOf
// branches merged.
//
// MERGE ORDER IS EARLIEST-WINS for scalars and UNION for the two collections
// that matter. A later branch adding `required: [functions]` must not erase the
// properties the earlier branch brought in, and a branch carrying its own
// description must not overwrite one already stated.
func flatten(root, n schemaNode, depth int) schemaNode {
	if n == nil || depth > maxFlatten {
		return n
	}
	if ref := str(n, "$ref"); ref != "" {
		target := resolveRef(root, ref)
		if target == nil {
			return n
		}
		// Merge rather than replace: a node can carry both a $ref and its own
		// description, and the local one is the more specific of the two.
		merged := flatten(root, target, depth+1)
		out := schemaNode{}
		for k, v := range merged {
			out[k] = v
		}
		for k, v := range n {
			if k == "$ref" {
				continue
			}
			out[k] = v
		}
		return out
	}

	branches, ok := n["allOf"].([]any)
	if !ok {
		return n
	}
	out := schemaNode{}
	for k, v := range n {
		if k != "allOf" {
			out[k] = v
		}
	}
	props := map[string]any{}
	var required []any
	if p, ok := out["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	if r, ok := out["required"].([]any); ok {
		required = append(required, r...)
	}
	for _, b := range branches {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		fb := flatten(root, schemaNode(bm), depth+1)
		for k, v := range fb {
			switch k {
			case "properties":
				if p, ok := v.(map[string]any); ok {
					for pk, pv := range p {
						if _, seen := props[pk]; !seen {
							props[pk] = pv
						}
					}
				}
			case "required":
				if r, ok := v.([]any); ok {
					required = append(required, r...)
				}
			default:
				if _, seen := out[k]; !seen {
					out[k] = v
				}
			}
		}
	}
	if len(props) > 0 {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = dedupeAny(required)
	}
	return out
}

func dedupeAny(in []any) []any {
	seen := map[string]bool{}
	out := make([]any, 0, len(in))
	for _, v := range in {
		k := fmt.Sprintf("%v", v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// variants returns the accepted forms of a node: one entry normally, several
// when the node is a oneOf.
//
// BOTH FORMS OR THE OUTPUT LIES. `queues: [validate]` and the object form are
// each legal, and a command that showed only the object would send someone to
// rewrite a manifest that already validates.
func variants(root, n schemaNode) []schemaNode {
	n = flatten(root, n, 0)
	raw, ok := n["oneOf"].([]any)
	if !ok {
		return []schemaNode{n}
	}
	out := make([]schemaNode, 0, len(raw))
	for _, b := range raw {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		v := flatten(root, schemaNode(bm), 0)
		// A oneOf branch usually carries no prose of its own; the description on
		// the parent is about the key, which is what the reader asked about.
		if str(v, "description") == "" && str(n, "description") != "" {
			cp := schemaNode{}
			for k, val := range v {
				cp[k] = val
			}
			cp["description"] = n["description"]
			v = cp
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return []schemaNode{n}
	}
	return out
}

// formsFor returns the accepted shapes of the thing a reader is asking about,
// walking through an array to its entries first.
//
// THE oneOf IS ON THE ITEMS, NOT ON THE ARRAY. `queues` is `[]` of
// `oneOf: [string, object]`, so asking the array for its variants finds one
// (itself) and asking it for its properties finds none — the command printed a
// type line and no fields at all for exactly the key whose two spellings it
// most needed to show.
func formsFor(root, n schemaNode) []schemaNode {
	n = flatten(root, n, 0)
	if str(n, "type") == "array" {
		if items := sub(n, "items"); items != nil {
			return variants(root, items)
		}
	}
	return variants(root, n)
}

// typeName renders a node's type in the vocabulary of the YAML someone writes,
// not JSON Schema's. `<[]object>` and `<map[string]string>` are what the
// Driftfile looks like; "array" and "additionalProperties" are what the
// document that describes it looks like, and the reader is not editing that.
func typeName(root, n schemaNode) string {
	n = flatten(root, n, 0)
	if raw, ok := n["oneOf"].([]any); ok && len(raw) > 0 {
		parts := make([]string, 0, len(raw))
		for _, v := range variants(root, n) {
			parts = append(parts, strings.Trim(typeName(root, v), "<>"))
		}
		return "<" + strings.Join(dedupeStrings(parts), " | ") + ">"
	}
	switch str(n, "type") {
	case "array":
		inner := "object"
		if items := sub(n, "items"); items != nil {
			inner = strings.Trim(typeName(root, items), "<>")
		}
		return "<[]" + inner + ">"
	case "object":
		if ap := sub(n, "additionalProperties"); ap != nil {
			return "<map[string]" + strings.Trim(typeName(root, ap), "<>") + ">"
		}
		return "<object>"
	case "":
		// A node with no `type` but with properties is an object in everything
		// but spelling; saying <> would be a shrug where an answer exists.
		if sub(n, "properties") != nil {
			return "<object>"
		}
		return "<any>"
	default:
		return "<" + str(n, "type") + ">"
	}
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// fieldsOf returns the child keys of a node, sorted, with required first.
//
// REQUIRED FIRST, then alphabetical. The reader's question is usually "what do
// I have to write", and an alphabetical list makes them cross-reference every
// entry against a required list printed somewhere else.
func fieldsOf(root, n schemaNode) []string {
	n = flatten(root, n, 0)
	if str(n, "type") == "array" {
		if items := sub(n, "items"); items != nil {
			return fieldsOf(root, items)
		}
	}
	props := sub(n, "properties")
	if props == nil {
		return nil
	}
	req := map[string]bool{}
	for _, r := range strList(n, "required") {
		req[r] = true
	}
	var required, optional []string
	for k := range props {
		if req[k] {
			required = append(required, k)
		} else {
			optional = append(optional, k)
		}
	}
	sort.Strings(required)
	sort.Strings(optional)
	return append(required, optional...)
}

// isRequired reports whether key is required on this node.
func isRequired(root, n schemaNode, key string) bool {
	n = flatten(root, n, 0)
	if str(n, "type") == "array" {
		if items := sub(n, "items"); items != nil {
			n = flatten(root, items, 0)
		}
	}
	for _, r := range strList(n, "required") {
		if r == key {
			return true
		}
	}
	return false
}

// child returns the subschema for one key, walking THROUGH an array to its
// entries rather than making the caller name `items`.
func child(root, n schemaNode, key string) (schemaNode, bool) {
	n = flatten(root, n, 0)
	if str(n, "type") == "array" {
		if items := sub(n, "items"); items != nil {
			n = flatten(root, items, 0)
		}
	}
	if props := sub(n, "properties"); props != nil {
		if c := sub(props, key); c != nil {
			return flatten(root, c, 0), true
		}
	}
	// A map-typed node explains its VALUE for any key, which is the honest
	// answer for `backbone.secrets.DATABASE_URL`: the schema says what a secret
	// value may be and nothing about that particular name.
	if str(n, "type") == "object" {
		if ap := sub(n, "additionalProperties"); ap != nil {
			return flatten(root, ap, 0), true
		}
	}
	// A oneOf whose branches disagree: answer from the first branch that has
	// the key, so `atomic.functions.route` resolves even though `functions`
	// accepts two shapes.
	for _, v := range variants(root, n) {
		if props := sub(v, "properties"); props != nil {
			if c := sub(props, key); c != nil {
				return flatten(root, c, 0), true
			}
		}
	}
	return nil, false
}

// walk follows a dotted path from the document root.
//
// On a miss it returns the node reached so far and the segment that failed, so
// the caller can suggest a near key at THAT level — the point of the command is
// discovery, and "no such field" with no next step fails the reader exactly
// when they are least able to guess.
func walk(root schemaNode, path string) (node schemaNode, at schemaNode, missing string, err error) {
	cur := flatten(root, root, 0)
	if strings.TrimSpace(path) == "" {
		return cur, nil, "", nil
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return nil, cur, path, fmt.Errorf("%q is not a path: it has an empty segment", path)
		}
		next, ok := child(root, cur, seg)
		if !ok {
			return nil, cur, seg, nil
		}
		cur = next
	}
	return cur, nil, "", nil
}

// nearest returns the field name closest to want, or "" when nothing is close.
//
// Bounded at a third of the word's length so a genuine typo suggests and an
// unrelated word does not — "nosq" should find `nosql`, "kubernetes" should
// find nothing rather than the alphabetically luckiest key.
func nearest(candidates []string, want string) string {
	best, bestD := "", 1<<30
	limit := len(want)/3 + 1
	for _, c := range candidates {
		d := editDistance(strings.ToLower(c), strings.ToLower(want))
		if d < bestD {
			best, bestD = c, d
		}
	}
	if bestD > limit {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
