package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// THE canvas test. Two sites written entirely with `path:` must produce two
// DISTINCT slugs.
//
// A reader that missed the rename would read "" for both, and an empty mount
// path resolving to the root would give every site the slug "default" — the
// second overwriting the first, and the prune then given a keep-list naming only
// "default", removing every other site directory on the slice. One root site is
// silently correct; two is silently destructive.
func TestSlotIdentity_TwoPathSitesKeepDistinctSlugs(t *testing.T) {
	m := parseSlots(t, canvasDriftfile("path", "/", "/admin"), "site-a", "site-b")

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

// The retired spelling produces identical output, and says so once.
func TestSlotIdentity_TheRetiredRouteKeyResolvesIdenticallyAndWarnsOnce(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	m := parseSlots(t, canvasDriftfile("route", "/", "/admin"), "site-a", "site-b")
	sites, err := canvasSites(m)
	if err != nil {
		t.Fatalf("the retired spelling must keep resolving: %v", err)
	}
	if len(sites) != 2 || sites[0].Slug != "default" || sites[1].Slug != "admin" {
		t.Fatalf("the retired spelling resolved differently: %+v", sites)
	}
	if n := strings.Count(notices.String(), "canvas.sites[].route"); n != 1 {
		t.Errorf("the retired key was announced %d times across 2 entries, want 1:\n%s",
			n, notices.String())
	}
}

// A bare-string entry carries no mount key at all, so it is the root — and it is
// not deprecated, so it must say nothing.
func TestSlotIdentity_ABareStringSiteIsTheRootAndIsSilent(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	m := parseSlots(t, "slice: demo\ncanvas:\n  sites:\n    - ./site-a\n", "site-a")
	sites, err := canvasSites(m)
	if err != nil {
		t.Fatalf("a bare-string site must resolve: %v", err)
	}
	if len(sites) != 1 || sites[0].Slug != "default" {
		t.Fatalf("bare string resolved to %+v, want one site at slug default", sites)
	}
	if strings.Contains(notices.String(), "canvas.sites") {
		t.Errorf("a bare-string entry declares no retired key and must be silent:\n%s",
			notices.String())
	}
}

// A collection entry names the slot it seeds.
func TestSlotIdentity_ANosqlEntryNamesItsSlot(t *testing.T) {
	m := parseSlots(t, "slice: demo\nbackbone:\n  nosql:\n    - slot: ops\n")

	entries := m.Slice().Entries("slot", "backbone", "nosql")
	if len(entries) != 1 || entries[0].Str("slot") != "ops" {
		t.Fatalf("slot not resolved: %+v", entries)
	}
}

// The retired nosql spelling still resolves — with its size, which stays
// required in that branch — and warns once.
func TestSlotIdentity_TheRetiredNosqlKeyResolvesAndWarnsOnce(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	m := parseSlots(t, "slice: demo\nbackbone:\n  nosql:\n"+
		"    - name: ops\n      size: 5MB\n      ttl: 30d\n"+
		"    - name: events\n      size: 5MB\n")

	entries := m.Slice().Entries("slot", "backbone", "nosql")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Str("slot") != "ops" || entries[1].Str("slot") != "events" {
		t.Errorf("the retired key did not resolve onto slot: %+v", entries)
	}
	// ttl is a BEHAVIOUR, not a capacity purchase, and survives the rename. Losing
	// it turns a TTL'd collection into kept-forever with nothing reporting it.
	if entries[0].Str("ttl") != "30d" {
		t.Errorf("ttl = %q, want 30d — it must survive the rename", entries[0].Str("ttl"))
	}
	if n := strings.Count(notices.String(), "backbone.nosql[].name"); n != 1 {
		t.Errorf("the retired key was announced %d times across 2 entries, want 1:\n%s",
			n, notices.String())
	}
}

// A slot on its own is a complete entry. A manifest legitimately references a
// collection it neither seeds nor expires, and demanding either would make the
// reference form unwriteable.
func TestSlotIdentity_ASlotAloneIsAValidEntry(t *testing.T) {
	if _, err := tryParseSlots(t, "slice: demo\nbackbone:\n  nosql:\n    - slot: ops\n"); err != nil {
		t.Fatalf("`{slot: ops}` with neither seed nor ttl must parse: %v", err)
	}
}

// canvasDriftfile writes one site per mount path, under the given key.
func canvasDriftfile(key string, paths ...string) string {
	var b strings.Builder
	b.WriteString("slice: demo\ncanvas:\n  sites:\n")
	for i, p := range paths {
		b.WriteString("    - dir: ./site-" + string(rune('a'+i)) + "\n")
		b.WriteString("      " + key + ": \"" + p + "\"\n")
	}
	return b.String()
}

func parseSlots(t *testing.T, body string, dirs ...string) *Manifest {
	t.Helper()
	m, err := tryParseSlots(t, body, dirs...)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}

func tryParseSlots(t *testing.T, body string, dirs ...string) (*Manifest, error) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return ParseDriftfile(path)
}
