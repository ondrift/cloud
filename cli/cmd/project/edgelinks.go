// edgelinks.go — `edge.links`, the slices of the same owner this slice may
// call or front.
//
// Present, the key is the whole truth: apply adds every declared link the
// slice does not hold and removes every live link the file does not name, and
// `--plan` shows both. Absent, the live links are left exactly as they are.
// An empty list is a declaration — no links — and removes them all.
//
// The calls are `drift slice link`'s, exported from cmd/slice, so the command
// and the Driftfile cannot speak two dialects of one route. Whether a target
// is a slice of the same owner is the platform's to decide; its refusal is
// surfaced as it arrives.
//
// deploy.go runs this before `edge.hosts`, because a fronted host names a
// slice this one must already link.
package project

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// declaredLinks is `edge.links` in the platform's spelling (lowercased,
// trimmed), each name once, in the order the file gives them.
func declaredLinks(m *Manifest) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range m.Slice().Strings("edge", "links") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// linkChanges splits the declared and live sets into what an apply adds, what
// it removes, and what it leaves.
func linkChanges(declared []string, live []slice.Link) (add, remove, keep []string) {
	liveSet := map[string]bool{}
	for _, l := range live {
		liveSet[strings.ToLower(strings.TrimSpace(l.Target))] = true
	}
	declaredSet := map[string]bool{}
	for _, d := range declared {
		declaredSet[d] = true
		if liveSet[d] {
			keep = append(keep, d)
		} else {
			add = append(add, d)
		}
	}
	for name := range liveSet {
		if !declaredSet[name] {
			remove = append(remove, name)
		}
	}
	sort.Strings(remove)
	return add, remove, keep
}

// preflightEdgeLinks runs before the first deploy step. The one refusal it can
// make without the platform is a link to the slice itself.
func preflightEdgeLinks(m *Manifest) error {
	if !m.Slice().Has("edge", "links") {
		return nil
	}
	for _, name := range declaredLinks(m) {
		if name == strings.ToLower(m.Name()) {
			return fmt.Errorf("edge.links names %q, the slice this file is applied to; a slice cannot link to itself", name)
		}
	}
	return nil
}

// applyEdgeLinks reconciles `edge.links`. Absent, it does nothing, and the
// live links are left alone.
//
// Adds come before removals, so a link the file keeps is never down between
// the two, and the first refusal stops the run with the platform's reason.
func applyEdgeLinks(m *Manifest) error {
	if !m.Slice().Has("edge", "links") {
		return nil
	}
	live, err := slice.ListLinks(m.Name())
	if err != nil {
		return fmt.Errorf("edge.links: %w", err)
	}
	add, remove, _ := linkChanges(declaredLinks(m), live)
	for _, name := range add {
		if _, err := slice.AddLink(m.Name(), name); err != nil {
			return fmt.Errorf("edge.links: %w", err)
		}
		fmt.Printf("  %s linked → %s\n", common.Check(), name)
	}
	for _, name := range remove {
		if err := slice.RemoveLink(m.Name(), name); err != nil {
			return fmt.Errorf("edge.links: %w", err)
		}
		fmt.Printf("  %s unlinked → %s %s\n", common.Check(), name, common.Hint("(not in edge.links)"))
	}
	return nil
}

// planEdgeLinks is the `--plan` section for `edge.links`: what would be added,
// removed and kept. It reads the live links and writes nothing.
func planEdgeLinks(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "links") {
		return
	}
	declared := declaredLinks(m)
	fmt.Fprintf(out, "\n  links (edge.links)\n")
	live, err := slice.ListLinks(m.Name())
	if err != nil {
		fmt.Fprintf(out, "    %s couldn't read the live links, so what would be removed is unknown: %v\n", common.Hint("!"), err)
		for _, name := range declared {
			fmt.Fprintf(out, "    %s %s\n", common.Hint("·"), name)
		}
		return
	}
	add, remove, keep := linkChanges(declared, live)
	if len(add) == 0 && len(remove) == 0 {
		fmt.Fprintf(out, "    %s\n", common.Hint("no change"))
	}
	for _, name := range add {
		fmt.Fprintf(out, "    + %s\n", name)
	}
	for _, name := range remove {
		fmt.Fprintf(out, "    - %s %s\n", name, common.Hint("(live, not in edge.links: would be removed)"))
	}
	for _, name := range keep {
		fmt.Fprintf(out, "    · %s\n", name)
	}
}
