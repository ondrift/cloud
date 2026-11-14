package backbone

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// pageOf builds a JSON array of n documents whose `_key` continues from start, so a
// stub can hand out successive pages the way the platform does.
func pageOf(start, n int) []byte {
	docs := make([]json.RawMessage, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, json.RawMessage(fmt.Sprintf(`{"_key":"k%05d","n":%d}`, start+i, start+i)))
	}
	b, _ := json.Marshal(docs)
	return b
}

// seedSession points the CLI at a stub and gives it a token to attach, in a scratch
// HOME so the operator's own ~/.drift is never touched.
func seedSession(t *testing.T, srv *httptest.Server) {
	t.Helper()
	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
}

func runList(t *testing.T, flags map[string]string) error {
	t.Helper()
	cmd := nosqlListCmd()
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("setting --%s: %v", k, err)
		}
	}
	return cmd.RunE(cmd, []string{})
}

// One request returns at most 1000 documents and the response says nothing about
// being short, so a bigger collection cannot be read correctly without paging.
// --all has to walk the cursor to the end.
func TestListAllPagesUntilTheCollectionIsExhausted(t *testing.T) {
	var mu sync.Mutex
	var cursors []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/backbone/nosql/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		after := r.URL.Query().Get("after")
		cursors = append(cursors, after)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		switch after {
		case "":
			_, _ = w.Write(pageOf(0, nosqlPageMax)) // a full page — more to come
		case fmt.Sprintf("k%05d", nosqlPageMax-1):
			_, _ = w.Write(pageOf(nosqlPageMax, 200)) // short page — the end
		default:
			t.Errorf("unexpected cursor %q", after)
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	if err := runList(t, map[string]string{"collection": "ops", "all": "true"}); err != nil {
		t.Fatalf("list --all: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"", fmt.Sprintf("k%05d", nosqlPageMax-1)}
	if len(cursors) != len(want) {
		t.Fatalf("expected %d requests (%v), got %d (%v)", len(want), want, len(cursors), cursors)
	}
	for i := range want {
		if cursors[i] != want[i] {
			t.Errorf("request %d resumed from %q, want %q", i, cursors[i], want[i])
		}
	}
}

// A stub that ignores the cursor keeps answering with the same full page. Without a
// guard that is an infinite loop; with one it has to be reported, because returning
// the first page as if it were the collection is the failure paging exists to remove.
func TestListAllRefusesACursorThatDoesNotAdvance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pageOf(0, nosqlPageMax)) // always the same full page
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	err := runList(t, map[string]string{"collection": "ops", "all": "true"})
	if err == nil {
		t.Fatal("a non-advancing cursor must be reported, not looped on or silently truncated")
	}
	if !strings.Contains(err.Error(), "did not advance") {
		t.Errorf("the error must name the cause, got: %v", err)
	}
}

// A document with no `_key` cannot be paged from. That is a broken response rather
// than the end of the collection, and must not read as the latter.
func TestListAllRefusesAPageWithNoStorageKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		docs := make([]string, 0, nosqlPageMax)
		for i := 0; i < nosqlPageMax; i++ {
			docs = append(docs, `{"n":1}`) // full page, no _key anywhere
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[" + strings.Join(docs, ",") + "]"))
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	err := runList(t, map[string]string{"collection": "ops", "all": "true"})
	if err == nil || !strings.Contains(err.Error(), "_key") {
		t.Errorf("a page with no _key must be reported as unpageable, got: %v", err)
	}
}

// Controls: without --all the command issues exactly ONE request and sends no
// cursor, and --after is passed through when given. The default read must stay a
// single round trip.
func TestListWithoutAllIssuesOneRequest(t *testing.T) {
	var mu sync.Mutex
	var reqs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.URL.RawQuery)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pageOf(0, 100))
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	if err := runList(t, map[string]string{"collection": "ops"}); err != nil {
		t.Fatalf("plain list: %v", err)
	}
	if err := runList(t, map[string]string{"collection": "ops", "after": "k00042"}); err != nil {
		t.Fatalf("list --after: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d: %v", len(reqs), reqs)
	}
	if strings.Contains(reqs[0], "after=") {
		t.Errorf("a plain list must send no cursor, got %q", reqs[0])
	}
	if !strings.Contains(reqs[1], "after=k00042") {
		t.Errorf("--after must reach the platform, got %q", reqs[1])
	}
}

// collectionOf serves a collection of n documents, keys k00000 on, the way the
// platform pages one: at most `limit` documents after the cursor, in key order. It
// returns the limit of every request it was sent.
func collectionOf(t *testing.T, n int) *[]int {
	t.Helper()
	var mu sync.Mutex
	limits := &[]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/backbone/nosql/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var limit int
		_, _ = fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			_, _ = fmt.Sscanf(after, "k%d", &start)
			start++
		}
		mu.Lock()
		*limits = append(*limits, limit)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pageOf(start, max(0, min(limit, n-start))))
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)
	return limits
}

// A bounded read stops at the bound and says whether the collection goes on. The
// one document it asks for to know is not returned, and a collection of exactly
// the bound is not taken for a larger one.
func TestReadNoSQLCollectionStopsAtTheBoundAndSaysWhetherThereIsMore(t *testing.T) {
	for _, c := range []struct {
		name     string
		size     int
		bound    int
		wantDocs int
		wantMore bool
		limits   []int
	}{
		{"a larger collection", 2500, 2000, 2000, true, []int{1000, 1000, 1}},
		{"exactly the bound", 2000, 2000, 2000, false, []int{1000, 1000, 1}},
		{"short of the second page", 1500, 2000, 1500, false, []int{1000, 1000}},
		{"a bound that is not a page", 1700, 1500, 1500, true, []int{1000, 500, 1}},
		{"an empty collection", 0, 2000, 0, false, []int{1000}},
		{"no bound reads it all", 2500, 0, 2500, false, []int{1000, 1000, 1000}},
	} {
		t.Run(c.name, func(t *testing.T) {
			limits := collectionOf(t, c.size)
			docs, more, err := ReadNoSQLCollection("ops", c.bound)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(docs) != c.wantDocs || more != c.wantMore {
				t.Errorf("read %d documents and more=%v, want %d and %v", len(docs), more, c.wantDocs, c.wantMore)
			}
			if fmt.Sprint(*limits) != fmt.Sprint(c.limits) {
				t.Errorf("the pages asked for were %v, want %v", *limits, c.limits)
			}
		})
	}
}

// A read that fails is an error and no documents: a half-read collection is not
// something to compare with.
func TestReadNoSQLCollectionReturnsNothingFromAFailedRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	docs, more, err := ReadNoSQLCollection("ops", 2000)
	if err == nil || docs != nil || more {
		t.Errorf("a failed read returned %d documents, more=%v and the error %v", len(docs), more, err)
	}
}
