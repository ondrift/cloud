package project

// migrate_elements.go: moving each top-level Pulse and Field tagged with an
// `element:` into that element's block under `atomic.elements`, for `drift file
// migrate`.
//
// The rest of migrate renames and moves KEYS; this moves whole list ENTRIES,
// with the comment lines directly above each one, re-indented under the
// element, and without its `element:` line. It is planned on the file as
// written and emitted after the edits within lines are made, so an entry
// carries its renamed keys and the `method:` line a retired `name` split into.
//
//   - An entry tagged with an element `atomic.elements` already declares is
//     appended to that element's list, which is created when the element has
//     none.
//   - Any other element is created, in the order its first entry appears in the
//     file: appended to `atomic.elements` when the file has one, and otherwise
//     in a new `elements:` block placed where the first top-level list began.
//   - `element: default` names the top level itself, so that entry stays where
//     it is and only its tag goes.
//   - A top-level list every entry of which moved is removed, key and all.
//
// A comment written after the tag on its own line is kept, as a comment line
// where the tag was.

import (
	"fmt"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"gopkg.in/yaml.v3"
)

// taggedEntry is one top-level entry carrying an `element:` tag.
type taggedEntry struct {
	list    string     // pulsesKey or fieldsKey
	item    *yaml.Node // the entry's mapping
	k, v    *yaml.Node // the tag
	element string
	oldPath string
}

// topList is one top-level list as the file writes it.
type topList struct {
	list   string // pulsesKey or fieldsKey, whichever spelling the file uses
	k, v   *yaml.Node
	moving int // how many of its entries move into an element
}

// elementPlan is every entry of one root that moves, or loses its tag.
type elementPlan struct {
	root     migrationRoot
	atomic   *yaml.Node
	lists    []topList
	moves    []taggedEntry // file order
	defaults []taggedEntry
}

// listSpellings is each top-level list under every name a file may write it
// with: `functions` is the retired spelling of `pulses`.
var listSpellings = []struct {
	list    string
	written []string
}{
	{pulsesKey, []string{pulsesKey, "functions"}},
	{fieldsKey, []string{fieldsKey}},
}

// planElements finds every tagged top-level entry of one root, refuses a form
// it cannot move line by line, and records a step for each.
func (mg *migrator) planElements(r migrationRoot) {
	_, av := pairOf(r.node, "atomic")
	atomic, atomicAlias := resolveAlias(av)
	if atomic == nil || atomic.Kind != yaml.MappingNode {
		return
	}

	p := elementPlan{root: r, atomic: atomic}
	for _, spelling := range listSpellings {
		for _, written := range spelling.written {
			lk, lv := pairOf(atomic, written)
			seq, seqAlias := resolveAlias(lv)
			if lk == nil || seq == nil || seq.Kind != yaml.SequenceNode {
				continue
			}
			li := len(p.lists)
			p.lists = append(p.lists, topList{list: spelling.list, k: lk, v: lv})
			for _, raw := range seq.Content {
				item, itemAlias := resolveAlias(raw)
				if item == nil || item.Kind != yaml.MappingNode {
					continue
				}
				ek, ev := pairOf(item, "element")
				if ek == nil {
					continue
				}
				t := taggedEntry{
					list: spelling.list, item: item, k: ek, v: ev, element: ev.Value,
					oldPath: r.prefix + "atomic." + spelling.list + "[].element",
				}
				switch {
				case atomicAlias || seqAlias || itemAlias || mg.aliased[atomic] || mg.aliased[seq] || mg.aliased[item]:
					mg.refuse(fmt.Sprintf("%s (line %d) sits inside a YAML anchor that is used in more than one place, "+
						"so moving its entry would change every place the anchor is used; move it into its element by hand.",
						t.oldPath, ek.Line))
					continue
				case seq.Style&yaml.FlowStyle != 0 || item.Style&yaml.FlowStyle != 0:
					mg.refuse(fmt.Sprintf("%s (line %d) is written in flow style ({ ... }); write the entry as an "+
						"indented block, then run drift file migrate again.", t.oldPath, ek.Line))
					continue
				case ev.Kind != yaml.ScalarNode || ev.Line != ek.Line:
					mg.refuse(fmt.Sprintf("%s (line %d) is not a single value on its own line; move the entry into "+
						"its element by hand.", t.oldPath, ek.Line))
					continue
				}
				if t.element == atomic_cmd.DefaultElementName {
					p.defaults = append(p.defaults, t)
					mg.steps = append(mg.steps, MigrationStep{Line: ek.Line, Old: t.oldPath, New: r.prefix + "atomic." + spelling.list + "[]"})
					continue
				}
				p.lists[li].moving++
				p.moves = append(p.moves, t)
				mg.steps = append(mg.steps, MigrationStep{Line: ek.Line, Old: t.oldPath, New: r.prefix + "atomic.elements[]." + spelling.list})
			}
		}
	}
	if len(p.moves) == 0 && len(p.defaults) == 0 {
		return
	}
	sort.SliceStable(p.moves, func(i, j int) bool { return p.moves[i].item.Line < p.moves[j].item.Line })

	if len(p.moves) > 0 {
		mg.checkElementsTarget(p)
	}
	mg.elements = append(mg.elements, p)
}

// checkElementsTarget refuses an existing `atomic.elements` that entries cannot
// be appended to line by line: a flow-style list, or a declared element whose
// list is one.
func (mg *migrator) checkElementsTarget(p elementPlan) {
	ek, ev := pairOf(p.atomic, elementsKey)
	if ek == nil {
		return
	}
	where := p.root.prefix + "atomic." + elementsKey
	seq, alias := resolveAlias(ev)
	switch {
	case alias || mg.aliased[seq]:
		mg.refuse(fmt.Sprintf("%s (line %d) is a YAML anchor or alias, so adding to it would change every place "+
			"it is used; move the tagged entries into it by hand.", where, ek.Line))
		return
	case seq.Kind != yaml.SequenceNode || seq.Style&yaml.FlowStyle != 0:
		mg.refuse(fmt.Sprintf("%s (line %d) is written in flow style ({ ... }); write it as an indented block, "+
			"then run drift file migrate again.", where, ek.Line))
		return
	}
	wanted := map[string]map[string]bool{}
	for _, t := range p.moves {
		if wanted[t.element] == nil {
			wanted[t.element] = map[string]bool{}
		}
		wanted[t.element][t.list] = true
	}
	for _, raw := range seq.Content {
		el, _ := resolveAlias(raw)
		if el == nil || el.Kind != yaml.MappingNode {
			continue
		}
		_, nv := pairOf(el, "name")
		if nv == nil || wanted[nv.Value] == nil {
			continue
		}
		if el.Style&yaml.FlowStyle != 0 {
			mg.refuse(fmt.Sprintf("%s (line %d) declares the element %q in flow style ({ ... }); write it as an "+
				"indented block, then run drift file migrate again.", where, el.Line, nv.Value))
			continue
		}
		for list := range wanted[nv.Value] {
			lk, lv := pairOf(el, list)
			if lk == nil {
				continue
			}
			if ls, _ := resolveAlias(lv); ls.Kind != yaml.SequenceNode || ls.Style&yaml.FlowStyle != 0 {
				mg.refuse(fmt.Sprintf("%s[].%s (line %d) of the element %q is written in flow style ({ ... }); "+
					"write it as an indented block, then run drift file migrate again.", where, list, lk.Line, nv.Value))
			}
		}
	}
}

// emitElements makes one root's element moves, after the edits within lines.
func (mg *migrator) emitElements(p elementPlan) {
	for _, t := range p.defaults {
		mg.dropTag(t)
	}
	if len(p.moves) == 0 {
		return
	}

	// Each moving entry's lines, without its tag, before anything is deleted.
	type moved struct {
		t           taggedEntry
		sp          span
		dash        int
		blankBefore bool
		lines       []string
	}
	var all []moved
	for _, t := range p.moves {
		sp, dash, ok := mg.entrySpan(t)
		if !ok || !mg.dropTag(t) {
			return
		}
		var lines []string
		for i := sp.start; i <= sp.end; i++ {
			if mg.deleted[i] {
				continue
			}
			lines = append(lines, mg.lines[i-1])
			lines = append(lines, mg.attached[i]...)
		}
		all = append(all, moved{t: t, sp: sp, dash: dash, blankBefore: sp.start > 1 && isBlank(mg.orig[sp.start-2]), lines: lines})
	}

	// The file's own sequence indentation: how far the first top-level list's
	// dashes sit inside its key.
	first := p.lists[0]
	for _, l := range p.lists[1:] {
		if l.k.Line < first.k.Line {
			first = l
		}
	}
	keyIndent := first.k.Column - 1
	seqIndent := 2
	if seq, _ := resolveAlias(first.v); len(seq.Content) > 0 {
		if _, dash, ok := mg.dashOf(seq.Content[0]); ok && dash >= keyIndent {
			seqIndent = dash - keyIndent
		}
	}

	// entriesUnder renders the moved entries of one element and list, their
	// dashes at dashIndent; a blank line that set an entry off stays, except
	// above the first of a list migrate creates.
	entriesUnder := func(element, list string, dashIndent int, appending bool) []string {
		var out []string
		n := 0
		for _, m := range all {
			if m.t.element != element || m.t.list != list {
				continue
			}
			if m.blankBefore && (appending || n > 0) {
				out = append(out, mg.newLine(""))
			}
			out = append(out, reindent(m.lines, dashIndent-m.dash)...)
			n++
		}
		return out
	}
	lists := func(element string) []string {
		seen := map[string]bool{}
		for _, m := range all {
			if m.t.element == element {
				seen[m.t.list] = true
			}
		}
		var out []string
		for _, l := range []string{pulsesKey, fieldsKey} {
			if seen[l] {
				out = append(out, l)
			}
		}
		return out
	}
	var order []string
	seenEl := map[string]bool{}
	for _, m := range all {
		if !seenEl[m.t.element] {
			seenEl[m.t.element] = true
			order = append(order, m.t.element)
		}
	}
	newElement := func(name string, dashIndent int) []string {
		keyAt := dashIndent + 2
		out := []string{mg.newLine(strings.Repeat(" ", dashIndent) + "- name: " + name)}
		for _, l := range lists(name) {
			out = append(out, mg.newLine(strings.Repeat(" ", keyAt)+l+":"))
			out = append(out, entriesUnder(name, l, keyAt+seqIndent, false)...)
		}
		return out
	}

	ek, ev := pairOf(p.atomic, elementsKey)
	if ek == nil {
		block := []string{mg.newLine(strings.Repeat(" ", keyIndent) + elementsKey + ":")}
		for _, name := range order {
			block = append(block, newElement(name, keyIndent+seqIndent)...)
		}
		anchor := mg.pairSpan(first.k, first.v).start - 1
		mg.inserts[anchor] = append(mg.inserts[anchor], block...)
	} else {
		seq, _ := resolveAlias(ev)
		declared := map[string]*yaml.Node{}
		for _, raw := range seq.Content {
			if el, _ := resolveAlias(raw); el != nil && el.Kind == yaml.MappingNode {
				if _, nv := pairOf(el, "name"); nv != nil && declared[nv.Value] == nil {
					declared[nv.Value] = el
				}
			}
		}
		elDash, elKeys := keyIndent+seqIndent, keyIndent+seqIndent+2
		if len(seq.Content) > 0 {
			if _, dash, ok := mg.dashOf(seq.Content[0]); ok {
				elDash = dash
			}
			if el, _ := resolveAlias(seq.Content[0]); el.Kind == yaml.MappingNode && len(el.Content) > 0 {
				elKeys = el.Content[0].Column - 1
			}
		}
		// The declared elements first, so their additions stay inside them
		// when an element created after them shares the same last line.
		for _, name := range order {
			el := declared[name]
			if el == nil {
				continue
			}
			for _, l := range lists(name) {
				if lk, lv := pairOf(el, l); lk != nil {
					ls, _ := resolveAlias(lv)
					dash := lk.Column - 1 + seqIndent
					if len(ls.Content) > 0 {
						if _, d, ok := mg.dashOf(ls.Content[0]); ok {
							dash = d
						}
					}
					end := mg.pairSpan(lk, lv).end
					mg.inserts[end] = append(mg.inserts[end], entriesUnder(name, l, dash, true)...)
					continue
				}
				sp, _, ok := mg.itemSpan(el)
				if !ok {
					return
				}
				add := []string{mg.newLine(strings.Repeat(" ", elKeys) + l + ":")}
				add = append(add, entriesUnder(name, l, elKeys+seqIndent, false)...)
				mg.inserts[sp.end] = append(mg.inserts[sp.end], add...)
			}
		}
		end := mg.pairSpan(ek, ev).end
		for _, name := range order {
			if declared[name] == nil {
				mg.inserts[end] = append(mg.inserts[end], newElement(name, elDash)...)
			}
		}
	}

	for _, m := range all {
		mg.deleteSpan(m.sp, true)
	}

	// A list every entry of which moved goes too, keeping the comments above
	// its key, and the blank line that set it off from a sibling below.
	for _, l := range p.lists {
		seq, _ := resolveAlias(l.v)
		if l.moving == 0 || l.moving != len(seq.Content) {
			continue
		}
		mg.dropLine(l.k, l.v)
		end := mg.pairSpan(l.k, l.v).end
		next := end + 1
		for next <= len(mg.orig) && isBlank(mg.orig[next-1]) {
			next++
		}
		if next <= len(mg.orig) && leadingSpaces(mg.orig[next-1]) >= l.k.Column-1 {
			for i := end + 1; i < next; i++ {
				mg.deleted[i] = true
			}
		}
	}
}

// dropTag removes an entry's `element:` line, keeping a comment written after
// it as a comment line in its place. A tag written as the entry's first key,
// on its dash line, gives that line to the entry's next key.
func (mg *migrator) dropTag(t taggedEntry) bool {
	orig := mg.orig[t.k.Line-1]
	vs := byteOffset(orig, t.v.Column)
	ve, ok := tokenEnd(orig, vs, t.v)
	if !ok {
		mg.refuse(fmt.Sprintf("%s (line %d) is written in a form drift file migrate does not rewrite; "+
			"move the entry into its element by hand.", t.oldPath, t.k.Line))
		return false
	}
	comment := strings.TrimSpace(trimEOL(orig[ve:]))

	if onlySpacesBefore(orig, t.k.Column) {
		mg.dropLine(t.k, t.v)
		return true
	}

	// On the dash line: the next key, written on the line below, moves up.
	if len(t.item.Content) < 4 || t.item.Content[0] != t.k || t.item.Content[2].Line != t.k.Line+1 {
		mg.refuse(fmt.Sprintf("%s (line %d) is the entry's first key, and the key after it does not follow "+
			"on the next line; move `element:` below the entry's first key, then run drift file migrate again.",
			t.oldPath, t.k.Line))
		return false
	}
	next := t.item.Content[2]
	text := mg.lines[next.Line-1]
	at := byteOffset(text, next.Column)
	if at < 0 {
		mg.refuse(fmt.Sprintf("%s (line %d) could not be rewritten; move the entry into its element by hand.",
			t.oldPath, t.k.Line))
		return false
	}
	mg.lines[t.k.Line-1] = orig[:byteOffset(orig, t.k.Column)] + text[at:]
	mg.deleted[next.Line] = true
	mg.attached[t.k.Line] = append(mg.attached[t.k.Line], mg.attached[next.Line]...)
	delete(mg.attached, next.Line)
	if comment != "" {
		mg.attached[t.k.Line] = append(mg.attached[t.k.Line], mg.newLine(strings.Repeat(" ", next.Column-1)+comment))
	}
	return true
}

// dropLine removes the line of a key whose value sits on the same line,
// keeping a comment written after it as a comment line in its place.
func (mg *migrator) dropLine(k, v *yaml.Node) {
	orig := mg.orig[k.Line-1]
	after := k
	if v.Kind == yaml.ScalarNode && v.Line == k.Line {
		after = v
	}
	comment := ""
	if e, ok := tokenEnd(orig, byteOffset(orig, after.Column), after); ok {
		rest := strings.TrimSpace(trimEOL(orig[e:]))
		if after == k {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		}
		if strings.HasPrefix(rest, "#") {
			comment = rest
		}
	}
	if comment == "" {
		mg.deleted[k.Line] = true
		return
	}
	mg.lines[k.Line-1] = mg.newLine(strings.Repeat(" ", k.Column-1) + comment)
}

// entrySpan is a moving entry's lines and its dash's indentation, or a
// refusal when the entry does not start on its dash line.
func (mg *migrator) entrySpan(t taggedEntry) (span, int, bool) {
	sp, dash, ok := mg.itemSpan(t.item)
	if !ok {
		mg.refuse(fmt.Sprintf("%s (line %d) belongs to an entry whose first key does not follow its `-`; "+
			"move the entry into its element by hand.", t.oldPath, t.k.Line))
	}
	return sp, dash, ok
}

// itemSpan is the lines one block-sequence entry occupies: the comment lines
// directly above its dash at the dash's own indentation, the dash line, and
// every line after it indented deeper. Blank lines trailing it are not part of
// it.
func (mg *migrator) itemSpan(item *yaml.Node) (span, int, bool) {
	line, dash, ok := mg.dashOf(item)
	if !ok {
		return span{}, 0, false
	}
	start := line
	for start > 1 {
		prev := mg.orig[start-2]
		if !isComment(prev) || leadingSpaces(prev) != dash {
			break
		}
		start--
	}
	end := line
	for i := line + 1; i <= len(mg.orig); i++ {
		text := mg.orig[i-1]
		if isBlank(text) {
			continue
		}
		if leadingSpaces(text) > dash {
			end = i
			continue
		}
		break
	}
	return span{start, end}, dash, true
}

// dashOf finds the `-` an entry of a block sequence starts with, on the line
// of its first key: that line and the dash's indentation.
func (mg *migrator) dashOf(raw *yaml.Node) (int, int, bool) {
	item, _ := resolveAlias(raw)
	if item == nil || item.Line < 1 || item.Line > len(mg.orig) {
		return 0, 0, false
	}
	text := mg.orig[item.Line-1]
	at := byteOffset(text, item.Column)
	if at < 0 {
		return 0, 0, false
	}
	before := strings.TrimRight(text[:at], " ")
	if !strings.HasSuffix(before, "-") || strings.TrimLeft(before[:len(before)-1], " ") != "" {
		return 0, 0, false
	}
	return item.Line, len(before) - 1, true
}

// canonicalSlots orders a flattened root's Pulses and Fields by element, and
// within an element by content, so two readings of one file compare equal
// whichever list or position each entry was written in. The order of entries
// is not part of what a Driftfile declares: elements are built and deployed
// grouped by element, and sorted within it.
func canonicalSlots(root Node) {
	atomic, ok := asNode(root.Get("atomic"))
	if !ok {
		return
	}
	for _, list := range []string{pulsesKey, fieldsKey} {
		items := atomic.List(list)
		key := func(i int) string {
			n, _ := asNode(items[i])
			return n.Str("element") + "\x00" + fmt.Sprint(plainValue(items[i]))
		}
		sort.SliceStable(items, func(i, j int) bool { return key(i) < key(j) })
	}
}
