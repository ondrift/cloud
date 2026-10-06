package drift

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// queueScenario drives every queue call once and records what each answered,
// with "error" standing for a call that failed. It is the same sequence the
// conformance suite runs in every language.
func queueScenario(t *testing.T) map[string]any {
	t.Helper()
	out := map[string]any{}
	errOr := func(v any, err error) any {
		if err != nil {
			return "error"
		}
		return v
	}
	body := func(raw json.RawMessage, err error) any {
		if err != nil {
			return "error"
		}
		if raw == nil {
			return nil
		}
		return fieldOf(t, raw)
	}

	q := Backbone.Queue("conformance-jobs")
	out["pop_never_pushed"] = body(Backbone.Queue("never-pushed").Pop())
	out["len_never_pushed"] = errOr(Backbone.Queue("never-pushed").Len())
	out["push_not_an_object"] = errOr(q.Push("a bare string"))
	out["push_bad_name"] = errOr(Backbone.Queue("-flag").Push(map[string]any{"n": 0}))
	var ids []any
	for n := 1; n <= 3; n++ {
		id, err := q.Push(map[string]any{"n": n})
		if err != nil {
			t.Fatalf("Push %d: %v", n, err)
		}
		ids = append(ids, id)
	}
	out["push_ids"] = ids
	out["len_after_push"] = errOr(q.Len())
	out["peek"] = body(q.Peek())
	out["len_after_peek"] = errOr(q.Len())
	var popped []any
	for range 3 {
		popped = append(popped, body(q.Pop()))
	}
	out["pop_in_order"] = popped
	out["pop_empty"] = body(q.Pop())
	out["peek_empty"] = body(q.Peek())
	out["len_after_pop"] = errOr(q.Len())
	return out
}

// A queue the Driftfile declares exists before anything runs: on a slice
// because `drift file apply` creates it, locally because the CLI names it in
// DRIFT_DECLARED_QUEUES. A consumer polling it before the first push reads it
// empty in both places; a queue nobody declared is still the slice's 404.
func TestLocalQueue_ADeclaredQueueExistsBeforeItsFirstPush(t *testing.T) {
	t.Setenv("BACKBONE_URL", "")
	t.Setenv("DRIFT_DECLARED_QUEUES", "txs, blocks")
	resetLocalStore(t)

	for _, name := range []string{"txs", "blocks"} {
		msg, err := Backbone.Queue(name).Pop()
		if err != nil || msg != nil {
			t.Errorf("Pop on the declared queue %q = (%s, %v), want (nil, nil)", name, msg, err)
		}
		if n, err := Backbone.Queue(name).Len(); err != nil || n != 0 {
			t.Errorf("Len of the declared queue %q = (%d, %v), want (0, nil)", name, n, err)
		}
	}
	if _, err := Backbone.Queue("undeclared").Pop(); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Pop on a queue nobody declared = %v, want the slice's 404", err)
	}
}

// The local store and a slice answer every queue call alike: the same values,
// the same empties, and the same refusals. Before, the local store read a pop's
// queue from wherever the caller put it, had no peek and no length, accepted any
// body under any name, and called a queue that did not exist empty.
func TestQueue_LocalAndSliceAgreeOnEveryCall(t *testing.T) {
	want := map[string]any{
		"pop_never_pushed":   "error",
		"len_never_pushed":   "error",
		"push_not_an_object": "error",
		"push_bad_name":      "error",
		"push_ids":           []any{"0", "1", "2"},
		"len_after_push":     3,
		"peek":               1,
		"len_after_peek":     3,
		"pop_in_order":       []any{1, 2, 3},
		"pop_empty":          nil,
		"peek_empty":         nil,
		"len_after_pop":      0,
	}
	got := map[string]map[string]any{}
	for _, where := range []string{"local", "slice"} {
		url := ""
		if where == "slice" {
			url = sliceQueueServer(t).URL
		}
		t.Setenv("BACKBONE_URL", url)
		resetLocalStore(t)
		got[where] = queueScenario(t)
		for k, v := range want {
			if !reflect.DeepEqual(got[where][k], v) {
				t.Errorf("%s: %s = %#v, want %#v", where, k, got[where][k], v)
			}
		}
	}
	if !reflect.DeepEqual(got["local"], got["slice"]) {
		t.Errorf("the local store and the slice disagree:\n  local: %v\n  slice: %v", got["local"], got["slice"])
	}
}
