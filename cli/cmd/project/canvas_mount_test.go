package project

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// siteTree writes a one-file static site under root/name and returns the
// project-relative path a Driftfile would name.
func siteTree(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatalf("writing %s/index.html: %v", dir, err)
	}
	return "./" + name
}

// uploads captures what applyCanvas asked the slice to store: one slug/route
// pair per site, which is the derivation this file is about.
type uploads struct {
	mu    sync.Mutex
	pairs map[string]string
}

func (u *uploads) note(q url.Values) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pairs[q.Get("site")] = q.Get("route")
}

func (u *uploads) snapshot() map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string]string{}
	for k, v := range u.pairs {
		out[k] = v
	}
	return out
}

func stubCanvasAPI(t *testing.T) *uploads {
	t.Helper()
	up := &uploads{pairs: map[string]string{}}
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/canvas" {
			up.note(r.URL.Query())
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	_ = rec
	return up
}

// Two sites keyed by the mount path must land on two slugs. A reader that
// cannot see the key reads the empty string, and every site on the project
// collapses onto `default` — the last upload winning and the others lost.
func TestSiteMount_PathKeyedSitesGetTheirOwnSlug(t *testing.T) {
	up := stubCanvasAPI(t)
	root := t.TempDir()
	public := siteTree(t, root, "public")
	admin := siteTree(t, root, "admin")

	m := manifestRooted(Node{"name": "demo", "canvas": map[string]any{"sites": []any{
		map[string]any{"dir": public, "path": "/"},
		map[string]any{"dir": admin, "path": "/admin"},
	}}}, root)

	if err := applyCanvas(m, io.Discard); err != nil {
		t.Fatalf("applyCanvas: %v", err)
	}

	got := up.snapshot()
	want := map[string]string{"default": "/", "admin": "/admin"}
	if len(got) != len(want) {
		t.Fatalf("two sites must upload under two slugs, got %v", got)
	}
	for slug, route := range want {
		if got[slug] != route {
			t.Errorf("slug %q must mount at %q, got %q (all sites collapsing onto one slug loses every site but the last)", slug, route, got[slug])
		}
	}
}

// Two sites that resolve to one slug overwrite each other on the slice. The
// refusal has to come before any upload, so the first site is not replaced by
// the second on the way to discovering they collide.
func TestSiteMount_CollidingSlugsAreRefused(t *testing.T) {
	up := stubCanvasAPI(t)
	root := t.TempDir()
	first := siteTree(t, root, "first")
	second := siteTree(t, root, "second")

	m := manifestRooted(Node{"name": "demo", "canvas": map[string]any{"sites": []any{
		map[string]any{"dir": first, "route": "/admin"},
		map[string]any{"dir": second, "route": "/admin/"},
	}}}, root)

	err := applyCanvas(m, io.Discard)
	if err == nil {
		t.Fatalf("two sites resolving to one slug must be refused, got no error")
	}
	if n := len(up.snapshot()); n != 0 {
		t.Errorf("the refusal must land before any upload, got %d upload(s)", n)
	}
}

// The empty string is not a route. Defaulting it to "/" is what turns an
// unreadable value into agreement on the slug `default`.
func TestCanonicalRoute_EmptyValueIsRefused(t *testing.T) {
	if _, err := common.CanonicalRoute(""); err == nil {
		t.Errorf("an empty mount path must be refused rather than read as the root")
	}
	got, err := common.CanonicalRoute("/admin/")
	if err != nil {
		t.Fatalf("a trailing slash is legal: %v", err)
	}
	if got != "/admin" {
		t.Errorf("canonical form of /admin/ is /admin, got %q", got)
	}
}

// applyCanvas and layoutCanvas are two readers of one fact. They must derive
// the same slug and route from the same document, or a site that works locally
// lands somewhere else on the slice.
func TestCanvasDerivation_ApplyAndLayoutAgree(t *testing.T) {
	up := stubCanvasAPI(t)
	root := t.TempDir()
	public := siteTree(t, root, "public")
	admin := siteTree(t, root, "admin")
	docs := siteTree(t, root, "docs")

	m := manifestRooted(Node{"name": "demo", "canvas": map[string]any{"sites": []any{
		map[string]any{"dir": public, "path": "/"},
		map[string]any{"dir": admin, "path": "/admin"},
		map[string]any{"dir": docs, "route": "/docs"},
	}}}, root)

	if err := applyCanvas(m, io.Discard); err != nil {
		t.Fatalf("applyCanvas: %v", err)
	}

	out := filepath.Join(t.TempDir(), "canvas")
	if err := layoutCanvas(m, out); err != nil {
		t.Fatalf("layoutCanvas: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	var registry []struct {
		Slug  string `json:"slug"`
		Route string `json:"route"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatalf("decoding the registry: %v", err)
	}

	laid := map[string]string{}
	for _, e := range registry {
		laid[e.Slug] = e.Route
	}
	applied := up.snapshot()

	if len(laid) != len(applied) {
		t.Fatalf("the two readers disagree on how many sites there are: local %v, deployed %v", laid, applied)
	}
	slugs := make([]string, 0, len(applied))
	for s := range applied {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, s := range slugs {
		if laid[s] != applied[s] {
			t.Errorf("slug %q mounts at %q locally and %q on the slice", s, laid[s], applied[s])
		}
		if _, err := os.Stat(filepath.Join(out, s, "index.html")); err != nil {
			t.Errorf("slug %q has no local directory: %v", s, err)
		}
	}
}

// The control, and the shape most manifests are in: `route:` keeps working and
// keeps deriving what it always did, including the short form that names no
// mount path at all and means the root.
func TestSiteMount_RouteKeyAndTheShortFormStillWork(t *testing.T) {
	up := stubCanvasAPI(t)
	root := t.TempDir()
	public := siteTree(t, root, "public")
	admin := siteTree(t, root, "admin")

	m := manifestRooted(Node{"name": "demo", "canvas": map[string]any{"sites": []any{
		public,
		map[string]any{"dir": admin, "route": "/admin"},
	}}}, root)

	if err := applyCanvas(m, io.Discard); err != nil {
		t.Fatalf("applyCanvas: %v", err)
	}

	got := up.snapshot()
	if got["default"] != "/" {
		t.Errorf("a bare site string mounts at the root, got %q", got["default"])
	}
	if got["admin"] != "/admin" {
		t.Errorf("a route-keyed site keeps its mount path, got %q", got["admin"])
	}
}
