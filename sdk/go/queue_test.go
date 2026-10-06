package drift

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// sliceQueueServer stands in for a slice's queue routes, and answers as the
// slice's backbone router does (src/slice/src/backbone/mod.rs in the platform):
//
//	POST /queue/push  {"queue":"q","body":{...}}  -> 200 {"id":"<n>"}
//	POST /queue/pop   ?queue=q                    -> 200 <Message> | 204 | 404
//	GET  /queue/peek  ?queue=q                    -> 200 <Message> | 204 | 404
//	GET  /queue/len   ?queue=q                    -> 200 {"length":N} | 404
//
// The queue's name is read from the query string for the three reads and from
// the body for a push, and from nowhere else: a pop that names its queue in a
// body is answered 400 "queue query param required", as the slice answers it.
// The conformance suite runs the same cases against a real slice.
//
// It shares no code with the SDK's local store, so a fault in one cannot hide
// in the other.
func sliceQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	type message struct {
		ID        string          `json:"id"`
		Body      json.RawMessage `json:"body"`
		Timestamp string          `json:"timestamp"`
	}
	type queue struct {
		msgs   []message
		nextID int
	}
	var mu sync.Mutex
	queues := map[string]*queue{}

	// The slice's NAME_RULE: an alphanumeric first byte, then alphanumerics and
	// `. _ -`, at most 255 bytes.
	validName := func(s string) bool {
		if s == "" || len(s) > 255 {
			return false
		}
		for i, c := range []byte(s) {
			alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
				return false
			}
		}
		return true
	}
	fail := func(w http.ResponseWriter, status int, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	read := func(w http.ResponseWriter, r *http.Request) (*queue, bool) {
		name := r.URL.Query().Get("queue")
		switch {
		case name == "":
			fail(w, http.StatusBadRequest, "queue query param required")
		case !validName(name):
			fail(w, http.StatusBadRequest, "invalid queue name")
		case queues[name] == nil:
			fail(w, http.StatusNotFound, "queue "+name+" not found")
		default:
			return queues[name], true
		}
		return nil, false
	}
	head := func(w http.ResponseWriter, q *queue, remove bool) {
		if len(q.msgs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		msg := q.msgs[0]
		if remove {
			q.msgs = q.msgs[1:]
		}
		_ = json.NewEncoder(w).Encode(msg)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "POST /queue/push":
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Queue string          `json:"queue"`
				Body  json.RawMessage `json:"body"`
			}
			if json.Unmarshal(raw, &req) != nil {
				fail(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			var obj map[string]json.RawMessage
			if req.Queue == "" || json.Unmarshal(req.Body, &obj) != nil || obj == nil {
				fail(w, http.StatusBadRequest, "queue and body required")
				return
			}
			if !validName(req.Queue) {
				fail(w, http.StatusBadRequest, "invalid queue name")
				return
			}
			q := queues[req.Queue]
			if q == nil {
				q = &queue{}
				queues[req.Queue] = q
			}
			id := strconv.Itoa(q.nextID)
			q.nextID++
			q.msgs = append(q.msgs, message{ID: id, Body: req.Body, Timestamp: "2026-01-01T00:00:00.000000000Z"})
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		case "POST /queue/pop":
			if q, ok := read(w, r); ok {
				head(w, q, true)
			}
		case "GET /queue/peek":
			if q, ok := read(w, r); ok {
				head(w, q, false)
			}
		case "GET /queue/len":
			if q, ok := read(w, r); ok {
				_ = json.NewEncoder(w).Encode(map[string]int{"length": len(q.msgs)})
			}
		default:
			fail(w, http.StatusNotFound, "not found")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fieldOf reads the integer `n` out of a popped body, or -1 when it is absent.
func fieldOf(t *testing.T, body json.RawMessage) int {
	t.Helper()
	var doc struct {
		N *int `json:"n"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.N == nil {
		t.Fatalf("the popped body %s carries no `n`", body)
	}
	return *doc.N
}

// Pop has to reach a slice. It named its queue in a JSON body, which the slice
// never reads, so every Pop on a slice answered 400 "queue query param
// required" and worked only against the in-memory local store.
func TestQueuePop_ReachesASlice(t *testing.T) {
	t.Setenv("BACKBONE_URL", sliceQueueServer(t).URL)
	q := Backbone.Queue("jobs")

	for n, want := range []string{"0", "1"} {
		id, err := q.Push(map[string]any{"n": n + 1})
		if err != nil {
			t.Fatalf("Push %d: %v", n+1, err)
		}
		if id != want {
			t.Errorf("Push %d returned id %q, want the slice's %q", n+1, id, want)
		}
	}
	for want := 1; want <= 2; want++ {
		got, err := q.Pop()
		if err != nil {
			t.Fatalf("Pop on a slice: %v", err)
		}
		if n := fieldOf(t, got); n != want {
			t.Errorf("Pop returned n=%d, want %d: the queue is first in, first out", n, want)
		}
	}
	got, err := q.Pop()
	if err != nil || got != nil {
		t.Errorf("Pop on an empty queue = (%s, %v), want (nil, nil): the slice answers 204", got, err)
	}
}

// What Pop hands back is the body that was pushed, on a slice and locally
// alike. The slice answers with an envelope around it ({id, body, timestamp})
// and the local store with the same envelope, so a caller that reads its own
// fields off the result reads the same thing in both places.
func TestQueuePop_ReturnsThePushedBodyInBothPlaces(t *testing.T) {
	for _, where := range []string{"local", "slice"} {
		t.Run(where, func(t *testing.T) {
			url := ""
			if where == "slice" {
				url = sliceQueueServer(t).URL
			}
			t.Setenv("BACKBONE_URL", url)
			resetLocalStore(t)

			q := Backbone.Queue("receipts")
			if _, err := q.Push(map[string]any{"n": 7, "to": "a@example.com"}); err != nil {
				t.Fatalf("Push: %v", err)
			}
			got, err := q.Pop()
			if err != nil {
				t.Fatalf("Pop: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("Pop returned %s, which is not the pushed object: %v", got, err)
			}
			if doc["to"] != "a@example.com" || len(doc) != 2 {
				t.Errorf("Pop returned %s, want exactly the pushed body {\"n\":7,\"to\":\"a@example.com\"}", got)
			}
		})
	}
}

// A queue nothing was ever pushed to does not exist on a slice, which answers
// 404. The local store answered "empty", so a consumer polling a misspelled
// queue waited forever locally and failed only once deployed.
func TestQueuePop_ANeverPushedQueueIsAnErrorInBothPlaces(t *testing.T) {
	for _, where := range []string{"local", "slice"} {
		t.Run(where, func(t *testing.T) {
			url := ""
			if where == "slice" {
				url = sliceQueueServer(t).URL
			}
			t.Setenv("BACKBONE_URL", url)
			resetLocalStore(t)

			_, err := Backbone.Queue("never-pushed").Pop()
			if err == nil || !strings.Contains(err.Error(), "404") {
				t.Errorf("Pop on a queue nothing was pushed to = %v, want the slice's 404", err)
			}
		})
	}
}
