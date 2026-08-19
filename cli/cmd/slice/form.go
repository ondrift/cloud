package slice

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

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
	// spawnInto names the section the spawned groups are appended to. Empty
	// means the node's own parent.
	spawnKey string
}

// shapeForm is the whole screen.
type shapeForm struct {
	root   []*node
	flat   []*row // what is currently visible, rebuilt on every render
	cursor int
	edit   bool // the focused row is being typed into
	// fresh means nothing has been typed since editing began, so the first
	// character REPLACES what is there rather than appending to it. Without it,
	// a field showing the default 0 answers a typed 2 with "02" — which parses,
	// which is why it would survive review, and which is not what anybody meant.
	fresh  bool
	status string
	width  int
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

	f.root = []*node{
		{label: "Slice name", kind: kindText, value: name,
			hint: "lowercase letters, numbers and dashes"},
		{label: "Atomic", kind: kindSection, children: []*node{
			{label: "Code & dependencies", kind: kindInt, unit: "MB", value: "0",
				hint: "your deployed code plus whatever it vendors"},
			{label: "Functions", kind: kindInt, value: "0", spawn: functionGroups,
				hint: "each books its own memory; the slice is billed the sum"},
		}},
		{label: "Backbone", kind: kindSection, children: []*node{
			{label: "NoSQL collections", kind: kindInt, value: "0",
				spawn: namedGroups("Collection", "size", "MB"),
				hint:  "declared by name — the slice refuses a write to any other"},
			{label: "SQL databases", kind: kindInt, value: "0",
				spawn: namedGroups("Database", "size", "MB")},
			{label: "Blob buckets", kind: kindInt, value: "0",
				spawn: namedGroups("Bucket", "size", "MB")},
			{label: "Queues", kind: kindInt, value: "0",
				spawn: namedGroups("Queue", "depth", "messages")},
		}},
		{label: "Canvas", kind: kindSection, children: []*node{
			{label: "Site storage", kind: kindInt, unit: "MB", value: "0",
				hint: "total across every site on the slice"},
		}},
		{label: "Deed", kind: kindSection, children: []*node{
			{label: "Vault entry size", kind: kindInt, unit: "KB", value: "0"},
			{label: "Vault entries per identity", kind: kindInt, value: "0"},
			{label: "Pocket record size", kind: kindInt, unit: "KB", value: "0"},
		}},
	}
	return f
}

func functionGroups(n int) []*node {
	out := make([]*node, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, &node{
			label: fmt.Sprintf("Function %d", i), kind: kindGroup, children: []*node{
				{label: "Method", kind: kindChoice, value: "post", choices: httpMethods},
				{label: "Route", kind: kindText, hint: "the path under /api/, e.g. auth/challenge"},
				{label: "Memory", kind: kindInt, unit: "MB",
					hint: fmt.Sprintf("%d-%d; Go and Rust need %d or more",
						minMemoryMiB, maxMemoryMiB, compiledFloorMiB)},
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
			out = append(out, &node{
				label: fmt.Sprintf("%s %d", noun, i), kind: kindGroup, children: []*node{
					{label: "Name", kind: kindText},
					{label: sizeLabel, kind: kindInt, unit: unit},
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

const (
	fReset = "\x1b[0m"
	fDim   = "\x1b[2m"
	fBold  = "\x1b[1m"
	fCyan  = "\x1b[36m"
	fInv   = "\x1b[7m"
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
	fmt.Fprintf(&b, "  %s↑↓ move · → expand · ← collapse · ⏎ edit · ^S create · ^C cancel%s\r\n\r\n",
		fDim, fReset)

	for i, r := range f.flat {
		b.WriteString(f.line(i, r))
	}

	b.WriteString("\r\n")
	if f.status != "" {
		fmt.Fprintf(&b, "  %s%s%s\r\n", fCyan, f.status, fReset)
	} else if hint := f.flat[f.cursor].n.hint; hint != "" {
		fmt.Fprintf(&b, "  %s%s%s\r\n", fDim, hint, fReset)
	}
	fmt.Fprint(os.Stdout, b.String())
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
	if r.n.kind == kindSection {
		label = fBold + label + fReset
	}

	value := ""
	switch r.n.kind {
	case kindSection, kindGroup:
	default:
		shown := r.n.value
		if shown == "" {
			shown = fDim + "…" + fReset
		}
		if f.edit && i == f.cursor {
			shown = fInv + r.n.value + " " + fReset
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
		if len(cur.children) > 0 {
			cur.expanded = true
		}
	case fkLeft:
		if cur.expanded {
			cur.expanded = false
		} else {
			f.collapseParent()
		}
	case fkEnter:
		switch cur.kind {
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
	r := bufio.NewReader(os.Stdin)
	for {
		f.render()
		k, ch, rerr := readFormKey(r)
		if rerr != nil {
			return declaredShape{}, "", false, nil
		}
		submit, quit := f.handle(k, ch)
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
	}
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
