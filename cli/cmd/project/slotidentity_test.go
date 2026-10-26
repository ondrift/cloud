package project

import (
	"strings"
	"testing"
)

// THE canvas test. Two sites written entirely with their mount paths must
// produce two DISTINCT slugs.
//
// A reader that missed the mount path would read "" for both, and an empty mount
// path resolving to the root would give every site the slug "default" — the
// second overwriting the first, and the prune then given a keep-list naming only
// "default", removing every other site directory on the slice. One root site is
// silently correct; two is silently destructive.
func TestSlotIdentity_TwoPathSitesKeepDistinctSlugs(t *testing.T) {
	m := parseSlots(t, canvasDriftfile("/", "/admin"), "site-a", "site-b")

	sites, err := canvasSites(m)
	if err != nil {
		t.Fatalf("two sites on distinct paths must resolve: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("got %d sites, want 2", len(sites))
	}
	if sites[0].Slug == sites[1].Slug {
		t.Fatalf("both sites collapsed to the slug %q — the second overwrites the first "+
			"and the prune deletes every other site on the slice", sites[0].Slug)
	}
	if sites[0].Slug != "default" || sites[1].Slug != "admin" {
		t.Errorf("slugs = %q, %q; want default, admin", sites[0].Slug, sites[1].Slug)
	}
}

// A collection entry names the slot it seeds.
func TestSlotIdentity_ANosqlEntryNamesItsSlot(t *testing.T) {
	m := parseSlots(t, "slice \"demo\" {}\nbackbone {\n  nosql \"ops\" { size = \"5MiB\" }\n}\n")

	entries := m.Slice().Entries("slot", "backbone", "nosql")
	if len(entries) != 1 || entries[0].Str("slot") != "ops" {
		t.Fatalf("slot not resolved: %+v", entries)
	}
}

// Every collection resolves onto its slot in the order the file declares them,
// and a ttl, which is a BEHAVIOUR rather than a capacity purchase, reaches the
// entry. Losing it turns a TTL'd collection into kept-forever with nothing
// reporting it.
func TestSlotIdentity_ANosqlEntryKeepsItsTTLAndItsOrder(t *testing.T) {
	m := parseSlots(t, "slice \"demo\" {}\n"+
		"backbone {\n  nosql \"ops\" {\n    size = \"5MiB\"\n    ttl  = \"30d\"\n  }\n"+
		"  nosql \"events\" { size = \"5MiB\" }\n}\n")

	entries := m.Slice().Entries("slot", "backbone", "nosql")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Str("slot") != "ops" || entries[1].Str("slot") != "events" {
		t.Errorf("the entries did not resolve onto their slots in order: %+v", entries)
	}
	if entries[0].Str("ttl") != "30d" {
		t.Errorf("ttl = %q, want 30d", entries[0].Str("ttl"))
	}
}

// A slot on its own is a complete entry. A manifest legitimately references a
// collection it neither seeds nor expires, and demanding either would make the
// reference form unwriteable.
func TestSlotIdentity_ASlotAloneIsAValidEntry(t *testing.T) {
	if _, err := tryParseSlots(t, "slice \"demo\" {}\nbackbone {\n  nosql \"ops\" { size = \"5MiB\" }\n}\n"); err != nil {
		t.Fatalf("a nosql block with neither seed nor ttl must parse: %v", err)
	}
}

// canvasDriftfile writes one site per mount path, each serving the next
// ./site-<letter> folder.
func canvasDriftfile(paths ...string) string {
	body := "slice \"demo\" {}\ncanvas {\n"
	for i, p := range paths {
		body += "  site \"" + p + "\" { dir = \"site-" + string(rune('a'+i)) + "\" }\n"
	}
	return body + "}\n"
}

func parseSlots(t *testing.T, body string, dirs ...string) *Manifest {
	t.Helper()
	m, err := tryParseSlots(t, body, dirs...)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}

// tryParseSlots parses body as a Driftfile with each of dirs made beside it.
func tryParseSlots(t *testing.T, body string, dirs ...string) (*Manifest, error) {
	t.Helper()
	withRealSchema(t)
	var paths []string
	for _, d := range dirs {
		paths = append(paths, strings.TrimSuffix(d, "/")+"/")
	}
	return ParseDriftfile(writeDriftfile(t, body, paths...))
}
