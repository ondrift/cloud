package project

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// This file holds the slice the seed tests talk to, and what apply does with a
// seeded collection. The plan's lines for a seed and a cache entry are in
// plan_seed_test.go.

// menuDriftfile declares one seeded collection and nothing else.
const menuDriftfile = `slice "demo" {}

backbone {
  nosql "menu" {
    size = "10MiB"
    seed = "seeds/menu.jsonl"
  }
}
`

// menuSeed is the seed file of menuDriftfile: five documents, each with an _id.
const menuSeed = `{"_id":"pizza","price":9}
{"_id":"pasta","price":11,"veg":true}
{"_id":"soup","price":5}
{"_id":"salad","price":6}
{"_id":"cake","price":7}
`

// sliceStore is a slice that keeps what it is given the way the platform does: a
// document written with a string _id replaces the one holding that _id, a write
// adds the document's own _key, a list walks the keys in byte order from the
// cursor it is handed, and a collection that was never written lists as empty.
type sliceStore struct {
	mu      sync.Mutex
	live    string // the GET /ops/slice body, "" for a slice that does not exist
	docs    map[string][]map[string]any
	cache   map[string]string
	counter int

	// lists is the limit of every GET /ops/backbone/nosql/list the plan or the
	// apply sent for a seeded collection, in order. The legacy-sentinel sweep that
	// ensures a collection sends one with no limit and is not in it.
	lists []int

	// failing is each "METHOD /path" the slice answers with a 500.
	failing map[string]bool
}

func newSliceStore() *sliceStore {
	return &sliceStore{docs: map[string][]map[string]any{}, cache: map[string]string{}, failing: map[string]bool{}}
}

// put stores doc the way the platform's write does.
func (s *sliceStore) put(collection string, doc map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store(collection, doc)
}

func (s *sliceStore) store(collection string, doc map[string]any) {
	if id, ok := doc["_id"].(string); ok && id != "" && len(id) <= 255 {
		kept := s.docs[collection][:0:0]
		for _, d := range s.docs[collection] {
			if d["_id"] != id {
				kept = append(kept, d)
			}
		}
		s.docs[collection] = kept
	}
	s.counter++
	stored := map[string]any{}
	for k, v := range doc {
		stored[k] = v
	}
	stored["_key"] = fmt.Sprintf("%08d_1", s.counter)
	s.docs[collection] = append(s.docs[collection], stored)
}

// has is every document the collection holds with this _id.
func (s *sliceStore) has(collection, id string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, d := range s.docs[collection] {
		if d["_id"] == id {
			out = append(out, d)
		}
	}
	return out
}

func (s *sliceStore) count(collection string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.docs[collection])
}

func (s *sliceStore) listLimits() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.lists...)
}

func (s *sliceStore) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	route := r.Method + " " + r.URL.Path
	if s.failing[route] {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	switch route {
	case "GET /ops/slice":
		if s.live == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, s.live)
	case "GET /ops/slice/status":
		_, _ = io.WriteString(w, `{"ready":true}`)
	case "GET /ops/canvas":
		_, _ = io.WriteString(w, `{"deployed":false,"sites":[]}`)
	case "GET /ops/backbone/sql/admin/list", "GET /ops/atomic", "GET /ops/slice/domain",
		"GET /ops/atomic/alert", "GET /ops/backbone/secret/list":
		_, _ = io.WriteString(w, `[]`)
	case "POST /ops/slice/price":
		_, _ = io.WriteString(w, `{"monthly_cents":0}`)
	case "GET /ops/backbone/nosql/list":
		limit := 1000
		if v := q.Get("limit"); v != "" {
			limit, _ = strconv.Atoi(v)
			s.lists = append(s.lists, limit)
		}
		limit = max(1, min(limit, 1000))
		docs := append([]map[string]any(nil), s.docs[q.Get("collection")]...)
		sort.Slice(docs, func(i, j int) bool { return docs[i]["_key"].(string) < docs[j]["_key"].(string) })
		page := []map[string]any{}
		for _, d := range docs {
			if after := q.Get("after"); after != "" && d["_key"].(string) <= after {
				continue
			}
			if len(page) == limit {
				break
			}
			page = append(page, d)
		}
		_ = json.NewEncoder(w).Encode(page)
	case "POST /ops/backbone/write":
		var doc map[string]any
		if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		collection, _ := doc["collection"].(string)
		s.store(collection, doc)
		_, _ = io.WriteString(w, "stored successfully")
	case "POST /ops/backbone/nosql/drop":
		delete(s.docs, q.Get("collection"))
		w.WriteHeader(http.StatusNoContent)
	case "POST /ops/backbone/nosql/ensure", "POST /ops/backbone/nosql/delete":
		_, _ = io.WriteString(w, `{}`)
	case "POST /ops/backbone/cache/set":
		var body struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.cache[body.Key] = body.Value
		_, _ = io.WriteString(w, "stored")
	case "GET /ops/backbone/cache/get":
		v, ok := s.cache[q.Get("key")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"key not found"}`)
			return
		}
		_, _ = io.WriteString(w, v)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

// seedProject lays out a project in a directory of its own, beside a slice that
// already holds the shape its Driftfile declares and keeps what it is given.
func seedProject(t *testing.T, driftfile string, files map[string]string) (*sliceStore, *recorder) {
	t.Helper()
	store := newSliceStore()
	rec := stubAPI(t, store.serve)
	withRealSchema(t)
	applyIn(t, driftfile)
	for path, body := range files {
		mustWrite(t, filepath.Join(".", path), body)
	}
	store.live = liveAtFileShape(t)
	return store, rec
}

// applyOutput applies the Driftfile in the working directory and returns what it
// printed.
func applyOutput(t *testing.T) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	return out, err
}

// A seed is additive. The seed file says which documents a collection starts with;
// it does not say which documents it may hold, so a document the app wrote to the
// collection survives the next apply, and a seeded document the app changed is
// written back by its _id, once.
func TestApply_ASeededCollectionKeepsTheDocumentsTheAppWrote(t *testing.T) {
	t.Skip("#3036 removes this skip: apply still drops a seeded collection before it seeds it")
	store, rec := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})

	out, err := applyOutput(t)
	if err != nil {
		t.Fatalf("the first apply: %v", err)
	}
	if !strings.Contains(out, "(seeded 5 docs)") {
		t.Errorf("the first apply does not say what it seeded:\n%s", out)
	}
	if got := store.count("menu"); got != 5 {
		t.Fatalf("the first apply left %d documents in the collection, want the 5 of the seed", got)
	}

	// The app adds a document and changes a seeded one.
	store.put("menu", map[string]any{"_id": "special", "price": 99, "collection": "menu"})
	store.put("menu", map[string]any{"_id": "pizza", "price": 12, "collection": "menu"})

	out, err = applyOutput(t)
	if err != nil {
		t.Fatalf("the second apply: %v", err)
	}
	if !strings.Contains(out, "(seeded 5 docs)") {
		t.Errorf("the second apply does not say what it seeded:\n%s", out)
	}

	if n := rec.count("POST /ops/backbone/nosql/drop"); n != 0 {
		t.Errorf("APPLY DROPPED A SEEDED COLLECTION %d time(s), which deletes every document the app wrote to it", n)
	}
	if got := store.has("menu", "special"); len(got) != 1 {
		t.Errorf("the document the app added is held %d time(s) after the apply, want once", len(got))
	}
	pizza := store.has("menu", "pizza")
	if len(pizza) != 1 {
		t.Fatalf("pizza is held %d time(s) after the apply, want once: a seed document is written by its _id", len(pizza))
	}
	if fmt.Sprint(pizza[0]["price"]) != "9" {
		t.Errorf("the seed document the app changed holds price %v after the apply, want the seed's 9", pizza[0]["price"])
	}
	if got := store.count("menu"); got != 6 {
		t.Errorf("the collection holds %d documents after the second apply, want the 5 of the seed and the 1 the app added", got)
	}
}

// A seed line that does not parse stops the apply at the file and the line, with
// nothing written: the collection the file names is not seeded in part.
func TestApply_ASeedLineThatDoesNotParseFailsAtItsFileAndLine(t *testing.T) {
	store, rec := seedProject(t, menuDriftfile, map[string]string{"seeds/menu.jsonl": menuSeed})
	// The live slice is read from the file as it was; the seed then goes bad.
	mustWrite(t, "seeds/menu.jsonl", `{"_id":"pizza","price":9}`+"\n\n"+`{"_id":"soup",`+"\n")

	_, err := applyOutput(t)
	if err == nil {
		t.Fatal("an apply of a seed with a line that is not JSON succeeded")
	}
	for _, want := range []string{"menu.jsonl:3", "invalid JSON"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
	if n := rec.count("POST /ops/backbone/write"); n != 0 {
		t.Errorf("the apply wrote %d seed documents before it refused the file", n)
	}
	if got := store.count("menu"); got != 0 {
		t.Errorf("the collection holds %d documents after a refused apply, want none", got)
	}
}

// The platform replaces a document by its _id only when the _id is a string of at
// most 255 bytes; any other _id is added again by every write. A seed is written
// on every apply and drops nothing, so such a record would add a copy at each one.
// It is refused where the file is read, at the file and the line.
func TestParseDriftfile_ASeedIDThePlatformCannotReplaceOnIsRefused(t *testing.T) {
	t.Skip("#3035 removes this skip: the seed reader accepts any _id that is not empty")
	withRealSchema(t)
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), menuDriftfile)
	mustWrite(t, filepath.Join(tmp, "seeds", "menu.jsonl"),
		`{"_id":"pizza","price":9}`+"\n"+
			`{"_id":7,"price":11}`+"\n"+ // a number
			`{"_id":"`+strings.Repeat("x", 256)+`","price":12}`+"\n"+ // 256 bytes
			`{"_id":"`+strings.Repeat("y", 255)+`","price":13}`+"\n") // 255 bytes: the longest that is replaced

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("a seed whose _ids the platform cannot replace on must not parse")
	}
	for _, want := range []string{":2: _id must be a string", ":3: _id is 256 bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q\ngot: %v", want, err)
		}
	}
	for _, not := range []string{":1:", ":4:"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("a record the platform replaces by its _id is refused (%q)\ngot: %v", not, err)
		}
	}
}
