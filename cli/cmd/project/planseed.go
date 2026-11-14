package project

// planseed.go: what `drift plan` says of the data a file seeds.
//
// # A NoSQL seed
//
// Size is the one thing the plan compares for a collection, and the seed is the
// other: apply writes every seed document by its _id, so the plan says which of
// them are missing from the slice or hold other values there, which is the
// writes apply would make. The seed is read by the one reader apply uses
// (readNoSQLSeed), so what is compared is what is written.
//
// The slice's documents are read through backbone.ReadNoSQLCollection, a GET, for
// at most seedCompareMax documents of a collection. A collection that holds more
// is not compared and is unread: a plan that read part of it cannot say that the
// rest agrees.
//
// # A cache entry
//
// Apply sets every declared entry, so the plan says whether the slice holds the
// value apply would set: present, differs or missing. The value is read through
// backbone.ReadCacheValue, a GET, compared and never printed.
//
// # A SQL seed
//
// The slice runs it only while the database holds no data, and nothing reads
// that. The plan says so and compares nothing.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/backbone"
)

// seedCompareMax is how many documents of a collection the plan reads to compare
// its seed: two pages, the most one request returns being 1000.
const seedCompareMax = 2000

// errSeedTooLarge is why a collection's seed is not compared when it holds more
// than seedCompareMax documents.
var errSeedTooLarge = fmt.Errorf("the collection holds more than %d documents", seedCompareMax)

// seedIDsNamed is how many _ids a seed line names before it counts the rest.
const seedIDsNamed = 5

// seedFieldsOfTheTransport are the fields of a stored document that are not the
// seed's to name: the platform gives each document its own _key, and `collection`
// is the route a write is sent through. Neither is a difference.
var seedFieldsOfTheTransport = map[string]bool{"_key": true, "collection": true}

// liveCollection is what the plan read of a collection that has a seed: its
// documents, whether it holds more than were read, and why it could not be read.
type liveCollection struct {
	docs []json.RawMessage
	more bool
	err  error
}

// fetchSeededCollections reads the documents of every collection the file seeds.
func fetchSeededCollections(m *Manifest) map[string]liveCollection {
	out := map[string]liveCollection{}
	for _, c := range m.Slice().Entries("slot", "backbone", "nosql") {
		if c.Str("seed") == "" {
			continue
		}
		docs, more, err := backbone.ReadNoSQLCollection(c.Str("slot"), seedCompareMax)
		out[c.Str("slot")] = liveCollection{docs: docs, more: more, err: err}
	}
	return out
}

// diffNoSQLSeed adds the line that says what apply would write of the seed of a
// collection the file declares, beside the collection's size line.
func (p *planState) diffNoSQLSeed(d *planDiff, name string, creating bool) {
	var seed string
	for _, c := range p.c.Manifest.Slice().Entries("slot", "backbone", "nosql") {
		if c.Str("slot") == name {
			seed = c.Str("seed")
		}
	}
	if seed == "" {
		return
	}
	addr := "nosql " + name

	docs, errs := readNoSQLSeed(name, p.c.Manifest.ResolvePath(seed))
	if len(errs) > 0 {
		d.notes = append(d.notes, errs.Error())
		return
	}

	// A slice that does not exist yet holds nothing to read.
	var live liveCollection
	if !creating {
		live = p.collections[name]
		switch {
		case live.err != nil:
			d.unread(addr, "seed", live.err)
			return
		case live.more:
			d.add(" ", addr, "seed not compared: the collection holds more than %d documents", seedCompareMax)
			d.noteUnread(addr, "seed", errSeedTooLarge)
			return
		}
	}

	total, missing, differs := compareSeed(docs, live.docs)
	mark, detail := seedLine(total, missing, differs)
	d.add(mark, addr, "%s", detail)
}

// sqlSeedLine is what the plan says of a SQL database's seed. The slice decides
// whether it runs, by whether the database holds data, and no route says that.
const sqlSeedLine = "seed: runs only while the database holds no data; the plan cannot read whether it does"

// diffSQLSeed adds the line that says what a SQL database's seed does, beside the
// database's size line. It is no difference and no unread fact: apply sends the
// seed on every apply and the slice runs it or not.
func (p *planState) diffSQLSeed(d *planDiff, name string) {
	for _, e := range p.c.Manifest.Slice().Entries("name", "backbone", "sql") {
		if strings.EqualFold(strings.TrimSpace(e.Str("name")), name) && e.Str("seed") != "" {
			d.add(" ", "sql "+name, "%s", sqlSeedLine)
			return
		}
	}
}

// liveCacheEntry is what the plan read of a declared cache entry.
type liveCacheEntry struct {
	value string
	found bool
	err   error
}

// fetchDeclaredCache reads each cache entry the file declares.
func fetchDeclaredCache(m *Manifest) map[string]liveCacheEntry {
	out := map[string]liveCacheEntry{}
	for key := range m.Slice().EntryMap("file", "backbone", "cache") {
		value, found, err := backbone.ReadCacheValue(key)
		out[key] = liveCacheEntry{value: value, found: found, err: err}
	}
	return out
}

// diffCache adds one line for each cache entry the file declares: present, differs
// or missing. Apply sets every declared entry on every apply, so the line says
// whether the slice holds what apply would set. The value is never printed.
func (p *planState) diffCache(d *planDiff, creating bool) {
	entries := p.c.Manifest.Slice().EntryMap("file", "backbone", "cache")
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		addr := "cache " + key
		want, _, err := declaredCacheValue(p.c.Manifest, key, entries[key])
		if err != nil {
			d.notes = append(d.notes, err.Error())
			continue
		}
		live := p.cache[key]
		switch {
		case creating || (live.err == nil && !live.found):
			d.add("+", addr, "missing")
		case live.err != nil:
			d.unread(addr, "cache entry", live.err)
		case live.value != asStored(want):
			d.add("~", addr, "differs: apply sets it again")
		default:
			d.add(" ", addr, "present")
		}
	}
}

// asStored is a cache value as the slice holds it once apply has set it: sent as
// a JSON string, which replaces a byte that is not UTF-8 with U+FFFD.
func asStored(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var stored string
	if json.Unmarshal(raw, &stored) != nil {
		return value
	}
	return stored
}

// compareSeed says, for each distinct _id the seed writes, whether the collection
// lacks the document or holds it with another value in a field the seed names. A
// field the seed does not name, such as one the app added, is no difference. A
// record whose _id repeats in the file is the last of them, which is what the
// collection holds once apply has written them all, and an _id the collection
// holds twice differs, because apply leaves one.
func compareSeed(seed []seedDoc, live []json.RawMessage) (total int, missing, differs []string) {
	held := map[string][]map[string]any{}
	for _, raw := range live {
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		if id, ok := doc["_id"].(string); ok {
			held[id] = append(held[id], doc)
		}
	}

	last := map[string]seedDoc{}
	var order []string
	for _, s := range seed {
		if _, seen := last[s.ID]; !seen {
			order = append(order, s.ID)
		}
		last[s.ID] = s
	}

	for _, id := range order {
		switch docs := held[id]; {
		case len(docs) == 0:
			missing = append(missing, id)
		case len(docs) > 1 || !holdsSeed(docs[0], last[id].Doc):
			differs = append(differs, id)
		}
	}
	return len(order), missing, differs
}

// holdsSeed reports whether a stored document has every field the seed names
// with the value the seed gives it.
func holdsSeed(stored, seed map[string]any) bool {
	for k, want := range seed {
		if seedFieldsOfTheTransport[k] {
			continue
		}
		if got, ok := stored[k]; !ok || !reflect.DeepEqual(got, want) {
			return false
		}
	}
	return true
}

// seedLine is the plan's line for a seed of total documents of which missing are
// not in the collection and differs hold other values there, with its mark: +
// when apply writes every document, ~ when it writes some, none when it writes
// nothing the slice does not already hold.
func seedLine(total int, missing, differs []string) (mark, detail string) {
	writes := len(missing) + len(differs)
	if writes == 0 {
		return " ", fmt.Sprintf("seed: %d %s, all present", total, plural(total, "document", "documents"))
	}
	mark = "~"
	if len(missing) == total {
		mark = "+"
	}
	var counts []string
	if len(missing) > 0 {
		counts = append(counts, fmt.Sprintf("%d missing", len(missing)))
	}
	if len(differs) > 0 {
		counts = append(counts, fmt.Sprintf("%d differs", len(differs)))
	}
	return mark, fmt.Sprintf("seed: %s: apply writes %d of %d (%s)",
		strings.Join(counts, ", "), writes, total, seedIDsOf(missing, differs))
}

// seedIDsOf names the first seedIDsNamed _ids that apply would write, the missing
// ones first, and counts the rest.
func seedIDsOf(missing, differs []string) string {
	budget := seedIDsNamed
	var groups []string
	for _, g := range []struct {
		label string
		ids   []string
	}{{"missing", missing}, {"differs", differs}} {
		n := min(len(g.ids), budget)
		if n == 0 {
			continue
		}
		groups = append(groups, g.label+" "+strings.Join(g.ids[:n], ", "))
		budget -= n
	}
	out := strings.Join(groups, "; ")
	if rest := len(missing) + len(differs) - seedIDsNamed; rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return out
}
