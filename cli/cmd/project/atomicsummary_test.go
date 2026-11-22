package project

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// threeFuncDriftfile declares three Functions in one default (Node) backend,
// so a refusal of the middle one proves both halves of the summary: what
// landed before it, and what a run never reached after it.
const threeFuncDriftfile = `slice "demo" {}

atomic {
  backend "default" {
    function "GET /a" {
      handler = "GetA"
      memory  = "32MiB"
    }
    function "GET /b" {
      handler = "GetB"
      memory  = "32MiB"
    }
    function "GET /c" {
      handler = "GetC"
      memory  = "32MiB"
    }
  }
}
`

const threeFuncHandlers = "function GetA(req) { return [200, 'OK', {}]; }\n" +
	"function GetB(req) { return [200, 'OK', {}]; }\n" +
	"function GetC(req) { return [200, 'OK', {}]; }\n" +
	"module.exports = { GetA, GetB, GetC };\n"

// drift apply stops at the first function the platform refuses, and the error
// it returns says which functions of this run already landed and which it
// never attempted, and that Backbone was already applied.
func TestApply_StopsAtTheFirstRefusedFunctionAndReportsWhatLanded(t *testing.T) {
	var mu sync.Mutex
	deployCalls := 0
	live := ""
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/atomic":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			mu.Lock()
			deployCalls++
			n := deployCalls
			mu.Unlock()
			// Refuse the SECOND function shipped: GetB, deployed after GetA.
			if n == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"the runner could not be reached"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case "/ops/backbone/sql/admin/list", "/ops/slice/domain", "/ops/atomic/alert":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	_ = rec
	withRealSchema(t)
	applyIn(t, threeFuncDriftfile, "atomic")
	if err := os.WriteFile(filepath.Join("atomic", "api.js"), []byte(threeFuncHandlers), 0o600); err != nil {
		t.Fatal(err)
	}
	live = liveAtFileShape(t)

	err := applyAgreeingAtZero(t)
	if err == nil {
		t.Fatal("apply must fail when a function is refused, and exit non-zero as every command's error does")
	}
	got := err.Error()

	for _, want := range []string{
		"Backbone was already applied.",
		"Deployed this run:",
		"Not attempted:",
		"get:a",
		"get:c",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Deployed this run: none") {
		t.Errorf("GetA landed before GetB was refused, so this run deployed something:\n%s", got)
	}
	if strings.Contains(got, "Not attempted: none") {
		t.Errorf("GetC was never attempted once GetB was refused:\n%s", got)
	}

	if !strings.Contains(got, "Deployed this run: get:a") {
		t.Errorf("want get:a named as deployed:\n%s", got)
	}
	if !strings.Contains(got, "Not attempted: get:c") {
		t.Errorf("want get:c named as not attempted, and nothing else:\n%s", got)
	}
}
