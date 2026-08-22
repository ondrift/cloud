package slice

import "testing"

// The two editing keys, driven on a BACKBONE list rather than on a function.
//
// Both were built and exercised on the function list only, and the lists are
// not the same shape: a function's row carries its route and opens onto a
// method and a memory box, while a collection's row is the whole item. A key
// that reads "the row you are on" behaves differently on the two, so proving it
// on one proves nothing about the other.

// expandAll opens every row, so a test can reach a node without walking the
// tree the way a finger would.
func expandAll(ns []*node) {
	for _, n := range ns {
		n.expanded = true
		expandAll(n.children)
	}
}

// findNode returns the first node matching pred, depth first.
func findNode(ns []*node, pred func(*node) bool) *node {
	for _, n := range ns {
		if pred(n) {
			return n
		}
		if got := findNode(n.children, pred); got != nil {
			return got
		}
	}
	return nil
}

// countItems counts the items of one list, by the prefix its adder carries.
func countListItems(ns []*node, prefix string) int {
	total := 0
	for _, n := range ns {
		if n.kind == kindItem && n.prefix == prefix {
			total++
		}
		total += countListItems(n.children, prefix)
	}
	return total
}

// settle rebuilds the visible rows and keeps the cursor inside them, which is
// what the draw loop does between keystrokes.
func settle(f *shapeForm) {
	f.visible()
	if f.cursor >= len(f.flat) {
		f.cursor = len(f.flat) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
}

// put places the cursor on a node, failing if it is not on screen.
func put(t *testing.T, f *shapeForm, target *node) {
	t.Helper()
	settle(f)
	for i, r := range f.flat {
		if r.n == target {
			f.cursor = i
			return
		}
	}
	t.Fatalf("node %q is not visible", target.label)
}

// adderFor finds the "+ Add …" row of one list.
func adderFor(t *testing.T, f *shapeForm, prefix string) *node {
	t.Helper()
	add := findNode(f.root, func(n *node) bool { return n.kind == kindAdd && n.prefix == prefix })
	if add == nil {
		t.Fatalf("no adder with prefix %q", prefix)
	}
	return add
}

// addNamed adds one item to a list and names it, leaving the cursor on it.
func addNamed(t *testing.T, f *shapeForm, prefix, name string) *node {
	t.Helper()
	put(t, f, adderFor(t, f, prefix))
	f.handle(fkSpace, 0) // the adder's Space makes one and opens it for typing
	settle(f)
	item := f.flat[f.cursor].n
	if item.kind != kindItem {
		t.Fatalf("adding left the cursor on a %v, not the new item", item.kind)
	}
	for _, ch := range name {
		f.handle(fkChar, ch)
	}
	f.handle(fkEnter, 0)
	settle(f)
	return item
}

// ^D removes a Backbone item, the same as it removes a function.
func TestRemoveKeyDropsABackboneItem(t *testing.T) {
	for _, list := range []struct{ prefix, name string }{
		{"Collection ", "events"},
		{"Database ", "ledger"},
		{"Bucket ", "uploads"},
		{"Queue ", "emails"},
	} {
		f := newShapeForm("t")
		expandAll(f.root)

		item := addNamed(t, f, list.prefix, list.name)
		if got := countListItems(f.root, list.prefix); got != 1 {
			t.Fatalf("%s: adding one left %d", list.prefix, got)
		}
		if item.value != list.name {
			t.Fatalf("%s: the item is named %q, want %q", list.prefix, item.value, list.name)
		}

		put(t, f, item)
		f.handle(fkRemove, 0)
		settle(f)

		if got := countListItems(f.root, list.prefix); got != 0 {
			t.Errorf("%s: ^D left %d behind — the row is not removable off the function list",
				list.prefix, got)
		}
	}
}

// Esc on an item added a keystroke ago and never named removes it.
//
// Leaving it behind is worse than it looks: a nameless row refuses the whole
// form at submit, over a decision the tenant already reversed.
func TestCancelDropsAHalfTypedBackboneItem(t *testing.T) {
	for _, prefix := range []string{"Collection ", "Database ", "Bucket ", "Queue "} {
		f := newShapeForm("t")
		expandAll(f.root)

		put(t, f, adderFor(t, f, prefix))
		f.handle(fkSpace, 0)
		settle(f)
		if got := countListItems(f.root, prefix); got != 1 {
			t.Fatalf("%s: adding one left %d", prefix, got)
		}

		// Half-typed: a couple of characters, then thought better of.
		f.handle(fkChar, 'e')
		f.handle(fkBackspace, 0)
		f.handle(fkCancel, 0)
		settle(f)

		if got := countListItems(f.root, prefix); got != 0 {
			t.Errorf("%s: Esc left %d nameless row(s), which refuse the form at submit", prefix, got)
		}
	}
}

// Esc on an item that already HAS a name keeps it and restores what it was.
//
// The control for the two above: it shows the removal is scoped to a row that
// was never named, rather than Esc deleting whatever it lands on.
func TestCancelKeepsANamedBackboneItemAndRestoresIt(t *testing.T) {
	f := newShapeForm("t")
	expandAll(f.root)

	item := addNamed(t, f, "Collection ", "events")

	// Type over it, then change your mind.
	put(t, f, item)
	f.handle(fkChar, 'x')
	f.handle(fkCancel, 0)
	settle(f)

	if got := countListItems(f.root, "Collection "); got != 1 {
		t.Fatalf("Esc removed a named collection; %d left", got)
	}
	if item.value != "events" {
		t.Errorf("the collection is now named %q, want the %q it was before typing", item.value, "events")
	}
}
