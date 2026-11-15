package project

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Seeding makes one call, a write for each record, and a write the platform
// refuses fails the apply through the renderer: the code line, and the platform's
// own reason. It does not print the status and the body it got.
func TestNoSQLSeedJSONL_ARefusedWriteFailsThroughTheRenderer(t *testing.T) {
	var writes atomic.Int32
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/ops/backbone/write" && writes.Add(1) == 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"the collection could not be written"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	})
	path := filepath.Join(t.TempDir(), "menu.jsonl")
	mustWrite(t, path, menuSeed)

	n, err := nosqlSeedJSONL("menu", path)

	if err == nil {
		t.Fatal("a seed the platform refused to write was reported as seeded")
	}
	if n != 2 {
		t.Errorf("seeding wrote %d records before the refused one, want 2", n)
	}
	for _, want := range []string{
		"Couldn't seed nosql doc",
		"the collection could not be written",
		"DRIFT-1005 · drift doctor explain DRIFT-1005",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("the failure prints the status and the body it got:\n%v", err)
	}
}
