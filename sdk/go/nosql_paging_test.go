package drift

import (
	"encoding/json"
	"fmt"
	"testing"
)

// freshLocalStore empties the process-wide local backbone so one test's documents
// cannot be counted by the next.
func freshLocalStore(t *testing.T) {
	t.Helper()
	localBackbone.mu.Lock()
	defer localBackbone.mu.Unlock()
	localBackbone.nosql = make(map[string]map[string]json.RawMessage)
	localBackbone.nextID = 0
}

func seedCollection(t *testing.T, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := Backbone.NoSQL.Collection(name).Insert(map[string]any{
			"_id":      fmt.Sprintf("%04x", i),
			"group_id": "g1",
			"seq":      i,
		}); err != nil {
			t.Fatalf("seeding document %d: %v", i, err)
		}
	}
}

// A ledger is only correct when it can be read whole. One page is capped and the
// response carries nothing marking it short, so a collection past the cap has to
// be paged or it cannot be read correctly at all.
func TestListAllReadsEveryDocumentPastOnePage(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)
	const total = 1200
	seedCollection(t, "ops", total)

	all, err := Backbone.NoSQL.Collection("ops").ListAll(nil)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != total {
		t.Errorf("ListAll returned %d documents, want all %d", len(all), total)
	}

	// Every document exactly once. A cursor that overlapped would double-count a
	// leaf, which corrupts a Merkle root as surely as dropping one.
	seen := map[string]bool{}
	for _, raw := range all {
		var doc struct {
			Key string `json:"_key"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row: %v", err)
		}
		if doc.Key == "" {
			t.Fatal("a listed document carries no _key, so it cannot be paged")
		}
		if seen[doc.Key] {
			t.Errorf("_key %q served twice", doc.Key)
		}
		seen[doc.Key] = true
	}
	if len(seen) != total {
		t.Errorf("saw %d distinct documents, want %d", len(seen), total)
	}
}

// The filtered path is the one an indexed read takes, and the one a ledger uses.
func TestListAllPagesAFilteredRead(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)
	const total = 1200
	seedCollection(t, "ops", total)

	all, err := Backbone.NoSQL.Collection("ops").ListAll(map[string]string{"group_id": "g1"})
	if err != nil {
		t.Fatalf("ListAll filtered: %v", err)
	}
	if len(all) != total {
		t.Errorf("filtered ListAll returned %d documents, want all %d", len(all), total)
	}
}

// The control: one List is still one page. ListAll must not have removed the cap,
// because an unbounded single read is a memory risk inside an Atomic function.
func TestListReturnsOnePageAndStaysCapped(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	freshLocalStore(t)
	seedCollection(t, "ops", 1200)

	rows, err := Backbone.NoSQL.Collection("ops").List(nil, 5000)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1000 {
		t.Errorf("List with an over-large limit returned %d, want the 1000 cap", len(rows))
	}

	// And an omitted limit is the platform's default, not "everything".
	rows, err = Backbone.NoSQL.Collection("ops").List(nil)
	if err != nil {
		t.Fatalf("List with no limit: %v", err)
	}
	if len(rows) != 100 {
		t.Errorf("List with no limit returned %d, want the 100 default", len(rows))
	}
}

// A collection that fits in one page must not cost a second round trip, and a
// collection landing exactly on a page boundary must still terminate.
func TestListAllHandlesShortAndExactCollections(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	for _, total := range []int{0, 1, 999, 1000, 1001} {
		freshLocalStore(t)
		seedCollection(t, "ops", total)
		all, err := Backbone.NoSQL.Collection("ops").ListAll(nil)
		if err != nil {
			t.Fatalf("ListAll over %d documents: %v", total, err)
		}
		if len(all) != total {
			t.Errorf("ListAll over %d documents returned %d", total, len(all))
		}
	}
}
