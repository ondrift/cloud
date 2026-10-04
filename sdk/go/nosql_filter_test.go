package drift

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// A filter with several entries matches the documents that satisfy ALL of them,
// as it does on a slice. The four documents are chosen so either pair alone
// matches two and both together match one: an implementation honouring only
// the first pair, or only the last, answers two.
func filterFixture(t *testing.T) collectionHandle {
	t.Helper()
	t.Setenv("BACKBONE_URL", "")
	resetLocalStore(t)
	c := Backbone.NoSQL.Collection("ledger")
	for _, d := range []map[string]any{
		{"_id": "x1", "a": "1", "b": "1"},
		{"_id": "x2", "a": "1", "b": "2"},
		{"_id": "x3", "a": "2", "b": "1"},
		{"_id": "x4", "a": "3", "b": "2"},
	} {
		if _, err := c.Insert(d); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	return c
}

func ids(t *testing.T, rows []json.RawMessage) string {
	t.Helper()
	var out []string
	for _, raw := range rows {
		var doc struct {
			ID string `json:"_id"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal row: %v", err)
		}
		out = append(out, doc.ID)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Go's map order is random per call, so the pairs are sent in a different
// order each time. Twenty reads make an answer that depended on the order show.
func TestLocalListAndsEveryFilterEntry(t *testing.T) {
	c := filterFixture(t)
	for i := 0; i < 20; i++ {
		rows, err := c.List(map[string]string{"a": "1", "b": "2"})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got := ids(t, rows); got != "x2" {
			t.Fatalf("a=1 AND b=2 returned %q, want x2 — only one pair was honoured", got)
		}
	}
}

// THE CONTROL: one entry reads exactly as it did.
func TestLocalListWithOneFilterEntryIsUnchanged(t *testing.T) {
	c := filterFixture(t)
	rows, err := c.List(map[string]string{"a": "1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := ids(t, rows); got != "x1,x2" {
		t.Errorf("a=1 returned %q, want x1,x2", got)
	}
	doc, err := c.Get("x3")
	if err != nil || doc == nil {
		t.Fatalf("Get(x3) = %s, %v — Get is a one-entry filter", doc, err)
	}
}

// The slice refuses these with a 400, so the local store does too.
func TestLocalListRefusesWhatTheSliceRefuses(t *testing.T) {
	filterFixture(t)
	nine := "nosql/list?collection=ledger"
	for i := 0; i < 9; i++ {
		nine += fmt.Sprintf("&field=f%d&value=v", i)
	}
	eight := "nosql/list?collection=ledger"
	for i := 0; i < 8; i++ {
		eight += fmt.Sprintf("&field=f%d&value=v", i)
	}
	for _, tc := range []struct {
		what, path string
	}{
		{"unequal numbers of fields and values", "nosql/list?collection=ledger&field=a&field=b&value=1"},
		{"more than eight pairs", nine},
		{"an empty value among several", "nosql/list?collection=ledger&field=a&value=1&field=b&value="},
		{"an empty field among several", "nosql/list?collection=ledger&field=a&value=1&field=&value=2"},
		{"several pairs with order=insertion", "nosql/list?collection=ledger&order=insertion&field=a&value=1&field=b&value=2"},
	} {
		if _, err := callBackboneLocal("GET", tc.path, nil); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			t.Errorf("%s: got %v, want a 400", tc.what, err)
		}
	}
	if _, err := callBackboneLocal("GET", eight, nil); err != nil {
		t.Errorf("eight pairs is the cap and is admitted, got %v", err)
	}
}
