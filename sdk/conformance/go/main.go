// Command conformance-go drives the Go SDK through the shared conformance cases
// and prints what it got, as JSON, for check.py to compare against
// expected.json.
//
// It asserts NOTHING itself. The assertions are in expected.json, once, for all
// six languages — six independently-worded suites can each pass while asserting
// subtly different things, and that is how five SDKs came to disagree with this
// one about local blobs without anybody noticing.
//
// Go is the reference implementation, so this driver passing is the harness
// working rather than news about Go.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	drift "github.com/ondrift/cloud/sdk/go"
)

func main() {
	// No BACKBONE_URL: every case runs against the in-memory local store, which
	// is the loop a developer meets first and the one where the six drifted.
	_ = os.Setenv("BACKBONE_URL", "")

	out := map[string]any{}
	defer func() {
		if r := recover(); r != nil {
			out["error"] = fmt.Sprint(r)
			emit(out)
		}
	}()

	// ── Blob ────────────────────────────────────────────────────────────────
	if err := drift.Backbone.Blob.Put("uploads/greeting.txt", []byte("hello, slice"), "text/plain"); err != nil {
		fail(out, "Blob.Put with a bucket: %v", err)
	}
	if got, err := drift.Backbone.Blob.Get("uploads/greeting.txt"); err != nil {
		fail(out, "Blob.Get with a bucket: %v", err)
	} else {
		out["blob_with_bucket"] = string(got)
	}

	if err := drift.Backbone.Blob.Put("greeting.txt", []byte("bare"), ""); err != nil {
		fail(out, "Blob.Put without a bucket: %v", err)
	}
	if got, err := drift.Backbone.Blob.Get("greeting.txt"); err != nil {
		fail(out, "Blob.Get without a bucket: %v", err)
	} else {
		out["blob_without_bucket"] = string(got)
	}

	// A key never written must FAIL, and "error" is what a failure reports here.
	// An SDK that returns empty-with-no-error reports null instead and fails the
	// case — which is the whole point, because an implementation returning empty
	// for EVERYTHING would otherwise pass the two cases above by accident.
	if got, err := drift.Backbone.Blob.Get("uploads/never-written.txt"); err != nil {
		out["blob_absent"] = "error"
	} else if len(got) == 0 {
		out["blob_absent"] = nil
	} else {
		out["blob_absent"] = string(got)
	}

	// ── Ordered NoSQL reads ─────────────────────────────────────────────────
	small := drift.Backbone.NoSQL.Collection("conformance_small")
	for i := 1; i <= 12; i++ {
		if _, err := small.Insert(map[string]any{"n": i}); err != nil {
			fail(out, "Insert %d: %v", i, err)
		}
	}

	if rows, err := small.ListInOrder(100); err != nil {
		fail(out, "ListInOrder: %v", err)
	} else {
		out["list_in_order"] = fieldN(rows)
	}

	if rows, err := small.List(nil); err != nil {
		fail(out, "List: %v", err)
	} else {
		out["list_default_order"] = fieldKey(rows)
	}

	// Past the 1000-row page cap, which is where a cursor that advances in KEY
	// order rather than in the ordered sequence silently truncates.
	big := drift.Backbone.NoSQL.Collection("conformance_big")
	for i := 1; i <= 1200; i++ {
		if _, err := big.Insert(map[string]any{"n": i}); err != nil {
			fail(out, "Insert big %d: %v", i, err)
		}
	}
	if rows, err := big.ListAllInOrder(); err != nil {
		fail(out, "ListAllInOrder: %v", err)
	} else {
		ns := fieldN(rows)
		out["list_all_in_order_count"] = len(ns)
		if len(ns) > 0 {
			out["list_all_in_order_first"] = ns[0]
			out["list_all_in_order_last"] = ns[len(ns)-1]
		}
		if len(ns) >= 1002 {
			out["list_all_in_order_boundary"] = ns[998:1002]
		}
	}

	emit(out)
}

// fieldN reads the `n` field out of each row, in the order the rows came back.
func fieldN(rows []json.RawMessage) []int {
	out := make([]int, 0, len(rows))
	for _, raw := range rows {
		var doc struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		out = append(out, doc.N)
	}
	return out
}

// fieldKey reads the storage key, which is what the default order is over.
func fieldKey(rows []json.RawMessage) []string {
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		var doc struct {
			Key string `json:"_key"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		out = append(out, doc.Key)
	}
	return out
}

// fail reports the first thing that went wrong and stops. A driver that carried
// on would report later cases against a store in a state nobody intended.
func fail(out map[string]any, format string, args ...any) {
	out["error"] = fmt.Sprintf(format, args...)
	emit(out)
	os.Exit(0) // check.py turns the reported error into the failure
}

func emit(out map[string]any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
