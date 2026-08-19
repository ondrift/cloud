package slice

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// form.go — the slice's shape as one screen you move around in.
//
// A sequence of prompts can only be answered forwards. You cannot see what you
// already said, cannot go back to change it, and cannot tell how much is left —
// and a slice's shape is a dozen numbers in four groups, most of which most
// people leave alone. So this is a tree instead: everything collapsed, expand
// what you care about, and the parts you never open stay at the platform's own
// defaults.
//
// It is hand-rolled on x/term and ANSI, which is what the dashboard already is.
// The alternative was a TUI framework, and adding one to a binary whose whole
// dependency list is four lines would cost more than this screen is worth.
//
// The tree is REBUILT rather than mutated when a count changes: typing 3 into
// Functions grows three children, typing 2 shrinks it to two, and the values
// already typed into the first two survive because they are copied across by
// position. That is why nodes carry their values as strings — the screen and the
// answer are the same thing, and there is no second model to keep in step.

type nodeKind int

const (
	kindSection nodeKind = iota // Atomic, Backbone… — expandable, holds no value
	kindGroup                   // Function 1, Collection 2… — expandable, spawned
	kindInt                     // a number
	kindText                    // a string
	kindChoice                  // one of a fixed set
	kindAction                  // Create — the one row that does something
)

type node struct {
	label    string
	kind     nodeKind
	value    string
	choices  []string
	unit     string
	hint     string
	children []*node
	expanded bool

	// spawn turns this node's number into its PARENT's group children. It is
	// what makes "Functions: 3" grow three subtrees, and it is held here rather
	// than on the parent because the count that drives it lives here.
	spawn func(n int) []*node
	// The range ← and → step this number through. maxV of 0 means no ceiling
	// this form knows of — the platform still has one, and still enforces it.
	minV, maxV int
}

// shapeForm is the whole screen.
type shapeForm struct {
	root []*node
	flat []*row // what is currently visible, rebuilt on every render
	// createNode is the green row at the bottom. Held by pointer so the renderer
	// can find it without matching on its label.
	createNode *node
	cursor     int
	edit       bool // the focused row is being typed into
	// fresh means nothing has been typed since editing began, so the first
	// character REPLACES what is there rather than appending to it. Without it,
	// a field showing the default 0 answers a typed 2 with "02" — which parses,
	// which is why it would survive review, and which is not what anybody meant.
	fresh  bool
	status string
	width  int

	// The live price, and what is known about it.
	//
	// priceGen rises on every edit. A reply carrying a stale generation is
	// dropped, so a slow request that started three keystrokes ago cannot
	// overwrite the answer to the current shape — the figure on screen is always
	// the figure for what is on screen, or nothing.
	priceCents int
	priceKnown bool
	priceBusy  bool
	priceErr   string
	priceGen   int
}

type row struct {
	n     *node
	depth int
}

// ─── the tree ───────────────────────────────────────────────────────────────

func newShapeForm(name string) *shapeForm {
	f := &shapeForm{width: 80}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
		f.width = w
	}

	// Every field carries a hint, and every hint says the same three things in
	// the same order: what it is, what it is for, and what it will take. A form
	// that explains only its surprising fields teaches that a silent field is
	// obvious, and the field somebody is stuck on is never the one predicted.
	f.createNode = &node{label: "Create", kind: kindAction,
		hint: "price this shape and create the slice — you will be shown the cost first"}

	f.root = []*node{
		{label: "Slice name", kind: kindText, value: name,
			hint: "The slice's name, and the hostname it answers on. " +
				"Lowercase letters, numbers and dashes, up to 32."},

		{label: "Atomic", kind: kindSection,
			hint: "Functions, and the volume their code lives on.",
			children: []*node{
				{label: "Code & dependencies", kind: kindInt, unit: "MB", value: "0", minV: 0,
					hint: "The runner volume: your deployed code plus everything it " +
						"vendors. Billed per GiB. Whole MB, 0 if you deploy no code."},
				{label: "Functions", kind: kindInt, value: "0", spawn: functionGroups, minV: 0, maxV: maxFunctionSlots,
					hint: "How many function slots to buy. Each books its own memory and " +
						"is billed from the moment it exists, deployed or not. 0-" +
						strconv.Itoa(maxFunctionSlots) + "."},
			}},

		{label: "Backbone", kind: kindSection,
			hint: "Storage. Each item is declared BY NAME — a write to a name the " +
				"slice does not carry is refused, not created.",
			children: []*node{
				{label: "NoSQL collections", kind: kindInt, value: "0", minV: 0, maxV: 64,
					spawn: namedGroups("Collection", "size", "MB"),
					hint: "Document collections, the store most apps reach for first. " +
						"Each is named and sized separately. Whole number."},
				{label: "SQL databases", kind: kindInt, value: "0", minV: 0, maxV: 64,
					spawn: namedGroups("Database", "size", "MB"),
					hint: "Per-slice SQLite files, encrypted at rest, one file each. " +
						"Whole number."},
				{label: "Blob buckets", kind: kindInt, value: "0", minV: 0, maxV: 64,
					spawn: namedGroups("Bucket", "size", "MB"),
					hint: "Object storage for files. Each bucket is named and sized " +
						"separately. Whole number."},
				{label: "Queues", kind: kindInt, value: "0", minV: 0, maxV: 64,
					spawn: namedGroups("Queue", "depth", "messages"),
					hint: "Message queues a QUEUE-method function drains. Sized in " +
						"messages held, not bytes. Whole number."},
			}},

		{label: "Canvas", kind: kindSection,
			hint: "Static sites served at the slice's own root.",
			children: []*node{
				{label: "Site storage", kind: kindInt, unit: "MB", value: "0",
					hint: "Total across every site on the slice, not per site. Billed " +
						"per GiB. Whole MB, 0 if you publish no site."},
			}},

		{label: "Deed", kind: kindSection,
			hint: "Identity: key material, and per-person app data.",
			children: []*node{
				{label: "Vault entry size", kind: kindInt, unit: "KB", value: "0",
					hint: "Ceiling on one wrapped keyring entry. Key material, not " +
						"documents. Whole KB."},
				{label: "Vault entries per identity", kind: kindInt, value: "0",
					hint: "How much key history one identity accumulates. Vault is " +
						"append-only, so past this the oldest go. Whole number."},
				{label: "Pocket record size", kind: kindInt, unit: "KB", value: "0",
					hint: "Ceiling on one per-identity record — an app's own data for " +
						"one person. Whole KB."},
			}},

		f.createNode,
	}
	return f
}

func functionGroups(n int) []*node {
	out := make([]*node, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, &node{
			label: fmt.Sprintf("Function %d", i), kind: kindGroup,
			hint: "One function slot: how it is addressed, and what it books.",
			children: []*node{
				{label: "Method", kind: kindChoice, value: "post", choices: httpMethods,
					hint: "← → to choose. Part of the function's identity, not a detail — " +
						"get:items and post:items are two different functions. QUEUE names " +
						"a queue to drain instead of a path."},
				{label: "Route", kind: kindText,
					hint: "The path under /api/, e.g. auth/challenge. No leading slash. " +
						"A QUEUE function names the queue it drains instead."},
				{label: "Memory", kind: kindInt, unit: "MB", minV: minMemoryMiB, maxV: maxMemoryMiB,
					hint: fmt.Sprintf("The pool this function's SIMULTANEOUS calls share — it "+
						"buys concurrency, not headroom for one call. %d-%d MB; Go and "+
						"Rust need %d or more.", minMemoryMiB, maxMemoryMiB, compiledFloorMiB)},
			},
		})
	}
	return out
}

// namedGroups builds "Collection 1 { Name, size }" and its siblings. The second
// field is a size in MB for storage and a depth in messages for a queue, which
// is why the label and unit are parameters rather than assumed.
func namedGroups(noun, sizeLabel, unit string) func(int) []*node {
	return func(n int) []*node {
		out := make([]*node, 0, n)
		for i := 1; i <= n; i++ {
			sizeHint := fmt.Sprintf("How large this %s may grow, in whole %s. Must be "+
				"above zero: the slice reads a zero cap as UNLIMITED and the bill "+
				"clamps it to nothing.", strings.ToLower(noun), unit)
			if unit == "messages" {
				sizeHint = "How many messages this queue may hold at once. Whole number, " +
					"above zero. This is a depth, not a size in bytes."
			}
			out = append(out, &node{
				label: fmt.Sprintf("%s %d", noun, i), kind: kindGroup,
				hint: fmt.Sprintf("One %s: what your code calls it, and how big it may get.",
					strings.ToLower(noun)),
				children: []*node{
					{label: "Name", kind: kindText,
						hint: fmt.Sprintf("The exact string your code addresses this %s by. "+
							"A write to any other name is refused with 400, not created — "+
							"so this is the resource, not a label for it.",
							strings.ToLower(noun))},
					{label: sizeLabel, kind: kindInt, unit: unit, minV: 1, hint: sizeHint},
				},
			})
		}
		return out
	}
}

// respawn regrows a count node's siblings, keeping what was already typed.
//
// Values are carried across BY POSITION, so shrinking from three to two keeps
// the first two exactly as they were and growing back to three restores them
// alongside a fresh third. Rebuilding without this would silently empty a
// function somebody had already filled in, on a keystroke they made to add
// another one.
func (f *shapeForm) respawn(parent, counter *node) {
	n, err := strconv.Atoi(strings.TrimSpace(counter.value))
	if err != nil || n < 0 {
		return
	}
	if n > maxFunctionSlots {
		n = maxFunctionSlots
		counter.value = strconv.Itoa(n)
		f.status = fmt.Sprintf("a slice may hold %d at most", maxFunctionSlots)
	}

	old := map[string]*node{}
	for _, c := range parent.children {
		if c.kind == kindGroup && strings.HasPrefix(c.label, groupPrefix(counter)) {
			old[c.label] = c
		}
	}

	fresh := counter.spawn(n)
	for _, g := range fresh {
		if prev, ok := old[g.label]; ok {
			for i := range g.children {
				if i < len(prev.children) {
					g.children[i].value = prev.children[i].value
				}
			}
			g.expanded = prev.expanded
		}
	}

	kept := make([]*node, 0, len(parent.children))
	for _, c := range parent.children {
		if c.kind == kindGroup && strings.HasPrefix(c.label, groupPrefix(counter)) {
			continue
		}
		kept = append(kept, c)
	}
	// The groups sit directly under the counter that produced them, so a section
	// with two counted lists does not interleave.
	at := len(kept)
	for i, c := range kept {
		if c == counter {
			at = i + 1
			break
		}
	}
	parent.children = append(kept[:at:at], append(fresh, kept[at:]...)...)
}

// groupPrefix is the label stem the counter's groups carry, so a section holding
// several counted lists can tell its own children apart.
func groupPrefix(counter *node) string {
	switch counter.label {
	case "Functions":
		return "Function "
	case "NoSQL collections":
		return "Collection "
	case "SQL databases":
		return "Database "
	case "Blob buckets":
		return "Bucket "
	case "Queues":
		return "Queue "
	}
	return "\x00never"
}

// ─── rendering ──────────────────────────────────────────────────────────────

// Deliberately no background colours anywhere.
//
// The field being typed into used to be drawn in reverse video, which paints
// the terminal's foreground colour behind the text and its background in front
// — unreadable on any scheme the author did not happen to share. An underline
// and a caret mark the same thing while leaving both colours alone, so this
// reads on a light terminal, a dark one, and one with a palette nobody
// anticipated.
const (
	fReset = "\x1b[0m"
	fDim   = "\x1b[2m"
	fBold  = "\x1b[1m"
	fCyan  = "\x1b[36m"
	fGreen = "\x1b[32m"
	fUnder = "\x1b[4m"
)

func (f *shapeForm) visible() {
	f.flat = nil
	var walk func(ns []*node, depth int)
	walk = func(ns []*node, depth int) {
		for _, n := range ns {
			f.flat = append(f.flat, &row{n: n, depth: depth})
			if n.expanded {
				walk(n.children, depth+1)
			}
		}
	}
	walk(f.root, 0)
}

func (f *shapeForm) render() {
	f.visible()
	if f.cursor >= len(f.flat) {
		f.cursor = len(f.flat) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}

	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	fmt.Fprintf(&b, "  %sCreate a slice%s\r\n", fBold, fReset)
	fmt.Fprintf(&b, "  %s↑↓ move · ←→ open a section, or change a value · ⏎ type · ^S create · ^C cancel%s\r\n\r\n",
		fDim, fReset)

	for i, r := range f.flat {
		if r.n == f.createNode {
			// One blank line separates the action from the shape, so the eye does
			// not read "Create" as another field of Deed.
			b.WriteString("\r\n")
		}
		b.WriteString(f.line(i, r))
	}

	b.WriteString("\r\n")
	if f.status != "" {
		fmt.Fprintf(&b, "  %s%s%s\r\n", fCyan, f.status, fReset)
	} else if hint := f.flat[f.cursor].n.hint; hint != "" {
		// Wrapped rather than truncated: a hint cut off mid-sentence is a hint
		// that stops exactly where it was about to say the thing.
		for _, line := range wrap(hint, f.width-4) {
			fmt.Fprintf(&b, "  %s%s%s\r\n", fDim, line, fReset)
		}
	}
	fmt.Fprint(os.Stdout, b.String())
}

// wrap breaks text on word boundaries at width columns.
func wrap(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line == "" {
			line = word
			continue
		}
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// priceLabel is what sits beside Create.
//
// It never shows a stale figure as if it were current. While a request is in
// flight the last known price is dimmed rather than hidden — hiding it makes
// the row jump on every keystroke — and a price that could not be fetched says
// so instead of falling back to a number nobody computed.
func (f *shapeForm) priceLabel() string {
	switch {
	case f.priceErr != "":
		return fDim + "price unavailable" + fReset
	case f.priceBusy && f.priceKnown:
		return fDim + euros(f.priceCents) + "/mo" + fReset
	case f.priceBusy:
		return fDim + "pricing…" + fReset
	case !f.priceKnown:
		return ""
	case f.priceCents == 0:
		return fGreen + "free" + fReset
	}
	return fGreen + euros(f.priceCents) + "/mo" + fReset
}

// priceableConfig builds a config from whatever is on screen, without
// validating it.
//
// Pricing is not submission: a half-typed form still has a cost, and refusing
// to price it until every route is filled in would leave the figure blank for
// most of the time somebody is deciding. Names do not enter the price — only
// the counts and the numbers — so an unnamed collection with a size is still
// worth what its size is worth.
func (f *shapeForm) priceableConfig() map[string]any {
	shape := declaredShape{Backbone: backboneShape{}}
	var atomic, backbone, canvas *node
	for _, n := range f.root {
		switch n.label {
		case "Atomic":
			atomic = n
		case "Backbone":
			backbone = n
		case "Canvas":
			canvas = n
		}
	}

	shape.StorageMiB = intOf(childValue(atomic, "Code & dependencies"))
	if atomic != nil {
		for _, g := range atomic.children {
			if g.kind != kindGroup {
				continue
			}
			if mem := intOf(childValue(g, "Memory")); mem > 0 {
				shape.Slots = append(shape.Slots, functionSlot{
					Method: childValue(g, "Method"),
					Route:  strings.TrimSpace(childValue(g, "Route")),
					Memory: mem,
				})
			}
		}
	}

	gatherLoose := func(prefix, sizeLabel string) map[string]int {
		out := map[string]int{}
		if backbone == nil {
			return nil
		}
		for i, g := range backbone.children {
			if g.kind != kindGroup || !strings.HasPrefix(g.label, prefix) {
				continue
			}
			size := intOf(childValue(g, sizeLabel))
			if size <= 0 {
				continue
			}
			name := strings.TrimSpace(childValue(g, "Name"))
			if name == "" {
				// A placeholder key so two unnamed items of the same size both
				// count. The name never reaches the price; only the value does.
				name = fmt.Sprintf("%s%d", prefix, i)
			}
			out[name] = size
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	shape.Backbone.Collections = gatherLoose("Collection ", "size")
	shape.Backbone.Databases = gatherLoose("Database ", "size")
	shape.Backbone.Buckets = gatherLoose("Bucket ", "size")
	shape.Backbone.Queues = gatherLoose("Queue ", "depth")

	shape.CanvasMiB = intOf(childValue(canvas, "Site storage"))
	return buildConfig(shape.StorageMiB, shape.Slots, shape.Backbone, shape.CanvasMiB)
}

// choiceStrip renders every option, the chosen one bracketed.
//
// Brackets rather than a background colour, for the same reason the edit caret
// is an underline: a highlight painted with the terminal's own colours is
// unreadable on half of them, and [post] reads on every one.
//
// When the strip will not fit the terminal it shows a window around the
// selection with ‹ › to say there is more either side, rather than wrapping a
// single field across two lines and breaking the column the tree is drawn in.
func (f *shapeForm) choiceStrip(n *node, depth int) string {
	sel := 0
	for i, c := range n.choices {
		if c == n.value {
			sel = i
		}
	}

	room := f.width - (len(n.label) + 4*depth + 8)
	full := 0
	for _, c := range n.choices {
		full += len(c) + 1
	}
	full += 2 // the brackets on the chosen one

	lo, hi := 0, len(n.choices)
	truncated := full > room && room > 12
	if truncated {
		// Keep the selection in view with as many neighbours as fit.
		span := 0
		lo, hi = sel, sel+1
		span = len(n.choices[sel]) + 3
		for lo > 0 || hi < len(n.choices) {
			grew := false
			if hi < len(n.choices) && span+len(n.choices[hi])+1 <= room {
				span += len(n.choices[hi]) + 1
				hi++
				grew = true
			}
			if lo > 0 && span+len(n.choices[lo-1])+1 <= room {
				lo--
				span += len(n.choices[lo]) + 1
				grew = true
			}
			if !grew {
				break
			}
		}
	}

	var b strings.Builder
	if truncated && lo > 0 {
		b.WriteString(fDim + "‹ " + fReset)
	}
	for i := lo; i < hi; i++ {
		if i > lo {
			b.WriteString(" ")
		}
		if i == sel {
			b.WriteString(fCyan + fBold + "[" + n.choices[i] + "]" + fReset)
		} else {
			b.WriteString(fDim + n.choices[i] + fReset)
		}
	}
	if truncated && hi < len(n.choices) {
		b.WriteString(fDim + " ›" + fReset)
	}
	return b.String()
}

func (f *shapeForm) line(i int, r *row) string {
	indent := strings.Repeat("    ", r.depth)

	marker := "  "
	if len(r.n.children) > 0 || r.n.kind == kindSection || r.n.kind == kindGroup {
		if r.n.expanded {
			marker = "▾ "
		} else {
			marker = "▸ "
		}
	}

	label := r.n.label
	switch r.n.kind {
	case kindSection:
		label = fBold + label + fReset
	case kindAction:
		label = fGreen + fBold + label + fReset + "   " + f.priceLabel()
		marker = "  "
	}

	value := ""
	switch r.n.kind {
	case kindSection, kindGroup, kindAction:
	case kindChoice:
		// Every option on the row, with the chosen one marked.
		//
		// A closed set of eight is small enough to show whole, and showing it
		// whole answers the question the field actually raises — "what else is
		// there?" — which cycling one-at-a-time never does: you have to press
		// Enter eight times to learn there were eight, and you cannot see the
		// one you passed.
		value = ": " + f.choiceStrip(r.n, r.depth)
	default:
		shown := r.n.value
		if shown == "" {
			shown = fDim + "…" + fReset
		}
		if f.edit && i == f.cursor {
			shown = fUnder + r.n.value + fReset + fCyan + "▏" + fReset
		}
		value = ": " + shown
		if r.n.unit != "" && r.n.value != "" {
			value += " " + fDim + r.n.unit + fReset
		}
	}

	cursor := "  "
	if i == f.cursor {
		cursor = fCyan + "❯ " + fReset
	}
	return fmt.Sprintf("%s%s%s%s%s\r\n", cursor, indent, marker, label, value)
}

// ─── input ──────────────────────────────────────────────────────────────────

// formKey is the small set of keys this screen understands. It is decoded here
// rather than shared with the dashboard because the dashboard's package imports
// this one, and a decoder is cheaper to state twice than a cycle is to break.
type formKey int

const (
	fkNone formKey = iota
	fkUp
	fkDown
	fkLeft
	fkRight
	fkEnter
	fkBackspace
	fkSubmit // Ctrl-S
	fkCancel // Ctrl-C or Esc
	fkChar
)

func readFormKey(r *bufio.Reader) (formKey, rune, error) {
	b, err := r.ReadByte()
	if err != nil {
		return fkNone, 0, err
	}
	switch b {
	case 3: // Ctrl-C
		return fkCancel, 0, nil
	case 19: // Ctrl-S
		return fkSubmit, 0, nil
	case 13, 10:
		return fkEnter, 0, nil
	case 127, 8:
		return fkBackspace, 0, nil
	case 27:
		// Esc alone cancels; Esc [ A..D is an arrow. Everything else is
		// swallowed, because a half-read sequence typed as text is worse than a
		// keystroke that did nothing.
		if r.Buffered() == 0 {
			return fkCancel, 0, nil
		}
		if nb, _ := r.ReadByte(); nb == '[' {
			ab, _ := r.ReadByte()
			switch ab {
			case 'A':
				return fkUp, 0, nil
			case 'B':
				return fkDown, 0, nil
			case 'C':
				return fkRight, 0, nil
			case 'D':
				return fkLeft, 0, nil
			}
		}
		return fkNone, 0, nil
	}
	if b >= 32 && b < 127 {
		return fkChar, rune(b), nil
	}
	return fkNone, 0, nil
}

// handle applies one keystroke and reports whether to submit or quit.
func (f *shapeForm) handle(k formKey, ch rune) (submit, quit bool) {
	f.status = ""
	cur := f.flat[f.cursor].n

	if f.edit {
		switch k {
		case fkEnter:
			f.edit = false
			f.commit(cur)
		case fkCancel:
			f.edit = false
		case fkBackspace:
			if cur.value != "" {
				cur.value = cur.value[:len(cur.value)-1]
			}
		case fkChar:
			if cur.kind == kindInt && (ch < '0' || ch > '9') {
				f.status = "numbers only"
				return false, false
			}
			if f.fresh {
				cur.value = ""
				f.fresh = false
			}
			cur.value += string(ch)
			// Belt and braces on top of `fresh`: a number never carries a leading
			// zero, whatever route the keystrokes took to get here. "03" parses as
			// 3, which is exactly why it survives every check and still reads as a
			// mistake to the person who typed it.
			if cur.kind == kindInt {
				cur.value = strings.TrimLeft(cur.value, "0")
				if cur.value == "" {
					cur.value = "0"
				}
			}
		}
		if k == fkBackspace {
			f.fresh = false
		}
		return false, false
	}

	switch k {
	case fkUp:
		f.cursor--
	case fkDown:
		f.cursor++
	case fkRight:
		// On a value, the arrows change it — a closed set steps through its
		// options, a number counts up. Neither has children to expand, so nothing
		// is losing a meaning it had.
		switch {
		case cur.kind == kindChoice:
			f.step(cur, +1)
		case cur.kind == kindInt:
			f.nudge(cur, +1)
		case len(cur.children) > 0:
			cur.expanded = true
		}
	case fkLeft:
		switch {
		case cur.kind == kindChoice:
			f.step(cur, -1)
		case cur.kind == kindInt:
			f.nudge(cur, -1)
		case cur.expanded:
			cur.expanded = false
		default:
			f.collapseParent()
		}
	case fkEnter:
		switch cur.kind {
		case kindAction:
			return true, false
		case kindSection, kindGroup:
			cur.expanded = !cur.expanded
		case kindChoice:
			f.cycle(cur)
		default:
			f.edit = true
			f.fresh = true
		}
	case fkSubmit:
		return true, false
	case fkCancel:
		return false, true
	}
	return false, false
}

// commit applies a value the user finished typing.
func (f *shapeForm) commit(n *node) {
	// A number is normalised so the screen shows what was meant: an emptied
	// field reads as 0 rather than blank, and 007 reads as 7.
	if n.kind == kindInt {
		if strings.TrimSpace(n.value) == "" {
			n.value = "0"
		} else if v, err := strconv.Atoi(strings.TrimSpace(n.value)); err == nil {
			n.value = strconv.Itoa(v)
		}
	}
	if n.spawn == nil {
		return
	}
	if parent := f.parentOf(n); parent != nil {
		f.respawn(parent, n)
		parent.expanded = true
	}
}

// nudge counts a number up or down and applies it immediately.
//
// commit is called on every step, so a count's children appear and disappear as
// the arrow is held rather than waiting for an Enter that a stepping key does
// not otherwise need. It is also what keeps the live price honest: the value on
// screen is the value that was priced.
//
// An empty field starts at its floor rather than at zero, because → on a blank
// memory box means "give me the smallest one that works", not "give me none".
func (f *shapeForm) nudge(n *node, by int) {
	raw := strings.TrimSpace(n.value)
	if raw == "" {
		if by < 0 {
			return
		}
		n.value = strconv.Itoa(max(n.minV, 1))
		f.commit(n)
		return
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return
	}
	v += by
	if v < n.minV {
		v = n.minV
	}
	if n.maxV > 0 && v > n.maxV {
		v = n.maxV
		f.status = fmt.Sprintf("%s stops at %d", n.label, n.maxV)
	}
	n.value = strconv.Itoa(v)
	f.commit(n)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// step moves a choice by one, stopping at either end rather than wrapping.
//
// Wrapping is right for a key that only goes one way — Enter still cycles — and
// wrong for a pair that goes both: an arrow that jumps from the first option to
// the last reads as a mis-key, and there is no way to tell it apart from one.
func (f *shapeForm) step(n *node, by int) {
	for i, c := range n.choices {
		if c != n.value {
			continue
		}
		next := i + by
		if next < 0 || next >= len(n.choices) {
			return
		}
		n.value = n.choices[next]
		return
	}
	if len(n.choices) > 0 {
		n.value = n.choices[0]
	}
}

func (f *shapeForm) cycle(n *node) {
	for i, c := range n.choices {
		if c == n.value {
			n.value = n.choices[(i+1)%len(n.choices)]
			return
		}
	}
	if len(n.choices) > 0 {
		n.value = n.choices[0]
	}
}

func (f *shapeForm) parentOf(target *node) *node {
	var found *node
	var walk func(ns []*node, parent *node)
	walk = func(ns []*node, parent *node) {
		for _, n := range ns {
			if n == target {
				found = parent
				return
			}
			walk(n.children, n)
		}
	}
	walk(f.root, nil)
	return found
}

// collapseParent folds the section the cursor is inside and moves to it, so ←
// on a leaf walks out of the tree rather than doing nothing.
func (f *shapeForm) collapseParent() {
	cur := f.flat[f.cursor]
	for i := f.cursor - 1; i >= 0; i-- {
		if f.flat[i].depth < cur.depth {
			f.flat[i].n.expanded = false
			f.cursor = i
			return
		}
	}
}

// ─── running it ─────────────────────────────────────────────────────────────

// runShapeForm puts the terminal in raw mode, draws until the user submits or
// quits, and returns what they declared.
func runShapeForm(name string) (declaredShape, string, bool, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return declaredShape{}, "", false, err
	}
	// Restored on every path, including a panic: a terminal left raw is one the
	// shell cannot be typed into afterwards.
	defer func() {
		_ = term.Restore(fd, old)
		fmt.Print("\x1b[?25h\r\n")
	}()
	fmt.Print("\x1b[?25l")

	f := newShapeForm(name)

	// Keys arrive on a channel so the loop can also wake for a price. Blocking
	// on the read directly would mean a figure that arrived while nobody was
	// typing did not appear until the next keystroke — which is exactly when
	// somebody has stopped to look at it.
	type keyEvent struct {
		k   formKey
		ch  rune
		err error
	}
	keys := make(chan keyEvent)
	go func() {
		r := bufio.NewReader(os.Stdin)
		for {
			k, ch, err := readFormKey(r)
			keys <- keyEvent{k, ch, err}
			if err != nil {
				return
			}
		}
	}()

	prices := make(chan priceResult, 8)
	var debounce *time.Timer
	// repriceSoon waits for typing to stop before asking. Every keystroke would
	// otherwise be a round trip, and the answer to a shape three characters ago
	// is not worth the request that fetched it.
	repriceSoon := func() {
		f.priceGen++
		f.priceBusy = true
		f.priceErr = ""
		gen := f.priceGen
		cfg := f.priceableConfig() // built HERE, on this goroutine, never shared
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(300*time.Millisecond, func() {
			cents, err := priceOf(cfg, 1)
			prices <- priceResult{gen: gen, cents: cents, err: err}
		})
	}
	repriceSoon()

	for {
		f.render()
		select {
		case ev := <-keys:
			if ev.err != nil {
				return declaredShape{}, "", false, nil
			}
			before := f.fingerprint()
			submit, quit := f.handle(ev.k, ev.ch)
			if quit {
				return declaredShape{}, "", false, nil
			}
			if submit {
				shape, chosen, verr := f.collect()
				if verr != nil {
					f.status = verr.Error()
					continue
				}
				return shape, chosen, true, nil
			}
			// Only a change of VALUES reprices. Moving the cursor or folding a
			// section does not alter what the slice costs, and asking anyway
			// would make simply reading the form generate traffic.
			if f.fingerprint() != before {
				repriceSoon()
			}
		case p := <-prices:
			if p.gen != f.priceGen {
				continue // an answer to a shape that has since been edited
			}
			f.priceBusy = false
			if p.err != nil {
				f.priceErr = p.err.Error()
				continue
			}
			f.priceCents, f.priceKnown, f.priceErr = p.cents, true, ""
		}
	}
}

type priceResult struct {
	gen   int
	cents int
	err   error
}

// fingerprint is every value in the tree, in order. Comparing it is how the
// loop tells an edit from a keystroke that only moved the cursor.
func (f *shapeForm) fingerprint() string {
	var b strings.Builder
	var walk func(ns []*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			b.WriteString(n.value)
			b.WriteByte(0x1f)
			walk(n.children)
		}
	}
	walk(f.root)
	return b.String()
}

// collect turns the tree into the shape, refusing anything the platform would
// refuse — while the screen is still up and the field can still be reached.
func (f *shapeForm) collect() (declaredShape, string, error) {
	name := strings.TrimSpace(f.root[0].value)
	if err := validSliceName(name); err != nil {
		return declaredShape{}, "", fmt.Errorf("slice name: %s", err)
	}

	shape := declaredShape{Backbone: backboneShape{}}
	section := func(label string) *node {
		for _, n := range f.root {
			if n.label == label {
				return n
			}
		}
		return nil
	}

	atomic := section("Atomic")
	shape.StorageMiB = intOf(childValue(atomic, "Code & dependencies"))

	for _, g := range atomic.children {
		if g.kind != kindGroup || !strings.HasPrefix(g.label, "Function ") {
			continue
		}
		route := strings.TrimSpace(childValue(g, "Route"))
		if err := validRoute(route); err != nil {
			return declaredShape{}, "", fmt.Errorf("%s route: %s", g.label, err)
		}
		mem := intOf(childValue(g, "Memory"))
		if err := validMemory(strconv.Itoa(mem)); err != nil {
			return declaredShape{}, "", fmt.Errorf("%s memory: %s", g.label, err)
		}
		shape.Slots = append(shape.Slots, functionSlot{
			Method: childValue(g, "Method"), Route: route, Memory: mem,
		})
	}

	bb := section("Backbone")
	var err error
	if shape.Backbone.Collections, err = gather(bb, "Collection ", "size"); err != nil {
		return declaredShape{}, "", err
	}
	if shape.Backbone.Databases, err = gather(bb, "Database ", "size"); err != nil {
		return declaredShape{}, "", err
	}
	if shape.Backbone.Buckets, err = gather(bb, "Bucket ", "size"); err != nil {
		return declaredShape{}, "", err
	}
	if shape.Backbone.Queues, err = gather(bb, "Queue ", "depth"); err != nil {
		return declaredShape{}, "", err
	}

	shape.CanvasMiB = intOf(childValue(section("Canvas"), "Site storage"))
	return shape, name, nil
}

// gather reads one counted list out of a section.
func gather(section *node, prefix, sizeLabel string) (map[string]int, error) {
	out := map[string]int{}
	for _, g := range section.children {
		if g.kind != kindGroup || !strings.HasPrefix(g.label, prefix) {
			continue
		}
		name := strings.TrimSpace(childValue(g, "Name"))
		if name == "" {
			return nil, fmt.Errorf("%s needs a name", g.label)
		}
		if _, clash := out[name]; clash {
			return nil, fmt.Errorf("two of them are called %q", name)
		}
		size := intOf(childValue(g, sizeLabel))
		if size <= 0 {
			return nil, fmt.Errorf("%s %s must be above zero — the slice reads zero as unbounded",
				g.label, sizeLabel)
		}
		out[name] = size
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func childValue(parent *node, label string) string {
	if parent == nil {
		return ""
	}
	for _, c := range parent.children {
		if c.label == label {
			return c.value
		}
	}
	return ""
}

func intOf(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// sortedKeys keeps a summary stable across runs of the same answers.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
