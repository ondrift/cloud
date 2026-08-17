package project

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// recorder collects every request path+method an apply issues, so a test can
// assert on what the CLI DID rather than on what it returned. The four
// reconcilers are best-effort and swallow their own failures, so a return value
// says nothing about whether a live resource was destroyed.
type recorder struct {
	mu   sync.Mutex
	hits []string
}

func (r *recorder) note(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, method+" "+path)
}

func (r *recorder) count(want string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, h := range r.hits {
		if h == want {
			n++
		}
	}
	return n
}

// stubAPI points the CLI at an httptest server for the duration of one test and
// seeds a scratch session, so no request reaches the operator's real ~/.drift.
func stubAPI(t *testing.T, h http.HandlerFunc) *recorder {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.Method, r.URL.Path)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })

	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	return rec
}

// manifestRooted is manifestFrom for a reconcile that resolves paths — a SQL
// schema file, a canvas directory — against the project root.
func manifestRooted(slice Node, baseDir string) *Manifest {
	return &Manifest{doc: slice, slice: slice, baseDir: baseDir}
}

// A manifest declares a database because it has a schema or a seed to apply to
// it, not because it is the whole inventory. Dropping the four it does not
// mention destroys them: the slice closes the connection and removes the .db,
// -wal and -shm files.
func TestApplySQL_DoesNotDropAnUnmentionedDatabase(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/backbone/sql/admin/list":
			_, _ = w.Write([]byte(`[{"name":"orders","size_bytes":1},{"name":"ledger","size_bytes":2},{"name":"audit","size_bytes":3}]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "orders.sql"), []byte("CREATE TABLE IF NOT EXISTS o (id TEXT);"), 0o600); err != nil {
		t.Fatalf("writing the schema file: %v", err)
	}
	m := manifestRooted(Node{"name": "demo", "backbone": map[string]any{
		"sql": []any{map[string]any{"name": "orders", "schema": "orders.sql"}},
	}}, root)

	if _, err := applySQL(m); err != nil {
		t.Fatalf("applySQL: %v", err)
	}

	if n := rec.count("POST /ops/backbone/sql/admin/drop"); n != 0 {
		t.Errorf("a database the manifest does not mention must survive the apply — its data is removed from disk, not detached; got %d drop request(s)", n)
	}
}

// A canvas project that adds one site must not take the others with it.
// pruneCanvas asks the slice to delete every site directory outside the keep
// list, so an incomplete manifest is a destructive one.
func TestApplyCanvas_DoesNotPruneAnUnmentionedSite(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	root := t.TempDir()
	siteDir := filepath.Join(root, "site")
	if err := os.MkdirAll(siteDir, 0o750); err != nil {
		t.Fatalf("making the site dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatalf("writing the site file: %v", err)
	}
	m := manifestRooted(Node{"name": "demo", "canvas": map[string]any{
		"sites": []any{map[string]any{"dir": "./site", "route": "/"}},
	}}, root)

	if err := applyCanvas(m, io.Discard); err != nil {
		t.Fatalf("applyCanvas: %v", err)
	}

	if n := rec.count("POST /ops/canvas/prune"); n != 0 {
		t.Errorf("a site the manifest does not mention must survive the apply; got %d prune request(s)", n)
	}
	if n := rec.count("POST /ops/canvas"); n != 1 {
		t.Errorf("the declared site must still deploy, got %d upload(s)", n)
	}
}

// applyAlerts has no zero-entry guard at all, so a project whose functions
// declare no alerts wipes the live registry on its next apply.
func TestApplyAlerts_DoesNotRemoveAnUndeclaredAlert(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/atomic/alert":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[{"name":"post:checkout-0"},{"name":"get:health-0"}]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})

	m := manifestRooted(Node{"name": "demo", "atomic": map[string]any{
		"functions": []any{map[string]any{"name": "get:ping", "handler": "GetPing"}},
	}}, t.TempDir())

	if _, err := applyAlerts(m); err != nil {
		t.Fatalf("applyAlerts: %v", err)
	}

	if n := rec.count("DELETE /ops/atomic/alert"); n != 0 {
		t.Errorf("an alert the manifest does not declare must survive the apply; got %d delete request(s)", n)
	}
}

// applyDomains removes every live host the manifest omits, so two projects
// deploying into one slice make each other's domains disappear.
func TestApplyDomains_DoesNotRemoveAnUndeclaredHost(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/domain":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[{"host":"shop.example.com","status":"live"},{"host":"admin.example.com","status":"live"}]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})

	m := manifestRooted(Node{"name": "demo", "domains": []any{
		map[string]any{"host": "shop.example.com"},
	}}, t.TempDir())

	if _, err := applyDomains(m); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}

	if n := rec.count("DELETE /ops/slice/domain"); n != 0 {
		t.Errorf("a host the manifest does not declare must survive the apply; got %d delete request(s)", n)
	}
}

// The control: what a manifest DOES name still reaches the slice. Removing the
// delete loops must not cost the create-and-update half of the reconcile.
func TestApply_StillCreatesWhatTheManifestNames(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/domain":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/ops/atomic/alert":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/ops/backbone/sql/admin/list":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "orders.sql"), []byte("CREATE TABLE IF NOT EXISTS o (id TEXT);"), 0o600); err != nil {
		t.Fatalf("writing the schema file: %v", err)
	}
	slice := Node{
		"name":    "demo",
		"domains": []any{map[string]any{"host": "shop.example.com"}},
		"backbone": map[string]any{
			"sql": []any{map[string]any{"name": "orders", "schema": "orders.sql"}},
		},
		"atomic": map[string]any{"functions": []any{map[string]any{
			"name":    "post:checkout",
			"handler": "PostCheckout",
			"alerts": []any{map[string]any{
				"on": "errors", "threshold": 3, "window": "5m", "notify": "webhook=https://example.com/hook",
			}},
		}}},
	}
	m := manifestRooted(slice, root)

	if _, err := applyDomains(m); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}
	if _, err := applyAlerts(m); err != nil {
		t.Fatalf("applyAlerts: %v", err)
	}
	if _, err := applySQL(m); err != nil {
		t.Fatalf("applySQL: %v", err)
	}

	if n := rec.count("POST /ops/slice/domain"); n != 1 {
		t.Errorf("a declared host must still be added, got %d", n)
	}
	if n := rec.count("POST /ops/atomic/alert"); n != 1 {
		t.Errorf("a declared alert must still be written, got %d", n)
	}
	if n := rec.count("POST /ops/backbone/sql/admin/load-schema"); n != 1 {
		t.Errorf("a declared schema must still be uploaded, got %d", n)
	}
}
