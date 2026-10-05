package project

// migrate.go: rewriting a Driftfile's TEXT into the current spelling of the
// format, for `drift file migrate`.
//
// The walker in deprecatedkeys.go rewrites the DOCUMENT `drift file apply`
// reads, every time, in memory. This rewrites the FILE once, so the person
// stops hearing about it. Everything it knows comes from the same places: the
// registry (driftfileKeyDeprecations) says which key goes where, and the
// identity split (functionidentity.go) says how a function's `name` becomes
// `route` and `method`.
//
// # It edits the text, guided by the node tree
//
// The file is parsed into a yaml.Node tree, which is what says where each key
// and value sits, and the change is made to the LINES of the file at those
// positions. Re-encoding the tree would keep comments and key order but would
// still restyle the file: blank lines go, indentation and sequence style are
// normalised, and a person reviewing the diff could not find the three lines
// that matter among the hundred that moved. Editing the lines keeps every byte
// outside the keys being moved exactly as it was.
//
// # It proves what it wrote
//
// A text edit can go wrong in ways a tree edit cannot, so the result is checked
// before anyone sees it. Both files are decoded and read the way apply reads
// them, through the walker and the identity split themselves, run silently; the
// two readings must be the same document, and the rewritten file must leave the
// walker nothing to rewrite. A rewrite that fails either check is refused, and
// nothing is written.
//
// # What it refuses
//
// Every refusal names the line and what to do instead, and a refused file is
// left untouched:
//
//   - both spellings of one key set: apply uses the new one and ignores the old,
//     and migrate will not delete a value the person wrote;
//   - a key to move that sits in flow style (`{ ... }`) or inside a YAML anchor
//     that is used elsewhere, where a move would change every use of it;
//   - a destination that is not a section;
//   - a move that would put an alias before its anchor.

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"gopkg.in/yaml.v3"
)

// MigrationStep is one key rewritten into its current spelling.
type MigrationStep struct {
	// Line is where the key sits in the file as written, 1-based.
	Line int
	// Old and New are the key's path before and after, prefixed with
	// `environments.<env>.` inside an overlay.
	Old, New string
}

// KeptKey is a retired key migrate cannot move, because it has nowhere to go:
// its value moved to a different owner entirely. It stays in the file, and is
// reported with what to do instead.
type KeptKey struct {
	Line   int
	Path   string
	Advice string
}

// Migration is the result of rewriting one Driftfile.
type Migration struct {
	// Output is the rewritten file. With no steps it is the input, unchanged.
	Output []byte
	Steps  []MigrationStep
	Kept   []KeptKey
}

// Changed reports whether the file needs rewriting at all.
func (m *Migration) Changed() bool { return len(m.Steps) > 0 }

// MigrationRefusal is every reason a file could not be rewritten. All of them,
// rather than the first, so a person fixes the file in one pass.
type MigrationRefusal struct {
	Reasons []string
}

func (r *MigrationRefusal) Error() string {
	return "drift file migrate cannot rewrite this file, so nothing was changed:\n  - " +
		strings.Join(r.Reasons, "\n  - ")
}

// functionNamePath is the retired spelling the identity split owns.
const functionNamePath = "atomic.functions[].name"

// MigrateDriftfile rewrites a Driftfile's retired keys into their current
// spelling, at the top level and inside every environment overlay.
//
// The input should be a document the schema accepts, as it is for apply: the
// walker only ever sees a legal document, and migrate promises the same reading
// the walker would make. The caller validates; this rewrites and proves.
func MigrateDriftfile(src []byte) (*Migration, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return &Migration{Output: src}, nil
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("a Driftfile is a set of keys at its top level, and this one is not")
	}

	mg := newMigrator(src, &doc)
	roots := mg.rootsOf(top)
	for _, r := range roots {
		mg.planRoot(r)
	}
	if len(mg.refusals) > 0 {
		return nil, &MigrationRefusal{Reasons: mg.refusals}
	}

	sort.SliceStable(mg.kept, func(i, j int) bool { return mg.kept[i].Line < mg.kept[j].Line })
	if len(mg.steps) == 0 && len(mg.blocks) == 0 {
		return &Migration{Output: src, Kept: mg.kept}, nil
	}

	if err := mg.applyInline(); err != nil {
		return nil, err
	}
	for _, b := range mg.blocks {
		mg.emitBlock(b)
	}
	if len(mg.refusals) > 0 {
		return nil, &MigrationRefusal{Reasons: mg.refusals}
	}
	sort.SliceStable(mg.steps, func(i, j int) bool { return mg.steps[i].Line < mg.steps[j].Line })
	out := mg.render()

	if err := checkMigration(src, out); err != nil {
		return nil, err
	}
	return &Migration{Output: out, Steps: mg.steps, Kept: mg.kept}, nil
}

// ─── the plan ────────────────────────────────────────────────────────────────

// migrator holds the file as lines and the edits planned against them. Every
// position it records is in the file AS WRITTEN; the edits are applied in one
// pass at the end, so no edit moves another's coordinates.
type migrator struct {
	orig     []string // the file's lines, without their "\n"
	lines    []string // orig, after the edits made within a line
	newline  bool     // whether the file ended with "\n"
	crlf     bool     // whether lines end "\r\n", which created lines copy
	step     int      // the file's indentation unit, for a section migrate creates
	aliased  map[*yaml.Node]bool
	seen     map[*yaml.Node]bool
	inline   map[int][]inlineEdit
	deleted  map[int]bool
	inserts  map[int][]string // after line N; 0 is before the first line
	blocks   []moveBlock
	steps    []MigrationStep
	kept     []KeptKey
	refusals []string
}

// inlineEdit replaces bytes [start, end) of one line.
type inlineEdit struct {
	start, end int
	text       string
}

// migrationRoot is the document, or one environment overlay, which the walker
// treats as a root of its own.
type migrationRoot struct {
	node   *yaml.Node
	prefix string
}

// holder is a mapping that may carry a retired key.
type holder struct {
	node *yaml.Node
	// key names the mapping in its parent mapping; nil for a root or a list item.
	key *yaml.Node
	// chain is every mapping from the root down to node, inclusive.
	chain []*yaml.Node
	// viaAlias is whether an alias was followed to reach it.
	viaAlias bool
}

// pendingMove is one key leaving its holder for a section under the root.
type pendingMove struct {
	key      common.KeyDeprecation
	k, v     *yaml.Node
	from     holder
	newLeaf  string
	oldPath  string
	newPath  string
	collapse bool
}

// moveBlock is every move into one destination section of one root, and where
// the moved lines go.
type moveBlock struct {
	moves       []pendingMove
	anchor      int    // the moved lines go after this line of the original
	header      string // the destination's own key line, when migrate creates it
	blankBefore bool   // whether a created section is set off by a blank line
	childIndent int
}

func newMigrator(src []byte, doc *yaml.Node) *migrator {
	text := string(src)
	mg := &migrator{
		aliased: map[*yaml.Node]bool{},
		seen:    map[*yaml.Node]bool{},
		inline:  map[int][]inlineEdit{},
		deleted: map[int]bool{},
		inserts: map[int][]string{},
	}
	mg.newline = strings.HasSuffix(text, "\n")
	mg.orig = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	mg.crlf = len(mg.orig) > 0 && strings.HasSuffix(mg.orig[0], "\r")
	// A CRLF file with no final newline still ends its last line "\r" here, so
	// a line placed after it is separated by the file's own ending; render
	// takes the "\r" off again.
	if last := len(mg.orig) - 1; mg.crlf && !mg.newline && !strings.HasSuffix(mg.orig[last], "\r") {
		mg.orig[last] += "\r"
	}
	mg.lines = append([]string(nil), mg.orig...)
	markAliased(doc, mg.aliased)
	mg.step = indentStep(doc.Content[0])
	return mg
}

// rootsOf is the document followed by each environment overlay that is a
// mapping, matching documentRoots in the walker. An overlay reached twice
// through an alias is one root.
func (mg *migrator) rootsOf(top *yaml.Node) []migrationRoot {
	roots := []migrationRoot{{node: top}}
	_, envs := pairOf(top, "environments")
	envs, _ = resolveAlias(envs)
	if envs == nil || envs.Kind != yaml.MappingNode {
		return roots
	}
	seen := map[*yaml.Node]bool{top: true}
	for i := 0; i+1 < len(envs.Content); i += 2 {
		body, _ := resolveAlias(envs.Content[i+1])
		if body == nil || body.Kind != yaml.MappingNode || seen[body] {
			continue
		}
		seen[body] = true
		roots = append(roots, migrationRoot{node: body, prefix: "environments." + envs.Content[i].Value + "."})
	}
	return roots
}

// planRoot plans every rewrite inside one root, in registry order: renames and
// splits as edits within a line, moves as blocks emitted once those are made.
func (mg *migrator) planRoot(r migrationRoot) {
	var moves []pendingMove
	for _, key := range driftfileKeyDeprecations {
		parent, leaf, ok := splitKeyPath(key.Path)
		if !ok {
			continue
		}
		for _, h := range holdersIn(r.node, parent) {
			k, v := pairOf(h.node, leaf)
			if k == nil || mg.seen[k] {
				continue
			}
			mg.seen[k] = true
			oldPath := r.prefix + key.Path

			if key.Kind != common.KeyAlias || key.New == "" {
				mg.kept = append(mg.kept, KeptKey{Line: k.Line, Path: oldPath, Advice: key.Because})
				continue
			}
			newParent, newLeaf, ok := splitKeyPath(key.New)
			if !ok {
				continue
			}
			newPath := r.prefix + key.New
			if newParent == parent {
				mg.planRename(h, k, newLeaf, oldPath, newPath)
				continue
			}
			if m, ok := mg.planMove(r, h, k, v, key, newParent, newLeaf, oldPath, newPath); ok {
				moves = append(moves, m)
			}
		}
	}
	mg.planSplits(r)
	mg.planBlocks(r, moves)
}

// planRename renames a key in place, the one shape that can address every
// element of a list: each element renames its own key.
func (mg *migrator) planRename(h holder, k *yaml.Node, newLeaf, oldPath, newPath string) {
	if nk, _ := pairOf(h.node, newLeaf); nk != nil {
		mg.refuse(bothSpellings(oldPath, k.Line, newPath, nk.Line))
		return
	}
	if !mg.renameKey(k, newLeaf, oldPath) {
		return
	}
	mg.steps = append(mg.steps, MigrationStep{Line: k.Line, Old: oldPath, New: newPath})
}

// planMove checks that a key can leave its holder for a section under the root,
// and records it.
func (mg *migrator) planMove(r migrationRoot, h holder, k, v *yaml.Node, key common.KeyDeprecation,
	newParent, newLeaf, oldPath, newPath string,
) (pendingMove, bool) {
	// The registry's moves land one level under the root; a deeper destination
	// is a registry entry this code does not yet know how to create.
	if strings.ContainsAny(newParent, ".[]") {
		mg.refuse(fmt.Sprintf("%s (line %d) moves to %s, which drift file migrate cannot create; move it by hand.",
			oldPath, k.Line, newPath))
		return pendingMove{}, false
	}
	if h.viaAlias || mg.anyAliased(h.chain) {
		mg.refuse(fmt.Sprintf("%s (line %d) sits inside a YAML anchor that is used in more than one place, "+
			"so moving it would change every place the anchor is used; move it to %s by hand.",
			oldPath, k.Line, newPath))
		return pendingMove{}, false
	}
	if h.node.Style&yaml.FlowStyle != 0 {
		mg.refuse(fmt.Sprintf("%s (line %d) is written in flow style ({ ... }); write that section as an "+
			"indented block, then run drift file migrate again.", oldPath, k.Line))
		return pendingMove{}, false
	}
	if !onlySpacesBefore(mg.orig[k.Line-1], k.Column) {
		mg.refuse(fmt.Sprintf("%s (line %d) does not start its own line; move it to %s by hand.",
			oldPath, k.Line, newPath))
		return pendingMove{}, false
	}

	if dk, dv := pairOf(r.node, newParent); dk != nil {
		resolved, wasAlias := resolveAlias(dv)
		switch {
		case wasAlias || mg.aliased[resolved]:
			mg.refuse(fmt.Sprintf("%s (line %d) is a YAML anchor or alias, so adding %s to it would change "+
				"every place it is used; move %s by hand.", r.prefix+newParent, dk.Line, newLeaf, oldPath))
			return pendingMove{}, false
		case resolved.Kind == yaml.MappingNode:
			if nk, _ := pairOf(resolved, newLeaf); nk != nil {
				mg.refuse(bothSpellings(oldPath, k.Line, newPath, nk.Line))
				return pendingMove{}, false
			}
			if resolved.Style&yaml.FlowStyle != 0 && len(resolved.Content) > 0 {
				mg.refuse(fmt.Sprintf("%s (line %d) is written in flow style ({ ... }); write it as an indented "+
					"block, then run drift file migrate again.", r.prefix+newParent, dk.Line))
				return pendingMove{}, false
			}
		case resolved.Kind == yaml.ScalarNode && resolved.Tag == "!!null":
		default:
			mg.refuse(fmt.Sprintf("%s (line %d) is not a section, so %s has nowhere to go; "+
				"drift file lint names what is wrong with it.", r.prefix+newParent, dk.Line, oldPath))
			return pendingMove{}, false
		}
	}

	_, leaf, _ := splitKeyPath(key.Path)
	if newLeaf != leaf && !mg.renameKey(k, newLeaf, oldPath) {
		return pendingMove{}, false
	}
	return pendingMove{key: key, k: k, v: v, from: h, newLeaf: newLeaf, oldPath: oldPath, newPath: newPath}, true
}

// planSplits rewrites each function's retired `name: method:route` into the
// `route` and `method` pair, the derivation normaliseFunctionIdentities makes.
func (mg *migrator) planSplits(r migrationRoot) {
	oldPath := r.prefix + functionNamePath
	newPath := r.prefix + "atomic.functions[].route + method"
functions:
	for _, h := range holdersIn(r.node, "atomic.functions[]") {
		k, v := pairOf(h.node, "name")
		if k == nil || mg.seen[k] {
			continue
		}
		mg.seen[k] = true

		// Both spellings: apply uses the pair whenever both halves are set, and
		// derives the pair from `name` otherwise, so a half-set pair beside a
		// name is overwritten. Either way something the person wrote is
		// ignored, and migrate does not choose which for them.
		for _, half := range []string{"route", "method"} {
			if hk, _ := pairOf(h.node, half); hk != nil {
				mg.refuse(bothSpellings(oldPath, k.Line, r.prefix+"atomic.functions[]."+half, hk.Line))
				continue functions
			}
		}
		if v.Kind != yaml.ScalarNode || v.Line != k.Line {
			mg.refuse(fmt.Sprintf("%s (line %d) is not a single value on its own line; write it as "+
				"route: and method: by hand.", oldPath, k.Line))
			continue
		}
		method, route, found := strings.Cut(v.Value, ":")
		if !found {
			mg.refuse(fmt.Sprintf("%s (line %d) is %q, which is not method:route.", oldPath, k.Line, v.Value))
			continue
		}

		text := mg.orig[v.Line-1]
		vs := byteOffset(text, v.Column)
		ve, ok := tokenEnd(text, vs, v)
		if !ok {
			mg.refuse(fmt.Sprintf("%s (line %d) is written in a form drift file migrate does not rewrite; "+
				"write it as route: and method: by hand.", oldPath, k.Line))
			continue
		}
		if !mg.renameKey(k, "route", oldPath) {
			continue
		}
		routeText := requote(v.Style, route)
		if h.node.Style&yaml.FlowStyle != 0 {
			mg.addInline(v.Line, vs, ve, routeText+", method: "+method)
		} else {
			mg.addInline(v.Line, vs, ve, routeText)
			mg.inserts[k.Line] = append(mg.inserts[k.Line], mg.newLine(strings.Repeat(" ", k.Column-1)+"method: "+method))
		}
		mg.steps = append(mg.steps, MigrationStep{Line: k.Line, Old: oldPath, New: newPath})
	}
}

// planBlocks groups one root's moves by destination and decides where the
// moved lines go:
//
//   - into an existing section: after its last line, at its children's
//     indentation;
//   - into an empty one (`edge:`, `edge: ~`, `edge: {}`): under its key, with
//     the empty value removed;
//   - into one the root lacks: created where the first moved key (in file
//     order) was, if that key sat at the root, or directly after the section
//     holding it otherwise.
//
// A holder every key of which moves out is left as `{}`, which is what the
// walker leaves, rather than as an empty key the schema would refuse.
func (mg *migrator) planBlocks(r migrationRoot, moves []pendingMove) {
	if len(moves) == 0 {
		return
	}
	sort.SliceStable(moves, func(i, j int) bool { return moves[i].k.Line < moves[j].k.Line })

	byDest := map[string][]pendingMove{}
	var order []string
	for _, m := range moves {
		parent, _, _ := splitKeyPath(m.key.New)
		if _, ok := byDest[parent]; !ok {
			order = append(order, parent)
		}
		byDest[parent] = append(byDest[parent], m)
	}

	emptied := map[*yaml.Node]int{}
	for _, m := range moves {
		emptied[m.from.node]++
	}

	rootIndent := r.node.Content[0].Column - 1
	for _, parent := range order {
		group := byDest[parent]
		b := moveBlock{}

		dk, dv := pairOf(r.node, parent)
		resolved, _ := resolveAlias(dv)
		switch {
		case dk != nil && resolved.Kind == yaml.MappingNode && len(resolved.Content) > 0:
			b.childIndent = resolved.Content[0].Column - 1
			b.anchor = mg.pairSpan(dk, dv).end
		case dk != nil:
			b.childIndent = dk.Column - 1 + mg.stepFor(group, rootIndent)
			b.anchor = dk.Line
			mg.clearEmptyValue(dk, resolved, r.prefix+parent)
		default:
			b.childIndent = rootIndent + mg.stepFor(group, rootIndent)
			b.header = mg.newLine(strings.Repeat(" ", rootIndent) + parent + ":")
			first := group[0]
			if first.from.node == r.node {
				b.anchor = mg.pairSpan(first.k, first.v).start - 1
			} else {
				rk, rv := rootPairHolding(r.node, first.from.chain)
				if rk == nil {
					mg.refuse(fmt.Sprintf("%s (line %d) could not be placed under %s; move it by hand.",
						first.oldPath, first.k.Line, r.prefix+parent))
					continue
				}
				b.anchor = mg.pairSpan(rk, rv).end
				b.blankBefore = b.anchor < len(mg.orig) && isBlank(mg.orig[b.anchor])
			}
		}

		for i := range group {
			// The moved lines leave a gap only when they were not also the
			// place a created section went: that gap is where it now stands.
			group[i].collapse = !(b.header != "" && i == 0 && group[i].from.node == r.node)
		}
		b.moves = group
		mg.blocks = append(mg.blocks, b)
	}

	for node, n := range emptied {
		if node == r.node || n*2 != len(node.Content) {
			continue
		}
		for _, m := range moves {
			if m.from.node == node {
				mg.markEmpty(m.from)
				break
			}
		}
	}
}

// stepFor is the indentation a created or emptied section gives its children:
// the depth the moved keys already had under their old section when one of
// them came from below the root, and the file's own unit otherwise.
func (mg *migrator) stepFor(group []pendingMove, rootIndent int) int {
	for _, m := range group {
		if d := m.k.Column - 1 - rootIndent; len(m.from.chain) > 1 && d > 0 {
			return d
		}
	}
	return mg.step
}

// clearEmptyValue removes the `~`, `null` or `{}` an empty destination section
// carries, so children can follow its key.
func (mg *migrator) clearEmptyValue(dk, dv *yaml.Node, where string) {
	if dv.Kind == yaml.ScalarNode && dv.Value == "" {
		return // `edge:` with nothing after it already takes children
	}
	text := mg.orig[dv.Line-1]
	start := byteOffset(text, dv.Column)
	end := -1
	switch {
	case start < 0 || dv.Line != dk.Line:
	case dv.Kind == yaml.MappingNode:
		if i := strings.Index(text[start:], "}"); strings.HasPrefix(text[start:], "{") && i > 0 &&
			strings.TrimSpace(text[start+1:start+i]) == "" {
			end = start + i + 1
		}
	default:
		if e, ok := tokenEnd(text, start, dv); ok {
			end = e
		}
	}
	if end < 0 {
		mg.refuse(fmt.Sprintf("%s (line %d) is written in a form drift file migrate does not rewrite; "+
			"write it as an empty `%s:` line and run drift file migrate again.", where, dk.Line, dk.Value))
		return
	}
	for start > 0 && text[start-1] == ' ' {
		start--
	}
	mg.addInline(dv.Line, start, end, "")
}

// markEmpty writes `{}` after the key of a section every key moved out of.
func (mg *migrator) markEmpty(h holder) {
	if h.key == nil {
		return
	}
	text := mg.orig[h.key.Line-1]
	ks := byteOffset(text, h.key.Column)
	ke, ok := tokenEnd(text, ks, h.key)
	if ok {
		rest := strings.TrimLeft(text[ke:], " ")
		if strings.HasPrefix(rest, ":") {
			colon := len(text) - len(rest) + 1
			mg.addInline(h.key.Line, colon, colon, " {}")
			return
		}
	}
	mg.refuse(fmt.Sprintf("%s (line %d) would be left empty, and drift file migrate cannot rewrite its key; "+
		"move its keys by hand.", h.key.Value, h.key.Line))
}

// renameKey replaces a key's text with another name.
func (mg *migrator) renameKey(k *yaml.Node, to, where string) bool {
	text := mg.orig[k.Line-1]
	start := byteOffset(text, k.Column)
	end, ok := tokenEnd(text, start, k)
	if !ok {
		mg.refuse(fmt.Sprintf("%s (line %d) is written in a form drift file migrate does not rewrite; "+
			"rename it to %s by hand.", where, k.Line, to))
		return false
	}
	mg.addInline(k.Line, start, end, to)
	return true
}

func (mg *migrator) addInline(line, start, end int, text string) {
	mg.inline[line] = append(mg.inline[line], inlineEdit{start: start, end: end, text: text})
}

func (mg *migrator) refuse(reason string) { mg.refusals = append(mg.refusals, reason) }

// newLine finishes a line migrate creates with the file's own line ending.
func (mg *migrator) newLine(s string) string {
	if mg.crlf {
		return s + "\r"
	}
	return s
}

func (mg *migrator) anyAliased(chain []*yaml.Node) bool {
	for _, n := range chain {
		if mg.aliased[n] {
			return true
		}
	}
	return false
}

// bothSpellings is the refusal for a key written under its old and new name.
func bothSpellings(oldPath string, oldLine int, newPath string, newLine int) string {
	return fmt.Sprintf("%s (line %d) and %s (line %d) are both set. drift file apply uses %s and ignores %s; "+
		"delete line %d, or merge its value into %s by hand, then run drift file migrate again.",
		oldPath, oldLine, newPath, newLine, newPath, oldPath, oldLine, newPath)
}

// ─── the edits ──────────────────────────────────────────────────────────────

// applyInline makes every edit within a line, right to left so each keeps the
// coordinates it was planned at.
func (mg *migrator) applyInline() error {
	for line, edits := range mg.inline {
		sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
		text := mg.orig[line-1]
		limit := len(text)
		for _, e := range edits {
			if e.start < 0 || e.end < e.start || e.end > limit {
				return fmt.Errorf("drift file migrate planned overlapping edits on line %d, so nothing was written; "+
					"this is a bug in migrate", line)
			}
			text = text[:e.start] + e.text + text[e.end:]
			limit = e.start
		}
		mg.lines[line-1] = text
	}
	return nil
}

// emitBlock cuts each moved pair out of its holder and places it under its
// destination, re-indented, with the comments above it and on it.
func (mg *migrator) emitBlock(b moveBlock) {
	var out []string
	if b.header != "" {
		if b.blankBefore {
			out = append(out, mg.newLine(""))
		}
		out = append(out, b.header)
	}
	for _, m := range b.moves {
		sp := mg.pairSpan(m.k, m.v)
		for i := sp.start; i <= sp.end; i++ {
			if mg.deleted[i] {
				mg.refuse(fmt.Sprintf("%s (line %d) overlaps another key being moved; move it by hand.", m.oldPath, m.k.Line))
				return
			}
		}
		out = append(out, reindent(mg.lines[sp.start-1:sp.end], b.childIndent-(m.k.Column-1))...)
		mg.deleteSpan(sp, m.collapse)
		mg.steps = append(mg.steps, MigrationStep{Line: m.k.Line, Old: m.oldPath, New: m.newPath})
	}
	mg.inserts[b.anchor] = append(mg.inserts[b.anchor], out...)
}

// deleteSpan removes a moved pair's lines. With collapse, a blank line left
// standing between two blank-separated neighbours goes too, so a key moved out
// of the middle of a section does not leave a double gap.
func (mg *migrator) deleteSpan(sp span, collapse bool) {
	for i := sp.start; i <= sp.end; i++ {
		mg.deleted[i] = true
	}
	if !collapse {
		return
	}
	after := sp.end + 1
	if after <= len(mg.orig) && !isBlank(mg.orig[after-1]) && leadingSpaces(mg.orig[after-1]) >= leadingSpaces(mg.orig[sp.start-1]) {
		return // a sibling follows directly: the gap above is that sibling's
	}
	for i := sp.start - 1; i >= 1 && isBlank(mg.orig[i-1]) && !mg.deleted[i]; i-- {
		mg.deleted[i] = true
	}
}

// render writes the file: every line not deleted, with the insertions after
// the line they follow.
func (mg *migrator) render() []byte {
	var b strings.Builder
	emit := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, l := range mg.inserts[0] {
		emit(l)
	}
	for i := 1; i <= len(mg.lines); i++ {
		if !mg.deleted[i] {
			emit(mg.lines[i-1])
		}
		for _, l := range mg.inserts[i] {
			emit(l)
		}
	}
	out := b.String()
	if !mg.newline {
		out = strings.TrimSuffix(out, "\n")
		if mg.crlf {
			out = strings.TrimSuffix(out, "\r")
		}
	}
	return []byte(out)
}

// ─── reading the file's layout ──────────────────────────────────────────────

// span is a range of lines, 1-based and inclusive.
type span struct{ start, end int }

// pairSpan is the lines one block-mapping pair occupies: the comment lines
// directly above its key at the key's own indentation, the key's line, and
// every line after it that is indented deeper, or is an item of a block
// sequence written at the key's indentation. Blank lines trailing the value
// are not part of it; they separate it from what follows.
func (mg *migrator) pairSpan(k, v *yaml.Node) span {
	indent := k.Column - 1
	start := k.Line
	for start > 1 {
		prev := mg.orig[start-2]
		if !isComment(prev) || leadingSpaces(prev) != indent {
			break
		}
		start--
	}

	resolved, _ := resolveAlias(v)
	blockSeq := resolved != nil && resolved.Kind == yaml.SequenceNode && resolved.Style&yaml.FlowStyle == 0 && v.Kind != yaml.AliasNode
	end := k.Line
	for i := k.Line + 1; i <= len(mg.orig); i++ {
		text := mg.orig[i-1]
		if isBlank(text) {
			continue
		}
		in := leadingSpaces(text)
		if in > indent || (in == indent && blockSeq && isDashLine(text)) {
			end = i
			continue
		}
		break
	}
	return span{start, end}
}

// reindent shifts lines by delta columns, leaving blank lines as they are.
func reindent(lines []string, delta int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		switch {
		case isBlank(l):
			out[i] = l
		case delta >= 0:
			out[i] = strings.Repeat(" ", delta) + l
		default:
			out[i] = l[min(-delta, leadingSpaces(l)):]
		}
	}
	return out
}

// indentStep is the file's indentation unit: how much deeper the first nested
// block mapping sits than its key. Two when the file has none.
func indentStep(n *yaml.Node) int {
	if n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0 {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 && v.Content[0].Line > k.Line {
				if d := v.Content[0].Column - k.Column; d > 0 {
					return d
				}
			}
		}
	}
	for _, c := range n.Content {
		if d := indentStep(c); d != 2 {
			return d
		}
	}
	return 2
}

func trimEOL(s string) string { return strings.TrimSuffix(s, "\r") }

func isBlank(s string) bool { return strings.TrimSpace(trimEOL(s)) == "" }

func isComment(s string) bool { return strings.HasPrefix(strings.TrimSpace(trimEOL(s)), "#") }

func isDashLine(s string) bool {
	t := strings.TrimSpace(trimEOL(s))
	return t == "-" || strings.HasPrefix(t, "- ")
}

func leadingSpaces(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// onlySpacesBefore reports whether a node at column (1-based) starts its line.
func onlySpacesBefore(text string, column int) bool {
	at := byteOffset(text, column)
	return at >= 0 && strings.TrimLeft(text[:at], " ") == ""
}

// byteOffset converts the parser's 1-based column, which counts characters,
// into a byte offset in the line, or -1 past its end.
func byteOffset(text string, column int) int {
	n := 0
	for i := range text {
		if n == column-1 {
			return i
		}
		n++
	}
	if n == column-1 {
		return len(text)
	}
	return -1
}

// tokenEnd is the byte offset just past a scalar written on one line starting
// at start, or false for a form migrate does not rewrite in place: a tag, an
// anchor, a block scalar, or a value that runs onto the next line.
func tokenEnd(text string, start int, n *yaml.Node) (int, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || start < 0 || start > len(text) || n.Anchor != "" ||
		n.Style&(yaml.TaggedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return 0, false
	}
	rest := text[start:]
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		if !strings.HasPrefix(rest, `"`) {
			return 0, false
		}
		for i := 1; i < len(rest); i++ {
			switch rest[i] {
			case '\\':
				i++
			case '"':
				return start + i + 1, true
			}
		}
	case n.Style&yaml.SingleQuotedStyle != 0:
		if !strings.HasPrefix(rest, "'") {
			return 0, false
		}
		for i := 1; i < len(rest); i++ {
			if rest[i] != '\'' {
				continue
			}
			if i+1 < len(rest) && rest[i+1] == '\'' {
				i++
				continue
			}
			return start + i + 1, true
		}
	default:
		if strings.HasPrefix(rest, n.Value) {
			return start + len(n.Value), true
		}
	}
	return 0, false
}

// requote writes a value in the quoting the value it replaces used, or plain
// when that reads back as the same string.
func requote(style yaml.Style, s string) string {
	switch {
	case style&yaml.DoubleQuotedStyle != 0:
		return strconv.Quote(s)
	case style&yaml.SingleQuotedStyle != 0:
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	var back any
	if err := yaml.Unmarshal([]byte(s), &back); err == nil && back == s {
		return s
	}
	return strconv.Quote(s)
}

// ─── the node tree ──────────────────────────────────────────────────────────

// pairOf returns a key and its value in a mapping, or nils.
func pairOf(m *yaml.Node, key string) (k, v *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// resolveAlias follows an alias to the node it names.
func resolveAlias(n *yaml.Node) (*yaml.Node, bool) {
	if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias, true
	}
	return n, false
}

// markAliased records every node an alias refers to.
func markAliased(n *yaml.Node, into map[*yaml.Node]bool) {
	if n == nil {
		return
	}
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		into[n.Alias] = true
	}
	for _, c := range n.Content {
		markAliased(c, into)
	}
}

// holdersIn resolves a dotted parent path to every mapping that could carry
// the leaf, expanding `[]` into each element of a list, as holdersAt does for
// the decoded document.
func holdersIn(root *yaml.Node, parent string) []holder {
	hs := []holder{{node: root, chain: []*yaml.Node{root}}}
	if parent == "" {
		return hs
	}
	for _, segment := range strings.Split(parent, ".") {
		name, isList := strings.CutSuffix(segment, "[]")
		var next []holder
		for _, h := range hs {
			k, v := pairOf(h.node, name)
			if k == nil {
				continue
			}
			value, viaAlias := resolveAlias(v)
			if !isList {
				if value.Kind == yaml.MappingNode {
					next = append(next, holder{node: value, key: k, chain: extend(h.chain, value), viaAlias: h.viaAlias || viaAlias})
				}
				continue
			}
			if value.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range value.Content {
				el, itemAlias := resolveAlias(item)
				if el.Kind == yaml.MappingNode {
					next = append(next, holder{node: el, chain: extend(h.chain, el), viaAlias: h.viaAlias || viaAlias || itemAlias})
				}
			}
		}
		hs = next
		if len(hs) == 0 {
			return nil
		}
	}
	return hs
}

func extend(chain []*yaml.Node, n *yaml.Node) []*yaml.Node {
	return append(append([]*yaml.Node(nil), chain...), n)
}

// rootPairHolding returns the root's key and value whose value is the first
// mapping below the root on a holder's chain.
func rootPairHolding(root *yaml.Node, chain []*yaml.Node) (k, v *yaml.Node) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		value, _ := resolveAlias(root.Content[i+1])
		if len(chain) > 1 && value == chain[1] {
			return root.Content[i], root.Content[i+1]
		}
	}
	return nil, nil
}

// ─── the proof ──────────────────────────────────────────────────────────────

// checkMigration reads the original and the rewritten file the way apply
// reads them, and refuses the rewrite unless both readings are the same
// document and the rewritten one leaves nothing to rewrite.
func checkMigration(src, out []byte) error {
	var after Node
	if err := yaml.Unmarshal(out, &after); err != nil {
		if strings.Contains(err.Error(), "unknown anchor") {
			return &MigrationRefusal{Reasons: []string{fmt.Sprintf(
				"moving a key would put a YAML alias before the anchor it refers to (%v); "+
					"move the anchored value above its first use, then run drift file migrate again.", err)}}
		}
		return fmt.Errorf("drift file migrate produced YAML that does not parse (%v), so nothing was written; "+
			"this is a bug in migrate", err)
	}
	var before Node
	if err := yaml.Unmarshal(src, &before); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	want, _ := readAsApply(before)
	got, left := readAsApply(after)
	if len(left) > 0 {
		return fmt.Errorf("drift file migrate left %s in the old spelling, so nothing was written; "+
			"this is a bug in migrate", strings.Join(left, ", "))
	}
	if !reflect.DeepEqual(plainValue(want), plainValue(got)) {
		return errors.New("the rewritten file would not declare what the original declares, so nothing was " +
			"written. This happens with YAML merge keys (<<) or aliases around a moved key; move it by hand")
	}
	return nil
}

// readAsApply runs the walker and the identity split over a decoded document,
// silently, as apply would, and returns what it would have announced.
func readAsApply(doc Node) (Node, []string) {
	var said []string
	tell := keyNotices{
		retired: func(k common.KeyDeprecation) {
			if k.Kind == common.KeyAlias && k.New != "" {
				said = append(said, k.Old)
			}
		},
		bothSpellings: func(k common.KeyDeprecation) { said = append(said, k.Old+" beside "+k.New) },
	}
	walkKeyDeprecations(doc, driftfileKeyDeprecations, tell)
	for _, root := range documentRoots(doc) {
		normaliseIdentities(root, func() { said = append(said, functionNamePath) })
	}
	return doc, said
}

// plainValue rewrites a decoded document into one set of Go types, so two
// documents compare by content rather than by which code built each map.
func plainValue(v any) any {
	switch t := v.(type) {
	case Node:
		return plainValue(map[string]any(t))
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = plainValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = plainValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = plainValue(val)
		}
		return out
	}
	return v
}
