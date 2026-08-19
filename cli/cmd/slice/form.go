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
	kindItem                    // a list item: expandable, and its VALUE is its name
	kindAdd                     // + Add function — appends a group above itself
	kindAction                  // Create — the one row that does something
)

type node struct {
	label       string
	kind        nodeKind
	value       string
	choices     []string
	unit        string
	hint        string
	placeholder string
	children    []*node
	expanded    bool

	// A list is a run of kindGroup rows followed by the kindAdd row that makes
	// them. Both carry the same prefix, which is how a section holding four
	// lists tells its own children apart; only the adder carries the factory.
	prefix string
	make   func(n int) *node
	limit  int
	// The range ← and → step this number through. maxV of 0 means no ceiling
	// this form knows of — the platform still has one, and still enforces it.
	minV, maxV int
	// scalar names the Backbone field this row writes, dotted from Backbone
	// down, and scale converts the unit shown into the unit stored. Empty means
	// the row is not a Backbone scalar.
	scalar string
	scale  int
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
	fresh    bool
	editPrev string
	status   string
	width    int

	// The live price, and what is known about it.
	//
	// priceGen rises on every edit. A reply carrying a stale generation is
	// dropped, so a slow request that started three keystrokes ago cannot
	// overwrite the answer to the current shape — the figure on screen is always
	// the figure for what is on screen, or nothing.
	priceCents    int
	priceSections map[string]int
	priceKnown    bool
	priceBusy     bool
	priceErr      string
	priceGen      int
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
		hint: "Creates the slice at the price shown here. There is no second " +
			"confirmation — this row is it, which is why the figure is on it."}

	f.root = []*node{
		{label: "Slice name", kind: kindText, value: name, placeholder: "e.g. my-project",
			hint: "The slice's name, and the hostname it answers on. " +
				"Lowercase letters, numbers and dashes, up to 32."},

		{label: "Atomic", kind: kindSection,
			hint: "Functions, and the volume their code lives on.",
			children: []*node{
				{label: "Code & dependencies", kind: kindInt, unit: "MB", value: "0", minV: 0,
					scalar: "atomic.MaxStorageBytes", scale: bytesPerMiB,
					hint: "The runner volume: your deployed code plus everything it " +
						"vendors. Billed per GiB. Whole MB, 0 if you deploy no code."},
				{label: "Scheduled jobs", kind: kindInt, value: "0", minV: 0,
					scalar: "atomic.MaxNumberOfScheduledJobs", placeholder: "how many",
					hint: "Cron-triggered runs the slice may hold. PRICED per job, because " +
						"one fires on a timer whether or not anything calls the slice."},
				{label: "Function timeout", kind: kindInt, unit: "seconds", value: "0", minV: 0,
					scalar: "atomic.MaxFunctionRuntimeInSeconds", placeholder: "seconds",
					hint: "How long one invocation may run before it is killed. Free."},
				{label: "Requests per minute", kind: kindInt, value: "0", minV: 0,
					scalar: "atomic.MaxNumberOfRequestsPerMinute", placeholder: "per minute",
					hint: "The slice-wide rate ceiling. Shed requests answer 429. Free."},
				{label: "Log retention", kind: kindInt, unit: "hours", value: "0", minV: 0,
					scalar: "atomic.MaxNumberOfHoursForLogRetention", placeholder: "hours",
					hint: "How long invocation logs are kept. Free."},
				{label: "Deploy history", kind: kindInt, unit: "versions", value: "0", minV: 0,
					scalar: "atomic.MaxNumberOfDeploymentsInHistory", placeholder: "versions",
					hint: "Rollback slots kept per function. Free."},
				adder("Add function", "Function ", maxFunctionSlots, functionGroup,
					"Adds a function slot. Each books its own memory and is billed "+
						"from the moment it exists, deployed or not."),
			}},

		{label: "Backbone", kind: kindSection,
			hint: "Storage. Each item is declared BY NAME — a write to a name the " +
				"slice does not carry is refused, not created.",
			children: []*node{
				// One sub-section per primitive, rather than four adders in a row.
				// The adders alone left no way to tell what Backbone HAS from what
				// it could have: an empty slice and a full one looked the same, and
				// four "+ Add" lines read as one list of four things to add rather
				// than four lists.
				{label: "NoSQL", kind: kindSection,
					hint: "Document collections — the store most apps reach for first.",
					children: []*node{
						adder("Add collection", "Collection ", 64,
							namedGroup("Collection", "size", "MB"),
							"Adds a document collection, named and sized separately."),
					}},
				{label: "SQL", kind: kindSection,
					hint: "Per-slice SQLite files, encrypted at rest, one file each.",
					children: []*node{
						adder("Add database", "Database ", 64,
							namedGroup("Database", "size", "MB"),
							"Adds a per-slice SQLite file."),
					}},
				{label: "Blobs", kind: kindSection,
					hint: "Object storage for files.",
					children: []*node{
						{label: "Object size", kind: kindInt, unit: "MB", value: "0", minV: 0,
							scalar: "backbone.blobs.MaxSizeInBytesEach", scale: bytesPerMiB,
							placeholder: "MB per object",
							hint: "Ceiling on ONE object, separate from a bucket's total. " +
								"A 5 MB bucket with a 5 MB object size holds one object."},
						adder("Add bucket", "Bucket ", 64,
							namedGroup("Bucket", "size", "MB"),
							"Adds a bucket, named and sized separately."),
					}},
				{label: "Queues", kind: kindSection,
					hint: "Message queues a QUEUE-method function drains.",
					children: []*node{
						{label: "Default depth", kind: kindInt, unit: "messages", value: "0", minV: 0,
							scalar: "backbone.queues.MaxDepthEach", placeholder: "messages",
							hint: "Bounds a queue that names no depth of its own."},
						adder("Add queue", "Queue ", 64,
							namedGroup("Queue", "depth", "messages"),
							"Adds a queue, sized in messages held rather than bytes."),
					}},
				{label: "Secrets", kind: kindSection,
					hint: "Values a function reads at run time and nobody reads back.",
					children: []*node{
						{label: "Count", kind: kindInt, value: "0", minV: 0,
							scalar: "backbone.secrets.MaxCount", placeholder: "how many",
							hint: "How many secrets the slice may hold. Free."},
						{label: "Size each", kind: kindInt, unit: "KB", value: "0", minV: 0,
							scalar: "backbone.secrets.MaxSizeInBytesEach", scale: 1024,
							placeholder: "KB", hint: "Per-secret ceiling. Free."},
					}},
				{label: "Realtime", kind: kindSection,
					hint: "Live WebSocket connections to the slice's own hub.",
					children: []*node{
						{label: "Concurrent connections", kind: kindInt, value: "0", minV: 0,
							scalar: "backbone.realtime.MaxConcurrentConnections", placeholder: "how many",
							hint: "Live connections at once. PRICED per connection, because " +
								"each holds memory on the in-slice hub for as long as it is open."},
					}},
				{label: "Locks", kind: kindSection,
					hint: "Advisory locks held at once.",
					children: []*node{
						{label: "Concurrent locks", kind: kindInt, value: "0", minV: 0,
							scalar: "backbone.locks.MaxConcurrent", placeholder: "how many",
							hint: "Locks held simultaneously. Cheap, and free."},
					}},
				{label: "Backups", kind: kindSection,
					hint: "Automatic backups of the slice's data.",
					children: []*node{
						{label: "Retention", kind: kindInt, unit: "days", value: "0", minV: 0,
							scalar: "backbone.BackupRetentionDays", placeholder: "days",
							hint: "How long automatic backups are kept. Lowering it later " +
								"prunes archives outside the new window."},
					}},
			}},

		{label: "Canvas", kind: kindSection,
			hint: "Static sites served at the slice's own root.",
			children: []*node{
				{label: "Site storage", kind: kindInt, unit: "MB", value: "0", minV: 0,
					scalar: "canvas.TotalMaxSizeInBytes", scale: bytesPerMiB, placeholder: "MB",
					hint: "Total across every site on the slice, not per site. Billed " +
						"per GiB. Whole MB, 0 if you publish no site."},
			}},

		{label: "Deed", kind: kindSection,
			hint: "Identity: key material, and per-person app data.",
			children: []*node{
				{label: "Vault entry size", kind: kindInt, unit: "KB", value: "0", minV: 0,
					scalar: "deed.vault.MaxSizeInBytesEach", scale: 1024, placeholder: "KB",
					hint: "Ceiling on one wrapped keyring entry. Key material, not " +
						"documents. Whole KB."},
				{label: "Vault entries per identity", kind: kindInt, value: "0", minV: 0,
					scalar: "deed.vault.MaxEntriesPerUID", placeholder: "how many",
					hint: "How much key history one identity accumulates. Vault is " +
						"append-only, so past this the oldest go. Whole number."},
				{label: "Pocket record size", kind: kindInt, unit: "KB", value: "0", minV: 0,
					scalar: "deed.pocket.MaxSizeInBytesEach", scale: 1024, placeholder: "KB",
					hint: "Ceiling on one per-identity record — an app's own data for " +
						"one person. Whole KB."},
			}},

		f.createNode,
	}
	return f
}

// adder builds the "+ Add …" row that ends a list.
func adder(label, prefix string, limit int, make func(int) *node, hint string) *node {
	return &node{label: label, kind: kindAdd, prefix: prefix, limit: limit,
		make: make, hint: hint}
}

func functionGroup(i int) *node {
	return &node{
		label: "route", kind: kindItem, placeholder: "route, e.g. auth/challenge",
		hint: "Type this function's route — the path under /api/, e.g. auth/challenge. " +
			"No leading slash. A QUEUE function names the queue it drains instead. " +
			"The route is what the function is called; open it for the method and memory.",
		children: []*node{
			{label: "Method", kind: kindChoice, value: "post", choices: httpMethods,
				hint: "← → to choose. Part of the function's identity, not a detail — " +
					"get:items and post:items are two different functions. QUEUE names " +
					"a queue to drain instead of a path."},
			{label: "Memory", kind: kindInt, unit: "MB", minV: minMemoryMiB, maxV: maxMemoryMiB,
				placeholder: fmt.Sprintf("%d-%d", minMemoryMiB, maxMemoryMiB),
				hint: fmt.Sprintf("The pool this function's SIMULTANEOUS calls share — it "+
					"buys concurrency, not headroom for one call. %d-%d MB; Go and "+
					"Rust need %d or more.", minMemoryMiB, maxMemoryMiB, compiledFloorMiB)},
		},
	}
}

// namedGroup builds "Collection 1 { Name, size }" and its siblings. The second
// field is a size in MB for storage and a depth in messages for a queue, which
// is why the label and unit are parameters rather than assumed.
func namedGroup(noun, sizeLabel, unit string) func(int) *node {
	return func(i int) *node {
		sizeHint := fmt.Sprintf("How large this %s may grow, in whole %s. Must be "+
			"above zero: the slice reads a zero cap as UNLIMITED and the bill "+
			"clamps it to nothing.", strings.ToLower(noun), unit)
		if unit == "messages" {
			sizeHint = "How many messages this queue may hold at once. Whole number, " +
				"above zero. This is a depth, not a size in bytes."
		}
		return &node{
			label: strings.ToLower(noun), kind: kindItem,
			placeholder: fmt.Sprintf("%s name", strings.ToLower(noun)),
			hint: fmt.Sprintf("Type the exact string your code addresses this %s by. "+
				"A write to any other name is refused with 400, not created — so this "+
				"is the resource, not a label for it. Open it to set the size.",
				strings.ToLower(noun)),
			children: []*node{
				{label: sizeLabel, kind: kindInt, unit: unit, minV: 1, placeholder: unit, hint: sizeHint},
			},
		}
	}
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

// The pillar colours, the same truecolor values the dashboard uses
// (cmd/portal/layout.go) and the brand's own: Atomic #f1a006, Backbone #8269eb,
// Canvas #10b981.
//
// Deed has NO colour here, and that is the whole point rather than an omission.
// Its brand colour is white on a dark ground and the ink colour on a light one,
// which a terminal cannot be asked about — there is no way to read the
// background, and guessing wrong makes the one section that should be the most
// legible the least. Leaving it at the terminal's own foreground gets both
// cases right for free: white on dark, dark on light, chosen by the thing that
// actually knows.
var pillarColour = map[string]string{
	"Atomic":   "\x1b[38;2;241;160;6m",
	"Backbone": "\x1b[38;2;130;105;235m",
	"Canvas":   "\x1b[38;2;16;185;129m",
}

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
	fmt.Fprintf(&b, "  %s%s%s\r\n\r\n", fDim, f.keyHints(), fReset)

	for i, r := range f.flat {
		if r.n == f.createNode {
			// The bill closes before the button. A blank line, then the total on
			// its own row, then Create — so the screen reads top to bottom as an
			// itemised bill whose last line is the thing that pays it, rather than
			// a form with a figure tucked beside a button.
			b.WriteString("\r\n")
			b.WriteString(f.totalLine())
		}
		b.WriteString(f.line(i, r))
	}

	b.WriteString("\r\n")
	if f.status != "" {
		fmt.Fprintf(&b, "  %s%s%s\r\n", fCyan, f.status, fReset)
	} else if hint := hintFor(f.flat[f.cursor].n); hint != "" {
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

// keyHints lists only the keys that do something to the row under the cursor.
//
// A fixed line has to name every key the form has, which means most of it is
// false at any moment: ←→ does nothing on a name, ⏎ types nothing into a
// section, and a reader working out which half applies to them is doing the
// work the line was supposed to save. Naming two keys that both work beats
// naming six of which two do.
func (f *shapeForm) keyHints() string {
	if f.edit {
		return "⏎ done · ⌫ delete · esc discard"
	}

	n := f.flat[f.cursor].n
	switch n.kind {
	case kindItem:
		return "just type · ⏎ done · ↑↓ move · ^D remove"
	case kindGroup:
		if n.expanded {
			return "↑↓ move · space close"
		}
		return "↑↓ move · space open"
	case kindSection:
		if n.expanded {
			return "↑↓ move · space close"
		}
		return "↑↓ move · space open"
	case kindInt:
		return "↑↓ move · ←→ change · or type a number"
	case kindChoice:
		return "↑↓ move · ←→ choose"
	case kindAdd:
		return "↑↓ move · space add"
	case kindAction:
		return "↑↓ move · ⏎ create"
	}
	// Text. ← has no value to step, so it is the way back out of a group.
	return "just type · ⏎ done · ↑↓ move"
}

// queueTriggered reports whether this item is a function whose method is queue.
func queueTriggered(n *node) bool {
	return n.kind == kindItem && n.prefix == "Function " && childValue(n, "Method") == "queue"
}

// promptFor is the row's own placeholder, and hintFor its explanation, with one
// substitution: a queue-triggered function does not have a route.
//
// models.AtomicFunction carries no queue field — "a queue-triggered function has
// method `queue` and its queue name as the route", and its comment warns that
// spelling it any other way silently un-books every queue-driven function. So
// there is one field, and what changes is what it is called.
func promptFor(n *node) string {
	if queueTriggered(n) {
		return "queue name, e.g. emails"
	}
	return n.placeholder
}

func hintFor(n *node) string {
	if queueTriggered(n) {
		return "Type the name of the QUEUE this function drains — not a path. " +
			"Lowercase letters, digits and interior hyphens, up to 32: no slashes " +
			"and no underscores. It is the same field because the platform stores " +
			"a queue function's queue name as its route."
	}
	return n.hint
}

// sectionPrice is a pillar's own subtotal, padded so the column lines up
// however long the pillar's name is.
//
// A section with nothing priced in it says nothing rather than "EUR 0.00": a
// zero reads as a line somebody is being charged nothing for, and most of these
// sections are simply empty. Deed never prices at all — it has no billed unit —
// so it stays blank always, which is also the honest answer.
func (f *shapeForm) sectionPrice(name string) string {
	if !f.priceKnown {
		return ""
	}
	cents := f.priceSections[name]
	if cents <= 0 {
		return ""
	}
	pad := 24 - len(name)
	if pad < 2 {
		pad = 2
	}
	return strings.Repeat(" ", pad) + fGreen + euros(cents) + "/mo" + fReset
}

// totalLine is the bill's last figure, above the button that agrees to it.
func (f *shapeForm) totalLine() string {
	return fmt.Sprintf("    %-24s%s\r\n", "Total", f.priceLabel())
}

// priceLabel is the total itself.
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
			if g.kind != kindItem {
				continue
			}
			if mem := intOf(childValue(g, "Memory")); mem > 0 {
				shape.Slots = append(shape.Slots, functionSlot{
					Method: childValue(g, "Method"),
					Route:  strings.TrimSpace(g.value),
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
		for i, g := range itemsUnder(backbone, prefix) {
			size := intOf(childValue(g, sizeLabel))
			if size <= 0 {
				continue
			}
			name := strings.TrimSpace(g.value)
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
	shape.BackboneScalars = f.scalars(nil)
	return buildConfig(shape.StorageMiB, shape.Slots, shape.Backbone, shape.CanvasMiB, shape.BackboneScalars)
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

// countItems is how many list items a section holds, at any depth.
//
// It is what puts [2] beside NoSQL. A folded section otherwise looks identical
// whether it holds nothing or holds everything, and the count answers the
// question a mark only raised: not "is there something in here" but "how much",
// which is the thing you fold a section to stop looking at.
func countItems(n *node) int {
	total := 0
	for _, c := range n.children {
		if c.kind == kindItem {
			total++
		}
		total += countItems(c)
	}
	return total
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func (f *shapeForm) line(i int, r *row) string {
	indent := strings.Repeat("    ", r.depth)

	// No chevrons. Depth already says what contains what, and a glyph that only
	// repeats the indentation is one more thing between the reader and the
	// numbers this screen exists to show.
	marker := "  "

	label := r.n.label
	switch r.n.kind {
	case kindItem:
		// The item IS its name, so the row shows the value rather than a
		// stand-in like "Function 1". An unnamed one shows what to type, dim,
		// because a blank row gives no clue what it wants.
		if r.n.value == "" {
			label = fDim + promptFor(r.n) + fReset
		} else {
			label = r.n.value
		}
		if f.edit && i == f.cursor {
			// The prompt stays up while the field is empty and goes the moment a
			// character lands. A caret alone says "type", never what — and this
			// row is reached by ADDING something, so the question it is asking is
			// the one thing the screen has not said yet.
			label = fUnder + r.n.value + fReset + fCyan + "▏" + fReset
			if r.n.value == "" {
				label += fDim + promptFor(r.n) + fReset
			}
		}
	case kindSection:
		// (*) marks a SUB-section that holds something. The pillars do not carry
		// it: they carry a price, which says the same thing with a figure — a
		// pillar with items has a subtotal, and one without has nothing to show.
		// Two marks for one fact on the same row would be one too many.
		if n := countItems(r.n); r.depth > 0 && n > 0 {
			label = fmt.Sprintf("[%d] %s", n, label)
		}
		if c, ok := pillarColour[label]; ok {
			label = c + fBold + label + fReset
		} else {
			label = fBold + label + fReset
		}
		label += f.sectionPrice(r.n.label)
	case kindAdd:
		label = fCyan + "+ " + label + fReset
		marker = "  "
	case kindAction:
		label = fGreen + fBold + label + fReset
		marker = "  "
	}

	value := ""
	switch r.n.kind {
	case kindSection, kindGroup, kindAction, kindAdd, kindItem:
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
			shown = fDim + fallback(r.n.placeholder, "…") + fReset
		}
		if f.edit && i == f.cursor {
			shown = fUnder + r.n.value + fReset + fCyan + "▏" + fReset
			if r.n.value == "" && r.n.placeholder != "" {
				shown += fDim + r.n.placeholder + fReset
			}
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
	// Cancel is Ctrl-C or Esc. It is not advertised on the header line, because
	// it is what those keys already do everywhere and a form that has to say so
	// is spending a line on it.
	//
	// There is deliberately no submit KEY. Creating a slice happens in exactly
	// one place — the green row at the foot — so there is one answer to "how do
	// I create it", and it is on screen. A second, invisible way to spend money
	// is worth less than the line it would take to explain.
	fkCancel
	fkSpace
	fkRemove
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
	case 4: // Ctrl-D — remove the item under the cursor
		return fkRemove, 0, nil
	case 13, 10:
		return fkEnter, 0, nil
	case 127, 8:
		return fkBackspace, 0, nil
	case 27:
		// Esc alone cancels; Esc [ A..D is an arrow.
		//
		// Decided by PEEKING, not by whether anything is buffered. Buffering is a
		// property of how fast the bytes arrived, not of what was pressed: a lone
		// Esc inside a burst — a paste, or simply typing quickly — arrives with
		// the NEXT keystroke already in the buffer, and reading that byte as part
		// of a sequence swallowed both. Esc appeared to do nothing at all,
		// intermittently, which is the worst way for a key to fail.
		next, perr := r.Peek(1)
		if perr != nil || (next[0] != '[' && next[0] != 'O') {
			return fkCancel, 0, nil
		}
		if nb, _ := r.ReadByte(); nb == '[' || nb == 'O' {
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
			// Esc DISCARDS, which is what the hint has always said and what it
			// did not do: it used to keep whatever had been typed, so the only
			// way out of a half-typed field was to finish it or delete it by
			// hand.
			f.edit = false
			cur.value = f.editPrev
			// An item that ends this with no name was never named at all —
			// almost always one added a keystroke ago and thought better of.
			// Leaving it behind means a blank row that refuses the whole form at
			// submit, for a decision already made.
			if cur.kind == kindItem && strings.TrimSpace(cur.value) == "" {
				f.removeItem(cur)
			}
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

	// Outside a field, letters are movement. hjkl only reaches here when nothing
	// is being typed into — inside a field they are letters again, which is the
	// whole reason the translation lives here and not in the decoder.
	// A row you can type into is typed into. Highlight a route and start
	// spelling it; Enter was asking permission to do the only thing that row is
	// for.
	//
	// This is why there is no hjkl. A key cannot be both a letter and a command
	// on a field whose value is letters, and keeping the pair would have meant
	// four keys that move on some rows and type on others — the reader having to
	// know which kind of row they are on before they know what a key does. The
	// arrows move, always; everything printable types, wherever typing is
	// possible.
	if k == fkChar && typesInto(cur, ch) {
		f.edit, f.editPrev, f.fresh = true, cur.value, false
		cur.value = string(ch)
		if cur.kind == kindInt {
			cur.value = strings.TrimLeft(cur.value, "0")
			if cur.value == "" {
				cur.value = "0"
			}
		}
		return false, false
	}

	// Space is the only letter-shaped key left that is not a letter. It folds,
	// and typesInto refuses it above, so no name can contain one — which is true
	// of every name this form collects anyway: routes, queues, collections and
	// slices all forbid spaces.
	if k == fkChar && ch == ' ' {
		k = fkSpace
	}

	switch k {
	case fkUp:
		f.cursor--
	case fkDown:
		f.cursor++
	case fkSpace:
		switch cur.kind {
		case kindAdd:
			f.addItem(cur)
		case kindSection, kindGroup, kindItem:
			cur.expanded = !cur.expanded
		}
	case fkRemove:
		if cur.kind == kindItem {
			f.removeItem(cur)
		}
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
		case kindItem:
			f.edit, f.fresh, f.editPrev = true, true, cur.value
		case kindSection, kindGroup:
			cur.expanded = !cur.expanded
		case kindChoice:
			f.cycle(cur)
		default:
			f.edit, f.fresh, f.editPrev = true, true, cur.value
		}
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
}

// addItem inserts one new group directly above the adder that made it, so a
// list reads top to bottom and the "+ Add" row stays at its foot.
func (f *shapeForm) addItem(add *node) {
	parent := f.parentOf(add)
	if parent == nil {
		return
	}

	at, count := -1, 0
	for i, c := range parent.children {
		if c == add {
			at = i
		}
		if c.kind == kindItem && c.prefix == add.prefix {
			count++
		}
	}
	if at < 0 {
		return
	}
	if add.limit > 0 && count >= add.limit {
		f.status = fmt.Sprintf("a slice holds %d at most", add.limit)
		return
	}

	item := add.make(count + 1)
	item.expanded = true // opened, because nobody adds one to leave it empty
	item.prefix = add.prefix

	grown := make([]*node, 0, len(parent.children)+1)
	grown = append(grown, parent.children[:at]...)
	grown = append(grown, item)
	grown = append(grown, parent.children[at:]...)
	parent.children = grown

	// Land on the new item WITH THE CARET UP. Adding a function is a step
	// towards naming one, and the name is the next thing anybody types; making
	// them press Enter first is a keystroke that asks "did you mean it?" about
	// something they just chose to do.
	f.visible()
	for i, r := range f.flat {
		if r.n == item {
			f.cursor = i
			f.edit, f.fresh, f.editPrev = true, true, ""
			break
		}
	}
}

// removeItem drops a group and renumbers what is left, so the labels stay
// 1..N and never carry a gap somebody has to reason about.
func (f *shapeForm) removeItem(item *node) {
	parent := f.parentOf(item)
	if parent == nil {
		return
	}
	kept := make([]*node, 0, len(parent.children))
	for _, c := range parent.children {
		if c != item {
			kept = append(kept, c)
		}
	}
	parent.children = kept

	f.status = "removed"
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
// typesInto reports whether this keystroke should start editing the row.
//
// Text takes any printable character. A NUMBER takes only digits, which is what
// lets hjkl keep moving on a memory box: a letter there could never have been
// the value, so reading it as movement costs nothing.
func typesInto(n *node, ch rune) bool {
	switch n.kind {
	case kindText, kindItem:
		return ch != ' '
	case kindInt:
		return ch >= '0' && ch <= '9'
	}
	return false
}

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
			cents, per, err := priceBySection(cfg)
			prices <- priceResult{gen: gen, cents: cents, sections: per, err: err}
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
			f.priceCents, f.priceSections, f.priceKnown, f.priceErr = p.cents, p.sections, true, ""
		}
	}
}

type priceResult struct {
	gen      int
	cents    int
	sections map[string]int
	err      error
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
		if g.kind != kindItem || g.prefix != "Function " {
			continue
		}
		route := strings.TrimSpace(g.value)
		// A queue-triggered function is addressed by its queue, and a queue name
		// obeys a different, tighter rule than a path. Checking it as a route
		// accepted queue_test here and had it refused after the create was
		// priced, summarised and sent.
		check, what := validRoute, "route"
		if childValue(g, "Method") == "queue" {
			check, what = validQueueName, "queue name"
		}
		if err := check(route); err != nil {
			return declaredShape{}, "", fmt.Errorf("function %d %s: %s", len(shape.Slots)+1, what, err)
		}
		mem := intOf(childValue(g, "Memory"))
		if err := validMemory(strconv.Itoa(mem)); err != nil {
			return declaredShape{}, "", fmt.Errorf("%s memory: %s", route, err)
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
	shape.BackboneScalars = f.scalars(nil)
	return shape, name, nil
}

// scalars collects every Backbone dial that was actually set.
//
// Keyed by the DOTTED PATH the config uses, with the leaf spelled the way
// encoding/json will look for it — models.SliceConfig tags its containers but
// not its scalar limits, so `secrets.MaxCount` is the key and
// `secrets.max_count` decodes to zero with nothing reported.
//
// A zero is left out rather than sent. Zero means "not declared" on this ingest
// path, and the platform fills it from its own defaults; writing it explicitly
// would turn every dial the tenant never touched into a declaration of nothing.
func (f *shapeForm) scalars(_ *node) map[string]int {
	out := map[string]int{}
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, c := range ns {
			if c.scalar != "" {
				if v := intOf(c.value); v > 0 {
					scale := c.scale
					if scale == 0 {
						scale = 1
					}
					out[c.scalar] = v * scale
				}
			}
			walk(c.children)
		}
	}
	walk(f.root)
	return out
}

// gather reads one counted list out of a section.
func gather(section *node, prefix, sizeLabel string) (map[string]int, error) {
	out := map[string]int{}
	for _, g := range itemsUnder(section, prefix) {
		name := strings.TrimSpace(g.value)
		if name == "" {
			return nil, fmt.Errorf("a %s has no name", strings.TrimSpace(strings.ToLower(prefix)))
		}
		if _, clash := out[name]; clash {
			return nil, fmt.Errorf("two of them are called %q", name)
		}
		size := intOf(childValue(g, sizeLabel))
		if size <= 0 {
			return nil, fmt.Errorf("%s: %s must be above zero — the slice reads zero as unbounded",
				name, sizeLabel)
		}
		out[name] = size
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// itemsUnder finds every list item of one kind at any depth, so Backbone's
// sub-sections do not each need their own reader.
func itemsUnder(n *node, prefix string) []*node {
	var out []*node
	var walk func(*node)
	walk = func(cur *node) {
		for _, c := range cur.children {
			if c.kind == kindItem && c.prefix == prefix {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
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
