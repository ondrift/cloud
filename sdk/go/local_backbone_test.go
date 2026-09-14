package drift

import (
	"encoding/json"
	"strings"
	"testing"
)

// The local backbone stands in for a slice while a developer has none. Every
// test here asserts that it AGREES with the slice — measured against a live one
// — because the failure mode it is guarding against is silent: a switch with no
// default returns nil for a path it does not implement, and nil is
// indistinguishable from a successful call with an empty body.

// resetLocalStore empties every local map, not only the documents, so a lock or
// a secret left by one test cannot decide another.
func resetLocalStore(t *testing.T) {
	t.Helper()
	localBackbone.mu.Lock()
	defer localBackbone.mu.Unlock()
	localBackbone.nosql = make(map[string]map[string]json.RawMessage)
	localBackbone.cache = make(map[string]json.RawMessage)
	localBackbone.queues = make(map[string][]json.RawMessage)
	localBackbone.blobs = make(map[string][]byte)
	localBackbone.locks = make(map[string]localLock)
	localBackbone.secrets = make(map[string]string)
	localBackbone.nextID = 0
}

// A path the local store does not implement must FAIL. Reporting success for a
// call that stored nothing is the defect every other test here is a special case
// of: `callDeed` already refuses for exactly this reason, and Backbone did not.
func TestLocalBackboneRefusesAnUnimplementedPath(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	_, err := Backbone.SQL("clinic").Query("SELECT 1")
	if err == nil {
		t.Fatal("SQL.Query succeeded against the local store, which implements no SQL at all")
	}
	if !strings.Contains(err.Error(), "sql/query") {
		t.Errorf("the refusal must name the path that is missing, got: %v", err)
	}
}

// Deployed: 204 and the document is gone. Locally there was no nosql/delete
// case at all, so Delete returned nil and removed nothing — a cleanup handler
// tests green against a store that never deleted anything.
func TestLocalDeleteRemovesTheDocument(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	c := Backbone.NoSQL.Collection("chain")

	key, err := c.Insert(map[string]any{"_id": "a"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := c.Insert(map[string]any{"_id": "b"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := c.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, err := c.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("after deleting 1 of 2 documents the collection holds %d, want 1", len(rows))
	}
	var doc struct {
		ID string `json:"_id"`
	}
	if err := json.Unmarshal(rows[0], &doc); err != nil {
		t.Fatalf("unmarshal survivor: %v", err)
	}
	if doc.ID != "b" {
		t.Errorf("the wrong document survived: %q", doc.ID)
	}
}

// ListInOrder exists solely to fix the order a plain List returns. Locally the
// order parameter was ignored, so the method returned the very order it exists
// to avoid — 8,9,10,11,12,1..7 for twelve documents.
func TestLocalListInOrderIsInsertionOrder(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	c := Backbone.NoSQL.Collection("log")

	const total = 12
	for i := 1; i <= total; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	rows, err := c.ListInOrder(100)
	if err != nil {
		t.Fatalf("ListInOrder: %v", err)
	}
	if len(rows) != total {
		t.Fatalf("ListInOrder returned %d documents, want %d", len(rows), total)
	}
	for i, raw := range rows {
		var doc struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row %d: %v", i, err)
		}
		if doc.N != i+1 {
			t.Fatalf("position %d holds n=%d, want %d — the order is not the order it was written", i, doc.N, i+1)
		}
	}
}

// The control. The DEFAULT read is byte order over the key and must stay that
// way, or this fix has quietly changed the contract a paging caller holds.
func TestLocalDefaultListStaysInKeyOrder(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	c := Backbone.NoSQL.Collection("log")

	for i := 1; i <= 12; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	rows, err := c.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, raw := range rows {
		var doc struct {
			Key string `json:"_key"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row: %v", err)
		}
		got = append(got, doc.Key)
	}
	want := "1,10,11,12,2,3,4,5,6,7,8,9"
	if strings.Join(got, ",") != want {
		t.Errorf("default order is %q, want the byte order %q", strings.Join(got, ","), want)
	}
}

// The slice 400s an order value it does not know rather than ignoring it, so a
// typo is a failure and not a silently unordered read.
func TestLocalListRefusesAnUnknownOrder(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	if _, err := Backbone.NoSQL.Collection("log").Insert(map[string]any{"n": 1}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	_, err := Backbone.NoSQL.Collection("log").fetchList("nosql/list?order=nonsense&collection=log")
	if err == nil {
		t.Fatal("an unknown order value was accepted; the slice answers 400")
	}
}

// An ordered read has no field index behind it, so the slice refuses the
// combination rather than quietly dropping one half of it.
func TestLocalListRefusesOrderWithAFilter(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	if _, err := Backbone.NoSQL.Collection("log").Insert(map[string]any{"n": 1, "g": "x"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	_, err := Backbone.NoSQL.Collection("log").fetchList("nosql/list?order=insertion&collection=log&field=g&value=x")
	if err == nil {
		t.Fatal("order=insertion was accepted alongside a field filter; the slice answers 400")
	}
}

// ListAllInOrder pages, so the ordered cursor has to advance in the ordered
// sequence rather than in key order. A log past one page is the case this is
// for, and it is the case a wrong cursor silently truncates.
func TestLocalOrderedPagingAdvancesInOrder(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	c := Backbone.NoSQL.Collection("log")

	const total = 2500 // past the 1000 cap, so ListAllInOrder must page three times
	for i := 1; i <= total; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	rows, err := c.ListAllInOrder()
	if err != nil {
		t.Fatalf("ListAllInOrder: %v", err)
	}
	if len(rows) != total {
		t.Fatalf("ListAllInOrder returned %d documents, want %d", len(rows), total)
	}
	for i, raw := range rows {
		var doc struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row %d: %v", i, err)
		}
		if doc.N != i+1 {
			t.Fatalf("position %d holds n=%d, want %d", i, doc.N, i+1)
		}
	}
}

// AcquireAs is renew-or-take by a STABLE owner: that is what makes it a lease
// rather than a lock. Locally the owner was ignored and a held lock returned an
// empty body, so the second call — the renew — failed to parse. Code written
// correctly for the platform was the code that looked broken.
func TestLocalAcquireAsRenewsForTheSameOwner(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	tok, err := Backbone.Lock.AcquireAs("anchor", "anchor-a", 30)
	if err != nil {
		t.Fatalf("first AcquireAs: %v", err)
	}
	if tok != "anchor-a" {
		t.Errorf("the token is %q, want the owner %q back", tok, "anchor-a")
	}

	tok2, err := Backbone.Lock.AcquireAs("anchor", "anchor-a", 30)
	if err != nil {
		t.Fatalf("re-acquiring as the SAME owner must renew, got: %v", err)
	}
	if tok2 != "anchor-a" {
		t.Errorf("the renewed token is %q, want %q", tok2, "anchor-a")
	}

	if _, err := Backbone.Lock.AcquireAs("anchor", "anchor-b", 30); err == nil {
		t.Error("a second owner took a live lease; the slice answers 409")
	}
}

// Renew is owner-guarded. Locally there was no lock/renew case, so every renew
// succeeded — including one by a caller holding nothing, which is how two
// active holders happen.
func TestLocalRenewIsOwnerGuarded(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	if _, err := Backbone.Lock.AcquireAs("anchor", "anchor-a", 30); err != nil {
		t.Fatalf("AcquireAs: %v", err)
	}
	if err := Backbone.Lock.Renew("anchor", "anchor-a", 30); err != nil {
		t.Fatalf("the holder must be able to renew: %v", err)
	}
	if err := Backbone.Lock.Renew("anchor", "anchor-b", 30); err == nil {
		t.Error("a non-holder renewed the lease")
	}
	if err := Backbone.Lock.Renew("never-held", "anybody", 30); err == nil {
		t.Error("a lock nobody holds was renewed")
	}
}

// Release is token-guarded for the same reason: a release that succeeds for a
// non-holder hands the lock to a third party while the holder is still working.
func TestLocalReleaseIsTokenGuarded(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	tok, err := Backbone.Lock.Acquire("job", 30)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := Backbone.Lock.Release("job", "not-the-token"); err == nil {
		t.Error("a non-holder released the lock")
	}
	if err := Backbone.Lock.Release("job", tok); err != nil {
		t.Fatalf("the holder must be able to release: %v", err)
	}
	if _, err := Backbone.Lock.Acquire("job", 30); err != nil {
		t.Fatalf("the lock was not free after its holder released it: %v", err)
	}
}

// A secret set then read must come back. Locally Set was a no-op and Get fell
// through to the environment, so the value read as "" with no error — and a
// guard like `if token == ""` takes the wrong branch.
func TestLocalSecretRoundTrips(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	if err := Backbone.Secret.Set("API_TOKEN", "s3cret"); err != nil {
		t.Fatalf("Secret.Set: %v", err)
	}
	got, err := Backbone.Secret.Get("API_TOKEN")
	if err != nil {
		t.Fatalf("Secret.Get: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("Secret.Get returned %q, want %q", got, "s3cret")
	}

	if err := Backbone.Secret.Delete("API_TOKEN"); err != nil {
		t.Fatalf("Secret.Delete: %v", err)
	}
	if _, err := Backbone.Secret.Get("API_TOKEN"); err == nil {
		t.Error("a deleted secret still reads back")
	}
}

// The env var still wins, because that is how the runner injects a DECLARED
// secret into a deployed function and local dev must take the same path.
func TestLocalSecretPrefersTheEnvironment(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	t.Setenv("DRIFT_SECRET_API_TOKEN", "from-env")
	resetLocalStore(t)

	if err := Backbone.Secret.Set("API_TOKEN", "from-store"); err != nil {
		t.Fatalf("Secret.Set: %v", err)
	}
	got, err := Backbone.Secret.Get("API_TOKEN")
	if err != nil {
		t.Fatalf("Secret.Get: %v", err)
	}
	if got != "from-env" {
		t.Errorf("Secret.Get returned %q, want the declared env value %q", got, "from-env")
	}
}

// Put then Get must return the bytes. Locally Put returned before reaching the
// store at all, and Get looked the object up under a name the SDK never sends —
// so blobs read as empty and looked broken rather than unimplemented.
func TestLocalBlobRoundTrips(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	want := []byte("hello, slice")
	if err := Backbone.Blob.Put("uploads/greeting.txt", want, "text/plain"); err != nil {
		t.Fatalf("Blob.Put: %v", err)
	}
	got, err := Backbone.Blob.Get("uploads/greeting.txt")
	if err != nil {
		t.Fatalf("Blob.Get: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Blob.Get returned %q, want %q", got, want)
	}
}

// A bare name has no bucket, so it lands in "default" — and must come back from
// there rather than from whatever the slash-splitting produced.
func TestLocalBlobRoundTripsWithoutABucket(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	if err := Backbone.Blob.Put("greeting.txt", []byte("bare"), ""); err != nil {
		t.Fatalf("Blob.Put: %v", err)
	}
	got, err := Backbone.Blob.Get("greeting.txt")
	if err != nil {
		t.Fatalf("Blob.Get: %v", err)
	}
	if string(got) != "bare" {
		t.Errorf("Blob.Get returned %q, want %q", got, "bare")
	}
}

// An empty queue is a legitimate empty answer, not a missing implementation.
// The default case must not turn one into the other.
func TestLocalEmptyAnswersStayEmpty(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)

	msg, err := Backbone.Queue("jobs").Pop()
	if err != nil {
		t.Fatalf("popping an empty queue must not be an error: %v", err)
	}
	if msg != nil {
		t.Errorf("an empty queue returned %q", msg)
	}

	val, err := Backbone.Cache.Get("absent")
	if err != nil {
		t.Fatalf("a cache miss must not be an error: %v", err)
	}
	if val != nil {
		t.Errorf("a cache miss returned %q", val)
	}
}
