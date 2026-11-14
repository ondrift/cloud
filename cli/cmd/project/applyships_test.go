package project

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// `drift apply` is the only command that ships a function or a site. Two things
// follow, and both are pinned here against a stand-in for the slice, with the
// real build and upload paths in between:
//
//   - a function whose source changed is built and uploaded by the apply itself,
//     and one that did not is neither built nor uploaded on the next apply;
//   - a site whose files changed is uploaded by the apply itself, with the new
//     files in it.

// fakeSlice records what an apply sent: each function's last-deployed digest,
// which it reports back from GET /ops/atomic the way the operator does, and the
// files of each uploaded site.
type fakeSlice struct {
	mu       sync.Mutex
	digests  map[string]string // "get:hello" -> digest
	uploads  int               // POST /ops/atomic
	sites    []map[string]string
	sentMeta []map[string]any
}

func (f *fakeSlice) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ops/atomic":
			records := []map[string]any{}
			for key, digest := range f.digests {
				method, name := splitKey(key)
				records = append(records, map[string]any{"function_name": name, "method": method, "digest": digest})
			}
			_ = json.NewEncoder(w).Encode(records)
		case r.Method == http.MethodPost && r.URL.Path == "/ops/atomic":
			meta := readMetadata(t, r)
			f.uploads++
			f.sentMeta = append(f.sentMeta, meta)
			f.digests[meta["method"].(string)+":"+meta["name"].(string)] = meta["digest"].(string)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/ops/canvas":
			body, _ := io.ReadAll(r.Body)
			f.sites = append(f.sites, unzip(t, body))
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}
}

func splitKey(key string) (method, name string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

func readMetadata(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("the upload is not multipart: %v", err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("the upload carries no metadata part: %v", err)
		}
		if part.FormName() != "metadata" {
			continue
		}
		raw, _ := io.ReadAll(part)
		var meta map[string]any
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("metadata is not JSON: %v", err)
		}
		return meta
	}
}

func unzip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the site upload is not a zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		raw, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(raw)
	}
	return files
}

func (f *fakeSlice) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads
}

const shipsDriftfile = `slice "ships" {}

atomic {
  backend "default" {
    function "GET /hello" {
      handler = "get_hello"
      memory  = "32MiB"
    }
  }
}

canvas {
  site "/" {
    dir = "site"
  }
}
`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyShipsAFunctionWhoseSourceChangedAndSkipsOneThatDidNot(t *testing.T) {
	withRealSchema(t)
	slice := &fakeSlice{digests: map[string]string{}}
	stubAPI(t, slice.handler(t))

	path := writeDriftfile(t, shipsDriftfile)
	root := filepath.Dir(path)
	hello := filepath.Join(root, "atomic", "hello.py")
	writeFile(t, hello, "def get_hello(req):\n    return 200, 'OK', {'v': 1}\n")
	writeFile(t, filepath.Join(root, "site", "index.html"), "<!doctype html>one")

	apply := func() {
		t.Helper()
		m, err := ParseDriftfile(path)
		if err != nil {
			t.Fatalf("parsing the Driftfile: %v", err)
		}
		if err := applyAtomic(m, io.Discard); err != nil {
			t.Fatalf("applyAtomic: %v", err)
		}
	}

	apply()
	if got := slice.uploadCount(); got != 1 {
		t.Fatalf("the first apply uploaded %d functions, want the one it declares", got)
	}
	if meta := slice.sentMeta[0]; meta["name"] != "hello" || meta["method"] != "get" {
		t.Errorf("the upload is %v, want GET hello", meta)
	}

	apply()
	if got := slice.uploadCount(); got != 1 {
		t.Errorf("an apply with nothing changed uploaded again (%d uploads in all): the function is rebuilt for nothing", got)
	}

	writeFile(t, hello, "def get_hello(req):\n    return 200, 'OK', {'v': 2}\n")
	apply()
	if got := slice.uploadCount(); got != 2 {
		t.Errorf("an apply after the source changed made %d uploads in all, want 2: the new source never shipped", got)
	}
	if first, last := slice.sentMeta[0]["digest"], slice.sentMeta[1]["digest"]; first == last {
		t.Errorf("the changed source went up under the old digest %v", first)
	}
}

func TestApplyUploadsASiteWithTheFilesItHasNow(t *testing.T) {
	withRealSchema(t)
	slice := &fakeSlice{digests: map[string]string{}}
	stubAPI(t, slice.handler(t))

	path := writeDriftfile(t, shipsDriftfile)
	root := filepath.Dir(path)
	writeFile(t, filepath.Join(root, "atomic", "hello.py"), "def get_hello(req):\n    return 200, 'OK', {}\n")
	index := filepath.Join(root, "site", "index.html")
	writeFile(t, index, "<!doctype html>one")

	apply := func() {
		t.Helper()
		m, err := ParseDriftfile(path)
		if err != nil {
			t.Fatalf("parsing the Driftfile: %v", err)
		}
		if err := applyCanvas(m, io.Discard); err != nil {
			t.Fatalf("applyCanvas: %v", err)
		}
	}

	apply()
	writeFile(t, index, "<!doctype html>two")
	writeFile(t, filepath.Join(root, "site", "about.html"), "about")
	apply()

	if len(slice.sites) != 2 {
		t.Fatalf("two applies uploaded %d sites, want 2", len(slice.sites))
	}
	if got := slice.sites[0]["index.html"]; got != "<!doctype html>one" {
		t.Errorf("the first upload held %q", got)
	}
	last := slice.sites[1]
	if last["index.html"] != "<!doctype html>two" || last["about.html"] != "about" {
		t.Errorf("the apply after the files changed uploaded %v, want the new index and the new page", last)
	}
}
