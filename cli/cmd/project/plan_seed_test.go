package project

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// seedLineOf is the plan's line for a collection that says what its seed would
// do: the one whose detail begins "seed", with its mark. The collection's size
// line carries the same address.
func seedLineOf(t *testing.T, out, address string) planLine {
	t.Helper()
	for _, l := range planLinesFor(out, address) {
		if strings.HasPrefix(l.detail, "seed") {
			return l
		}
	}
	t.Fatalf("the plan has no seed line for %s:\n%s", address, out)
	return planLine{}
}

// menuLive writes the documents of menuSeed the way the platform holds them: with
// the _key it added to each, and the collection the CLI wrote beside it.
func menuLive(s *sliceStore, only ...string) {
	for _, line := range strings.Split(strings.TrimSpace(menuSeed), "\n") {
		doc := map[string]any{}
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			panic(err)
		}
		if len(only) > 0 && !slices.Contains(only, doc["_id"].(string)) {
			continue
		}
		doc["collection"] = "menu"
		s.put("menu", doc)
	}
}

// compareSeed asks one thing of each seed document: does the collection hold a
// document with that _id whose fields, the ones the seed names, have the seed's
// values. What else the document holds is not asked.
func TestCompareSeed_ComparesTheFieldsTheSeedNames(t *testing.T) {
	seedOf := func(lines ...string) []seedDoc {
		var docs []seedDoc
		for _, l := range lines {
			doc := map[string]any{}
			if err := json.Unmarshal([]byte(l), &doc); err != nil {
				t.Fatal(err)
			}
			docs = append(docs, seedDoc{ID: doc["_id"].(string), Doc: doc})
		}
		return docs
	}
	liveOf := func(docs ...string) []json.RawMessage {
		var raw []json.RawMessage
		for _, d := range docs {
			raw = append(raw, json.RawMessage(d))
		}
		return raw
	}

	for _, c := range []struct {
		name    string
		seed    []seedDoc
		live    []json.RawMessage
		total   int
		missing []string
		differs []string
	}{
		{"what the platform and the app added is no difference",
			seedOf(`{"_id":"a","n":1}`),
			liveOf(`{"_id":"a","n":1,"_key":"7_9","collection":"menu","note":"x"}`), 1, nil, nil},
		{"a number is not its string",
			seedOf(`{"_id":"a","n":9}`), liveOf(`{"_id":"a","n":"9"}`), 1, nil, []string{"a"}},
		{"a field the slice lacks",
			seedOf(`{"_id":"a","n":9,"m":1}`), liveOf(`{"_id":"a","n":9}`), 1, nil, []string{"a"}},
		{"a nested value that differs",
			seedOf(`{"_id":"a","tags":["x","y"],"o":{"k":1}}`),
			liveOf(`{"_id":"a","tags":["x","y"],"o":{"k":2}}`), 1, nil, []string{"a"}},
		{"a nested value that agrees",
			seedOf(`{"_id":"a","tags":["x","y"],"o":{"k":1}}`),
			liveOf(`{"_id":"a","o":{"k":1},"tags":["x","y"]}`), 1, nil, nil},
		{"the same number written another way",
			seedOf(`{"_id":"a","n":1e3}`), liveOf(`{"_id":"a","n":1000.0}`), 1, nil, nil},
		{"a field the seed names as _key is the platform's",
			seedOf(`{"_id":"a","_key":"mine"}`), liveOf(`{"_id":"a","_key":"7_9"}`), 1, nil, nil},
		{"a repeated _id is the last of them",
			seedOf(`{"_id":"a","n":1}`, `{"_id":"b"}`, `{"_id":"a","n":2}`),
			liveOf(`{"_id":"a","n":2}`), 2, []string{"b"}, nil},
		{"an _id the collection holds twice differs, because apply leaves one",
			seedOf(`{"_id":"a","n":1}`), liveOf(`{"_id":"a","n":1}`, `{"_id":"a","n":1}`), 1, nil, []string{"a"}},
		{"a document with no string _id is nobody's",
			seedOf(`{"_id":"a"}`), liveOf(`{"_id":7}`, `{"n":1}`, `[1]`), 1, []string{"a"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			total, missing, differs := compareSeed(c.seed, c.live)
			if total != c.total || !slices.Equal(missing, c.missing) || !slices.Equal(differs, c.differs) {
				t.Errorf("compareSeed is %d documents, missing %v, differs %v, want %d, %v, %v",
					total, missing, differs, c.total, c.missing, c.differs)
			}
		})
	}
}

// A seed of one document says so in the singular.
func TestSeedLine_SaysOneDocumentInTheSingular(t *testing.T) {
	if mark, detail := seedLine(1, nil, nil); mark != " " || detail != "seed: 1 document, all present" {
		t.Errorf("the line is %q with the mark %q", detail, mark)
	}
}

// A collection whose seed documents are all there says so, and what the app
// added to it, or the platform added to a document, is no difference. The
// slice is then the file.
func TestPlan_ASeededCollectionWithEverySeedDocumentSaysAllPresent(t *testing.T) {
	store, _ := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store)
	// What the app wrote: a document of its own, and a field on a seeded one.
	store.put("menu", map[string]any{"_id": "special", "price": 99, "collection": "menu"})
	store.mu.Lock()
	store.docs["menu"][0]["soldOut"] = true
	store.mu.Unlock()

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if err != nil {
		t.Fatalf("a plan of a slice that holds its seed exits %d (%v):\n%s", exitStatusOf(err), err, out)
	}
	got := seedLineOf(t, out, "nosql menu")
	if got.detail != "seed: 5 documents, all present" || got.mark != " " {
		t.Errorf("the seed line is %q with the mark %q, want %q with none:\n%s", got.detail, got.mark, "seed: 5 documents, all present", out)
	}
	if !strings.Contains(out, closingMatch) {
		t.Errorf("nothing differs and the plan does not end on %q:\n%s", closingMatch, out)
	}
}

// The line says how many documents apply would write, and which: the missing
// ones and the ones whose seeded fields hold other values, in the order the
// file has them.
func TestPlan_SaysWhichSeedDocumentsApplyWouldWrite(t *testing.T) {
	store, _ := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store, "pizza", "pasta", "cake")
	// pasta's price is not the seed's.
	store.mu.Lock()
	for _, d := range store.docs["menu"] {
		if d["_id"] == "pasta" {
			d["price"] = float64(12)
		}
	}
	store.mu.Unlock()

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if got := exitStatusOf(err); got != 2 {
		t.Errorf("a plan whose seed has documents to write exits %d with --exit-code, want 2 (%v)", got, err)
	}
	got := seedLineOf(t, out, "nosql menu")
	want := "seed: 2 missing, 1 differs: apply writes 3 of 5 (missing soup, salad; differs pasta)"
	if got.detail != want || got.mark != "~" {
		t.Errorf("the seed line is %q with the mark %q, want %q with ~:\n%s", got.detail, got.mark, want, out)
	}
	if strings.Contains(out, closingMatch) {
		t.Errorf("a plan with seed documents to write ends on %q:\n%s", closingMatch, out)
	}
}

// A collection the app has not written to yet is all missing, and the line is an
// addition. A line names five _ids and counts the rest.
func TestPlan_NamesAtMostFiveSeedIDs(t *testing.T) {
	var seed strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&seed, "{\"_id\":\"d%d\",\"n\":%d}\n", i, i)
	}
	seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": seed.String()})

	out, _ := planOutput(t, "--no-env-file")
	got := seedLineOf(t, out, "nosql menu")
	want := "seed: 8 missing: apply writes 8 of 8 (missing d1, d2, d3, d4, d5 and 3 more)"
	if got.detail != want || got.mark != "+" {
		t.Errorf("the seed line is %q with the mark %q, want %q with +:\n%s", got.detail, got.mark, want, out)
	}
}

// A slice that does not exist yet holds nothing to read: the plan says apply
// writes the whole seed, and asks the slice for no document.
func TestPlan_ASliceThatDoesNotExistYetWritesTheWholeSeed(t *testing.T) {
	store, rec := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	store.live = ""

	out, _ := planOutput(t, "--no-env-file")
	got := seedLineOf(t, out, "nosql menu")
	if want := "seed: 5 missing: apply writes 5 of 5 (missing pizza, pasta, soup, salad, cake)"; got.detail != want || got.mark != "+" {
		t.Errorf("the seed line is %q with the mark %q, want %q with +:\n%s", got.detail, got.mark, want, out)
	}
	if n := rec.count("GET /ops/backbone/nosql/list"); n != 0 {
		t.Errorf("the plan of a slice that does not exist listed its collection %d time(s)", n)
	}
}

// A collection is read two pages deep and no further. One that is larger says
// that it was not compared, and a plan that did not compare it is not a match.
func TestPlan_ACollectionLargerThanTwoPagesIsNotComparedAndIsUnread(t *testing.T) {
	store, _ := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store)
	for i := 0; i < 2500; i++ {
		store.put("menu", map[string]any{"_id": fmt.Sprintf("app-%d", i), "collection": "menu"})
	}

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if got := exitStatusOf(err); got != 3 {
		t.Errorf("a plan that did not compare a seed exits %d with --exit-code, want 3 (%v)", got, err)
	}
	got := seedLineOf(t, out, "nosql menu")
	if want := "seed not compared: the collection holds more than 2000 documents"; got.detail != want || got.mark != " " {
		t.Errorf("the seed line is %q with the mark %q, want %q with none:\n%s", got.detail, got.mark, want, out)
	}
	if strings.Contains(out, "all present") || strings.Contains(out, closingMatch) {
		t.Errorf("a collection the plan did not compare is called present or a match:\n%s", out)
	}
	if !strings.Contains(out, closingUnread) || !strings.Contains(out, "nosql menu: seed could not be read") {
		t.Errorf("the plan does not end on what it could not read:\n%s", out)
	}
	limits := store.listLimits()
	if len(limits) > 3 {
		t.Errorf("the plan sent %d list requests for one collection, want two pages and at most one more document to tell it is larger: %v", len(limits), limits)
	}
	read := 0
	for _, l := range limits {
		read += l
	}
	if read > 2001 {
		t.Errorf("the plan asked for %d documents of the collection, want at most 2000 and one to tell it is larger: %v", read, limits)
	}
}

// 2000 documents are two full pages and no more: the collection is compared, and
// a plan that reads two full pages does not take them for a larger collection.
func TestPlan_ACollectionOfExactlyTwoPagesIsCompared(t *testing.T) {
	store, _ := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store)
	for i := 0; i < 1995; i++ {
		store.put("menu", map[string]any{"_id": fmt.Sprintf("app-%d", i), "collection": "menu"})
	}
	if got := store.count("menu"); got != 2000 {
		t.Fatalf("the test's collection holds %d documents, want 2000", got)
	}

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if err != nil {
		t.Errorf("a plan of a collection that holds its seed exits %d (%v)", exitStatusOf(err), err)
	}
	if got := seedLineOf(t, out, "nosql menu"); got.detail != "seed: 5 documents, all present" {
		t.Errorf("the seed line is %q, want %q:\n%s", got.detail, "seed: 5 documents, all present", out)
	}
}

// A collection the slice would not list is unread, and is not called present.
func TestPlan_ACollectionTheSliceWillNotListIsUnread(t *testing.T) {
	store, _ := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store)
	store.failing["GET /ops/backbone/nosql/list"] = true

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if got := exitStatusOf(err); got != 3 {
		t.Errorf("a plan that could not list a seeded collection exits %d with --exit-code, want 3 (%v)", got, err)
	}
	if strings.Contains(out, "all present") || strings.Contains(out, closingMatch) {
		t.Errorf("a collection the plan could not list is called present or a match:\n%s", out)
	}
	if !strings.Contains(out, "nosql menu: seed could not be read") {
		t.Errorf("the plan does not say it could not read the seed:\n%s", out)
	}
}

// A plan reads and asks for a price, and nothing else, with a seed and a cache
// entry in the file; and it did read them.
func TestPlan_WithASeedAndACacheEntryIssuesNoWriteButThePriceQuote(t *testing.T) {
	store, rec := seedProject(t, `slice "demo" {}

backbone {
  nosql "menu" {
    size = "10MiB"
    seed = "seeds/menu.jsonl"
  }
  cache "greeting" { value = "hello" }
}
`, map[string]string{"seeds/menu.jsonl": menuSeed})
	menuLive(store, "pizza")

	if _, err := planOutput(t, "--no-env-file"); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, read := range []string{"GET /ops/backbone/nosql/list", "GET /ops/backbone/cache/get"} {
		if rec.count(read) == 0 {
			t.Errorf("the plan never sent %s, so it compared nothing", read)
		}
	}
	for _, hit := range rec.hits {
		if strings.HasPrefix(hit, "GET ") || hit == "POST /ops/slice/price" {
			continue
		}
		t.Errorf("A PLAN ISSUED %q. It reads, asks for a quote, and writes nothing.", hit)
	}
}

// cacheDriftfile declares three cache entries, one of them from a file.
const cacheDriftfile = `slice "demo" {}

backbone {
  cache "greeting" { value = "hello-declared" }
  cache "motd" { file = "data/motd.txt" }
  cache "token" { value = "tok-declared" }
}
`

// A declared cache entry is present, differs or is missing, and its value, the
// declared one and the live one, is never printed.
func TestPlan_ACacheEntryIsPresentDiffersOrMissing(t *testing.T) {
	store, _ := seedProject(t, cacheDriftfile, map[string]string{"data/motd.txt": "motd-from-the-file\n"})
	store.cache["greeting"] = "hello-declared"
	store.cache["motd"] = "motd-live-old"

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if got := exitStatusOf(err); got != 2 {
		t.Errorf("a plan with a cache entry to set exits %d with --exit-code, want 2 (%v)", got, err)
	}
	for _, c := range []struct{ address, mark, detail string }{
		{"cache greeting", " ", "present"},
		{"cache motd", "~", "differs: apply sets it again"},
		{"cache token", "+", "missing"},
	} {
		lines := planLinesFor(out, c.address)
		if len(lines) != 1 || lines[0].mark != c.mark || lines[0].detail != c.detail {
			t.Errorf("the plan says %v for %s, want %q with the mark %q:\n%s", lines, c.address, c.detail, c.mark, out)
		}
	}
	for _, value := range []string{"hello-declared", "motd-from-the-file", "motd-live-old", "tok-declared"} {
		if strings.Contains(out, value) {
			t.Errorf("THE PLAN PRINTED A CACHE VALUE (%q):\n%s", value, out)
		}
	}
}

// What is compared is what is written: a slice that apply has just filled, from a
// file that seeds a collection and declares cache entries, one of them a file that
// is not UTF-8, is the file, and the plan says so.
func TestPlan_ASliceApplyHasJustFilledIsTheFile(t *testing.T) {
	store, _ := seedProject(t, `slice "demo" {}

backbone {
  nosql "menu" {
    size = "10MiB"
    seed = "seeds/menu.jsonl"
  }
  cache "greeting" { value = "hello" }
  cache "blob" { file = "data/blob.bin" }
}
`, map[string]string{"seeds/menu.jsonl": menuSeed, "data/blob.bin": "\xff\xfe binary \x00 tail\n"})

	if _, err := applyOutput(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if store.count("menu") != 5 || len(store.cache) != 2 {
		t.Fatalf("the apply left %d documents and %d cache entries, want 5 and 2", store.count("menu"), len(store.cache))
	}

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if err != nil {
		t.Errorf("a plan of a slice apply just filled exits %d (%v):\n%s", exitStatusOf(err), err, out)
	}
	if !strings.Contains(out, closingMatch) {
		t.Errorf("the plan of a slice apply just filled does not end on %q:\n%s", closingMatch, out)
	}
	if got := seedLineOf(t, out, "nosql menu"); got.detail != "seed: 5 documents, all present" {
		t.Errorf("the seed line is %q, want all present", got.detail)
	}
	for _, key := range []string{"greeting", "blob"} {
		if lines := planLinesFor(out, "cache "+key); len(lines) != 1 || lines[0].detail != "present" {
			t.Errorf("the plan says %v for cache %s, want present:\n%s", lines, key, out)
		}
	}
}

// A cache entry the slice would not answer for is unread, and is not called
// present.
func TestPlan_ACacheEntryTheSliceWillNotAnswerForIsUnread(t *testing.T) {
	store, _ := seedProject(t, cacheDriftfile, map[string]string{"data/motd.txt": "motd\n"})
	store.cache["greeting"] = "hello-declared"
	store.cache["motd"] = "motd\n"
	store.cache["token"] = "tok-declared"
	store.failing["GET /ops/backbone/cache/get"] = true

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if got := exitStatusOf(err); got != 3 {
		t.Errorf("a plan that could not read a cache entry exits %d with --exit-code, want 3 (%v)", got, err)
	}
	if strings.Contains(out, "present") || strings.Contains(out, closingMatch) {
		t.Errorf("a cache entry the plan could not read is called present or a match:\n%s", out)
	}
	if !strings.Contains(out, "cache greeting, cache motd, cache token: cache entry could not be read") {
		t.Errorf("the plan does not say it could not read the cache entry:\n%s", out)
	}
}

// The SQL seed runs only while the database holds no data, and the slice decides:
// the plan cannot read whether it does, and says what the seed does instead of
// calling it present. It is no difference and no unread fact.
func TestPlan_ASQLSeedSaysItRunsOnlyWhileTheDatabaseHoldsNoData(t *testing.T) {
	seedProject(t, `slice "demo" {}

backbone {
  sql "ledger" {
    size   = "10MiB"
    schema = "schema.sql"
    seed   = "seed.sql"
  }
}
`, map[string]string{"schema.sql": "CREATE TABLE IF NOT EXISTS t (id TEXT);", "seed.sql": "INSERT INTO t VALUES ('a');"})

	out, err := planOutput(t, "--no-env-file", "--exit-code")
	if err != nil {
		t.Errorf("a plan whose only seed is SQL exits %d (%v):\n%s", exitStatusOf(err), err, out)
	}
	got := seedLineOf(t, out, "sql ledger")
	if !strings.Contains(got.detail, "runs only while the database holds no data") || got.mark != " " {
		t.Errorf("the seed line is %q with the mark %q, want it to say the seed runs only while the database holds no data, with no mark:\n%s", got.detail, got.mark, out)
	}
	if !strings.Contains(out, closingMatch) {
		t.Errorf("a SQL seed makes the plan end on something other than %q:\n%s", closingMatch, out)
	}
}

// What the help says is what the plan prints.
func TestPlan_HelpSaysWhatItPrintsForASeedAndACacheEntry(t *testing.T) {
	help := GetPlanCmd().Long
	for _, want := range []string{
		"seed: 2 missing, 1 differs: apply writes 3 of 12",
		"differs: apply sets it again",
		"runs only while the database holds no data",
		"seed not compared",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the plan's help does not say %q:\n%s", want, help)
		}
	}
}

// What apply does with a seeded collection is in its help.
func TestApply_HelpSaysASeedNeverDropsTheCollection(t *testing.T) {
	help := GetApplyCmd().Long
	for _, want := range []string{"never dropped", "by its _id"} {
		if !strings.Contains(help, want) {
			t.Errorf("apply's help does not say %q:\n%s", want, help)
		}
	}
}
