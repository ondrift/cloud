package drift

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The batch write is the primitive for anything that must land together — an
// order and its line items, a transfer's two legs. These pin the SDK's half of
// it: that the documents arrive, that they arrive as documents rather than
// wrapped, and that the count comes back.

func TestInsertManyWritesEveryDocument(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)

	docs := make([]any, 0, 50)
	for i := 0; i < 50; i++ {
		docs = append(docs, map[string]any{
			"_id":  fmt.Sprintf("%04x", i),
			"seq":  i,
			"kind": "line-item",
		})
	}

	n, err := Backbone.NoSQL.Collection("orders").InsertMany(docs)
	if err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	if n != 50 {
		t.Errorf("InsertMany reported %d written, want 50", n)
	}

	all, err := Backbone.NoSQL.Collection("orders").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 50 {
		t.Fatalf("the collection holds %d documents, want 50", len(all))
	}

	// Every document keyed, and keyed distinctly. A batch that reused one key
	// would report 50 written and store one.
	seen := map[string]bool{}
	for _, raw := range all {
		var doc struct {
			Key string `json:"_key"`
			Seq int    `json:"seq"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row: %v", err)
		}
		if doc.Key == "" {
			t.Fatal("a batched document carries no _key, so it cannot be paged")
		}
		if seen[doc.Key] {
			t.Errorf("_key %q written twice by one batch", doc.Key)
		}
		seen[doc.Key] = true
	}
	if len(seen) != 50 {
		t.Errorf("%d distinct documents, want 50", len(seen))
	}
}

// A batch and a loop over Insert must produce the same documents. This is the
// control: without it, a batch that stored something subtly different — the
// document nested under a field, say — would satisfy every count assertion
// above.
func TestInsertManyStoresWhatInsertStores(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")

	freshLocalStore(t)
	if _, err := Backbone.NoSQL.Collection("one").Insert(map[string]any{
		"_id": "a", "name": "widget", "qty": 3,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	single, err := Backbone.NoSQL.Collection("one").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	freshLocalStore(t)
	if _, err := Backbone.NoSQL.Collection("one").InsertMany([]any{
		map[string]any{"_id": "a", "name": "widget", "qty": 3},
	}); err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	batched, err := Backbone.NoSQL.Collection("one").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}

	if len(single) != 1 || len(batched) != 1 {
		t.Fatalf("expected one document each, got %d and %d", len(single), len(batched))
	}
	var a, b map[string]any
	if err := json.Unmarshal(single[0], &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(batched[0], &b); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"_id", "name", "qty"} {
		if fmt.Sprint(a[field]) != fmt.Sprint(b[field]) {
			t.Errorf("field %q: Insert stored %v, InsertMany stored %v", field, a[field], b[field])
		}
	}
}

// A non-map document takes the same `data` wrapping Insert gives it. Divergence
// here would store a value under one name through one method and another name
// through the other, which is discoverable from neither call site.
func TestInsertManyWrapsANonMapTheWayInsertDoes(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)

	if _, err := Backbone.NoSQL.Collection("raw").InsertMany([]any{"hello", 42}); err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	all, err := Backbone.NoSQL.Collection("raw").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d documents, want 2", len(all))
	}
	for _, raw := range all {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if _, ok := doc["data"]; !ok {
			t.Errorf("a scalar document was not wrapped under `data`: %s", raw)
		}
	}
}

// An empty batch is a no-op that costs no round trip, not an error. Callers
// build batches from filtered lists, and an empty result is the ordinary case.
func TestInsertManyOnNoDocumentsIsANoOp(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)

	n, err := Backbone.NoSQL.Collection("orders").InsertMany(nil)
	if err != nil {
		t.Fatalf("an empty batch must not be an error: %v", err)
	}
	if n != 0 {
		t.Errorf("an empty batch wrote %d documents", n)
	}
}

// A document carrying `_key` REPLACES the one at that key. This is what makes
// replaying a captured set idempotent: without it a restore run twice doubles
// every document, and the person most likely to run it twice is someone whose
// first attempt looked like it failed.
func TestInsertManyReplacesADocumentAtASuppliedKey(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)

	if _, err := Backbone.NoSQL.Collection("log").InsertMany([]any{
		map[string]any{"_key": "k1", "n": 1},
		map[string]any{"_key": "k2", "n": 2},
	}); err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	// The same batch again — a replay.
	if _, err := Backbone.NoSQL.Collection("log").InsertMany([]any{
		map[string]any{"_key": "k1", "n": 1},
		map[string]any{"_key": "k2", "n": 2},
	}); err != nil {
		t.Fatalf("InsertMany replay: %v", err)
	}

	all, err := Backbone.NoSQL.Collection("log").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("replaying a batch produced %d documents, want the same 2", len(all))
	}
}

// THE ONE THAT WOULD HAVE BEEN SILENT. The local store's dispatch is a switch
// with no default arm reachable by success: a path it does not implement returns
// an error rather than nil, because nil is indistinguishable from a successful
// empty body and would tell a caller its write landed when nothing was stored.
//
// `hurdles/020` is that failure found the hard way. This asserts the batch path
// is genuinely wired rather than merely not erroring, by checking the documents
// are readable back — the assertion an unimplemented path cannot pass.
func TestInsertManyIsImplementedLocallyRatherThanSilentlySkipped(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)

	if _, err := Backbone.NoSQL.Collection("proof").InsertMany([]any{
		map[string]any{"_id": "x", "v": 1},
	}); err != nil {
		t.Fatalf("the local store must implement write/batch: %v", err)
	}
	got, err := Backbone.NoSQL.Collection("proof").Get("x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("the batch reported success and stored nothing — the exact failure hurdles/020 records")
	}
}
