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
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

func main() {
	out := map[string]any{}
	defer func() {
		if r := recover(); r != nil {
			out["error"] = fmt.Sprint(r)
			emit(out)
		}
	}()

	// DRIFT_CONFORMANCE_BACKBONE_URL names a running slice's Backbone, and runs
	// the queue cases against it alone: the slice is the authority on the queue
	// wire, and the local store is held to what it answers.
	if slice := os.Getenv("DRIFT_CONFORMANCE_BACKBONE_URL"); slice != "" {
		_ = os.Setenv("BACKBONE_URL", slice)
		queueCases(out)
		emit(out)
		return
	}

	// No BACKBONE_URL: every case runs against the in-memory local store, which
	// is the loop a developer meets first and the one where the six drifted.
	_ = os.Setenv("BACKBONE_URL", "")

	queueCases(out)

	// A declared queue, named the way the CLI names it for a local run.
	_ = os.Setenv("DRIFT_DECLARED_QUEUES", "conformance-declared")
	if msg, err := drift.Backbone.Queue("conformance-declared").Pop(); err != nil {
		out["declared_queue_pop_empty"] = "error"
	} else if msg == nil {
		out["declared_queue_pop_empty"] = nil
	} else {
		out["declared_queue_pop_empty"] = string(msg)
	}

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

	// ── Filtered NoSQL reads ────────────────────────────────────────────────
	filtered := drift.Backbone.NoSQL.Collection("conformance_filter")
	wide := map[string]any{"_id": "wide"}
	nine := map[string]string{}
	for i := 0; i < 9; i++ {
		wide[fmt.Sprintf("f%d", i)] = "v"
		nine[fmt.Sprintf("f%d", i)] = "v"
	}
	for _, doc := range []map[string]any{
		{"_id": "x1", "a": "1", "b": "1"},
		{"_id": "x2", "a": "1", "b": "2"},
		{"_id": "x3", "a": "2", "b": "1"},
		{"_id": "x4", "a": "3", "b": "2"},
		wide,
	} {
		if _, err := filtered.Insert(doc); err != nil {
			fail(out, "Insert %v: %v", doc["_id"], err)
		}
	}
	if rows, err := filtered.List(map[string]string{"a": "1", "b": "2"}); err != nil {
		fail(out, "List with two entries: %v", err)
	} else {
		out["list_filter_two_fields"] = fieldIDs(rows)
	}
	if rows, err := filtered.List(map[string]string{"a": "1"}); err != nil {
		fail(out, "List with one entry: %v", err)
	} else {
		out["list_filter_one_field"] = fieldIDs(rows)
	}
	if rows, err := filtered.List(nine); err != nil {
		out["list_filter_over_cap"] = "error"
	} else {
		out["list_filter_over_cap"] = fieldIDs(rows)
	}

	// ── Edge links ──────────────────────────────────────────────────────────
	// A loopback peer that echoes the path it was asked for and the identity
	// header it received, null when the header is absent.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var slice any
		if v := r.Header.Values("X-Drift-Slice"); len(v) > 0 {
			slice = strings.Join(v, ",")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "slice": slice})
	}))
	defer peer.Close()
	_ = os.Unsetenv("DRIFT_SLICE")
	_ = os.Setenv("DRIFT_LINK_BILLING_API_URL", peer.URL+"/base/")

	if e, err := echoed(drift.Edge.Slice("billing-api").Get("/api/events")); err != nil {
		fail(out, "Edge.Slice Get: %v", err)
	} else {
		out["edge_slice_url"] = e["path"]
		out["slice_identity_absent"] = e["slice"]
	}
	if e, err := echoed(drift.Slice("billing-api").Get("/api/events")); err != nil {
		fail(out, "Slice Get: %v", err)
	} else {
		out["slice_url"] = e["path"]
	}

	_ = os.Setenv("DRIFT_SLICE", "billing")
	if e, err := echoed(drift.Edge.Slice("billing-api").Request("GET", "/api/events",
		map[string]string{"X-Drift-Slice": "operator"}, nil)); err != nil {
		fail(out, "Edge.Slice Request with a caller identity: %v", err)
	} else {
		out["slice_identity_kept"] = e["slice"]
	}
	_ = os.Unsetenv("DRIFT_SLICE")

	if _, err := drift.Edge.Slice("billing").Get("/x"); err != nil {
		out["edge_slice_not_linked"] = err.Error()
	} else {
		out["edge_slice_not_linked"] = nil
	}
	if _, err := drift.Slice("billing").Get("/x"); err != nil {
		out["slice_not_linked"] = err.Error()
	} else {
		out["slice_not_linked"] = nil
	}

	emit(out)
}

// queueCases drives every queue call once. The names carry the time, so a run
// against a slice, whose queues outlive the run, never meets an earlier run's.
func queueCases(out map[string]any) {
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	q := drift.Backbone.Queue("conformance-go-" + stamp)
	absent := drift.Backbone.Queue("conformance-absent-go-" + stamp)

	body := func(raw json.RawMessage, err error) any {
		if err != nil {
			return "error"
		}
		if raw == nil {
			return nil
		}
		var doc struct {
			N int `json:"n"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return string(raw)
		}
		return doc.N
	}
	length := func(n int, err error) any {
		if err != nil {
			return "error"
		}
		return n
	}
	refused := func(_ string, err error) any {
		if err != nil {
			return "error"
		}
		return nil
	}

	out["queue_pop_never_pushed"] = body(absent.Pop())
	_, err := absent.Pop()
	out["queue_error_carries_the_refusal"] = err != nil &&
		strings.Contains(err.Error(), "HTTP 404") && strings.Contains(err.Error(), "not found")
	out["queue_len_never_pushed"] = length(absent.Len())
	out["queue_push_not_an_object"] = refused(q.Push("a bare string"))
	out["queue_push_bad_name"] = refused(drift.Backbone.Queue("-flag").Push(map[string]any{"n": 0}))
	ids := []any{}
	for n := 1; n <= 3; n++ {
		id, err := q.Push(map[string]any{"n": n})
		if err != nil {
			fail(out, "Queue.Push %d: %v", n, err)
		}
		ids = append(ids, id)
	}
	out["queue_push_ids"] = ids
	out["queue_len_after_push"] = length(q.Len())
	out["queue_peek"] = body(q.Peek())
	out["queue_len_after_peek"] = length(q.Len())
	popped := []any{}
	for range 3 {
		popped = append(popped, body(q.Pop()))
	}
	out["queue_pop_in_order"] = popped
	out["queue_pop_empty"] = body(q.Pop())
	out["queue_peek_empty"] = body(q.Peek())
	out["queue_len_after_pop"] = length(q.Len())
}

// echoed decodes what the loopback peer echoed back.
func echoed(resp *drift.HTTPResponse, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	var e map[string]any
	if err := json.Unmarshal(resp.Body, &e); err != nil {
		return nil, fmt.Errorf("the peer's reply is not JSON: %w", err)
	}
	return e, nil
}

// fieldIDs reads the `_id` of each row, sorted, so a case compares the set.
func fieldIDs(rows []json.RawMessage) []string {
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		var doc struct {
			ID string `json:"_id"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		out = append(out, doc.ID)
	}
	sort.Strings(out)
	return out
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
