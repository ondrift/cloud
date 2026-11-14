package project

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveAtFileShape is the live slice a Driftfile already describes: a monthly
// slice holding exactly the shape the Driftfile in the working directory
// compiles to. Applied against it, a file resizes nothing, so a test reads what
// the deploy does and not what the resize does. The schema must be installed
// (withRealSchema) and the files the Driftfile names must exist.
func liveAtFileShape(t *testing.T) string {
	t.Helper()
	c, err := CompileDriftfile("Driftfile")
	if err != nil {
		t.Fatalf("compile the Driftfile the live slice is made from: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"name": c.Manifest.Name(), "tier": "custom", "config": c.Shape})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// runApplyAgreeingToRemove drives `drift apply` on the Driftfile in the working
// directory, with the price agreed at 0 and each of removes agreed to by its
// address, as `--approve-remove` would.
func runApplyAgreeingToRemove(t *testing.T, removes ...string) error {
	t.Helper()
	a := noAnswers()
	a.price = 0
	for _, r := range removes {
		a.removes[r] = true
	}
	return runApply(applyOptions{noEnvFile: true, answers: a, out: os.Stdout, in: strings.NewReader("")})
}

const helloHandler = "function GetHello(req) { return [200, 'OK', {}]; }\nmodule.exports = { GetHello };\n"

// orderDriftfile declares one Function that runs against a database and a queue
// the same file declares.
const orderDriftfile = `slice "demo" {}

atomic {
  backend "default" {
    function "GET /hello" {
      handler = "GetHello"
      memory  = "32MiB"
    }
  }
}

backbone {
  queue "txs" {}

  sql "ledger" {
    size   = "10MiB"
    schema = "schema.sql"
    seed   = "seed.sql"
  }
}
`

// orderProject lays out a project whose one Function runs against a database and a
// queue the same Driftfile declares, beside a slice stub that already holds the
// file's shape.
func orderProject(t *testing.T) *recorder {
	t.Helper()
	return orderProjectAnswering(t, nil)
}

// orderProjectAnswering is orderProject whose slice answers a request through
// answer first: a request answer returns true for is answered by it alone.
func orderProjectAnswering(t *testing.T, answer func(w http.ResponseWriter, r *http.Request) bool) *recorder {
	t.Helper()
	live := ""
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if answer != nil && answer(w, r) {
			return
		}
		switch r.URL.Path {
		case "/ops/slice":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/backbone/sql/admin/list", "/ops/atomic", "/ops/slice/domain", "/ops/atomic/alert":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case "/ops/backbone/sql/admin/load-schema", "/ops/backbone/sql/admin/load-seed", "/ops/backbone/queue/pop":
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	// After the stub, which gives the test a HOME of its own.
	withRealSchema(t)
	applyIn(t, orderDriftfile, "atomic")
	for path, body := range map[string]string{
		filepath.Join("atomic", "hello.js"): helloHandler,
		"schema.sql":                        "CREATE TABLE IF NOT EXISTS txs (id TEXT);",
		"seed.sql":                          "INSERT INTO txs VALUES ('genesis');",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	live = liveAtFileShape(t)
	return rec
}

// position is the index of the first recorded call equal to want, or -1.
func (r *recorder) position(want string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, h := range r.hits {
		if h == want {
			return i
		}
	}
	return -1
}

// BACKBONE BEFORE ATOMIC. A Worker starts the moment it is deployed and a Function
// can be called the moment it is, so the tables, the seed and the queues they
// use have to exist first. The apply ran Atomic, Backbone and Canvas together
// and the SQL schemas after all three, so a Worker's first query met no table.
func TestApply_AppliesBackboneBeforeAtomicDeploys(t *testing.T) {
	rec := orderProject(t)
	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}

	deploy := rec.position("POST /ops/atomic")
	if deploy < 0 {
		t.Fatal("apply never deployed the Function, so this proves nothing about order")
	}
	for _, want := range []string{
		"POST /ops/backbone/sql/admin/load-schema",
		"POST /ops/backbone/sql/admin/load-seed",
		"POST /ops/backbone/queue/push",
	} {
		at := rec.position(want)
		if at < 0 {
			t.Errorf("apply never made %s", want)
			continue
		}
		if at > deploy {
			t.Errorf("%s came after the Function was deployed (call %d, deploy at %d): "+
				"the Function could run before what it uses exists", want, at, deploy)
		}
	}
}

// A Backbone failure stops the apply before Atomic: deploying the Function anyway
// would start it against the very resource that just failed to apply.
func TestApply_ABackboneFailureDeploysNoAtomic(t *testing.T) {
	live := ""
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice":
			_, _ = w.Write([]byte(live))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/backbone/queue/push":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"disk full"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	withRealSchema(t)
	applyIn(t, `slice "demo" {}

atomic {
  backend "default" {
    function "GET /hello" {
      handler = "GetHello"
      memory  = "32MiB"
    }
  }
}

backbone {
  queue "txs" {}
}
`, "atomic")
	if err := os.WriteFile(filepath.Join("atomic", "hello.js"), []byte(helloHandler), 0o600); err != nil {
		t.Fatal(err)
	}
	live = liveAtFileShape(t)

	if err := applyAgreeingAtZero(t); err == nil || !strings.Contains(err.Error(), "txs") {
		t.Fatalf("a queue that failed to initialise must fail the apply naming it, got %v", err)
	}
	if n := rec.count("POST /ops/atomic"); n != 0 {
		t.Errorf("apply deployed %d Function(s) after Backbone failed", n)
	}
}

// Every section reads in order under its own heading. The backend deployers
// wrote each slot's line straight to stdout while the section around them was
// buffered, so the slots printed above the "Atomic" heading they belong to.
func TestApply_EverySlotLinePrintsUnderItsHeading(t *testing.T) {
	orderProject(t)
	out := captureStdout(t, func() {
		if err := applyAgreeingAtZero(t); err != nil {
			t.Errorf("apply: %v", err)
		}
	})

	lines := strings.Split(out, "\n")
	index := func(want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		return -1
	}
	backbone, atomic, slot := index("Backbone"), index("Atomic"), index("get:hello")
	schema := index("sql schema applied")
	if backbone < 0 || atomic < 0 || slot < 0 || schema < 0 {
		t.Fatalf("missing a heading or a line (backbone %d, atomic %d, slot %d, schema %d):\n%s",
			backbone, atomic, slot, schema, out)
	}
	if !(backbone < schema && schema < atomic && atomic < slot) {
		t.Errorf("want Backbone, its schema line, then Atomic, then the slot; got lines %d, %d, %d, %d:\n%s",
			backbone, schema, atomic, slot, out)
	}
}
