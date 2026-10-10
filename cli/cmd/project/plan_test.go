package project

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// fxevmLive is isrand/fxevm-testnode's shape as the platform stores it: the
// fixture's slots and limits, plus the free slice's placeholder storage it was
// created with.
const fxevmLive = `{"name":"fxevm-testnode","tier":"custom","monthly_cost_cents":668,"config":{
 "canvas":{"TotalMaxSizeInBytes":52428800},
 "atomic":{"MaxFunctionMemoryBytes":67108864,"MaxFunctionRuntimeInSeconds":30,"MaxNumberOfDeploymentsInHistory":1,
  "MaxNumberOfFunctions":5,"MaxNumberOfHoursForLogRetention":24,"MaxNumberOfRequestsPerMinute":60000,
  "MaxNumberOfScheduledJobs":1,"MaxStorageBytes":104857600,
  "functions":[
   {"memory_bytes":67108864,"method":"post","name":"post:rpc","route":"rpc"},
   {"memory_bytes":67108864,"method":"field","name":"field:sequencer","route":"sequencer"},
   {"memory_bytes":33554432,"method":"post","name":"post:admin/reset","route":"admin/reset"},
   {"memory_bytes":33554432,"method":"get","name":"get:bench","route":"bench"}]},
 "backbone":{"backup_retention_days":3,
  "blobs":{"MaxSizeInBytesEach":5242880,"buckets":{"bucket-01":5242880,"bucket-02":5242880}},
  "locks":{"MaxConcurrent":100000},
  "nosql":{"collections":{"collection-2":20971520,"default":20971520}},
  "queues":{"MaxDepthEach":1000,"queues":{"queue-1":500,"txs":1000}},
  "realtime":{"MaxConcurrentConnections":50},
  "secrets":{"MaxCount":5,"MaxSizeInBytesEach":1024},
  "sql":{"MaxDatabases":1,"databases":{"chain":268435456,"database-1":10485760}}},
 "deed":{"pocket":{"MaxSizeInBytesEach":16384},"vault":{"MaxEntriesPerUID":100,"MaxSizeInBytesEach":4096}},
 "labels":null}}`

// planStub answers every read a plan makes. live is the /ops/slice/get body,
// or "" for a slice that does not exist; deployed is /ops/atomic/list.
func planStub(t *testing.T, live, deployed string) *recorder {
	t.Helper()
	return stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			if live == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, live)
		case "/ops/atomic/list":
			_, _ = io.WriteString(w, deployed)
		case "/ops/canvas":
			_, _ = io.WriteString(w, `{"deployed":true,"sites":[{"slug":"default","route":"/"}]}`)
		case "/ops/backbone/secret/list":
			_, _ = io.WriteString(w, `[]`)
		case "/ops/atomic/auth":
			if r.URL.Query().Get("function") == "admin/reset" {
				_, _ = io.WriteString(w, `[{"method":"POST","path":"admin/reset","fingerprint":"...4321"}]`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		case "/ops/slice/auth":
			_, _ = io.WriteString(w, `{"enabled":true,"realm":"fxevm testnode","users":[{"name":"drift"}]}`)
		case "/ops/slice/price":
			// The memory line alone, at the platform's 3 cents a MiB: enough to
			// tell one shape's quote from another's.
			var body struct {
				Config SliceConfig `json:"config"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			total := 0
			for _, f := range body.Config.Atomic.Functions {
				total += f.MemoryBytes
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"monthly_cents": total / bytesPerMiB * 3})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// planIn runs `drift plan` in a copy of the fxevm fixture, with the file's
// write-only values set, and returns what it printed.
func planIn(t *testing.T, edit func(string) string) (string, error) {
	t.Helper()
	withRealSchema(t)
	dir := t.TempDir()
	src := filepath.Join(mustAbs(t, "testdata"), "fxevm")
	if err := copyTree(src, dir); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		path := filepath.Join(dir, "Driftfile")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(edit(string(raw))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("RESET_KEY", "reset-key-4321")
	t.Setenv("EXPLORER_PASS", "explorer-pass")
	t.Setenv("DRIFT_SLICE", "")

	return planOutput(t, "--no-env-file")
}

// THE read-only property. A plan reads the live slice and asks for a price,
// and issues nothing else that is not a GET: no create, no resize, no deploy,
// no secret, no key. Asserted on the recorder, over everything the stub saw.
func TestPlan_IssuesNoWriteButThePriceQuote(t *testing.T) {
	rec := planStub(t, fxevmLive, `[]`)
	if _, err := planIn(t, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	// It did its work: it read the slice and asked for the quote. Without this
	// a plan that returned before reading anything would pass.
	if rec.count("GET /ops/slice/get") == 0 || rec.count("POST /ops/slice/price") == 0 {
		t.Fatalf("the plan never read the slice or never asked for a price: %v", rec.hits)
	}
	for _, hit := range rec.hits {
		if strings.HasPrefix(hit, "GET ") || hit == "POST /ops/slice/price" {
			continue
		}
		t.Errorf("A PLAN ISSUED %q. It reads, asks for a quote, and writes nothing.", hit)
	}
}

// What the plan says about the testnode as it stands: the placeholder storage
// the free slice was created with is not in the file, so it is removed; the
// fifth slot it was sold goes; and the field nothing has deployed would deploy.
func TestPlan_ReportsTheRealDifferences(t *testing.T) {
	planStub(t, fxevmLive, `[{"function_name":"rpc","method":"post","digest":"aaaa1111"}]`)
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{
		"slice fxevm-testnode",
		"~ change",
		"- blobs bucket-01",
		"- nosql default",
		"- queue queue-1",
		"- sql database-1",
		"~ slots",
		"5 → 4",
		"code or declaration changed (digest aaaa",
		"not deployed yet: deploys",
		"  sql chain",
		"unchanged",
		"Restart   no",
		"Removes   sql database-1",
		"           blobs bucket-01",
		"unchanged (fingerprint matches $RESET_KEY)",
		"Data      could be lost: sql database-1 (removed)",
		"€5.76 a month, unchanged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"reset-key-4321", "explorer-pass"} {
		if strings.Contains(out, secret) {
			t.Errorf("THE PLAN PRINTED A SECRET VALUE:\n%s", out)
		}
	}
}

// A booking change is priced and restarts the slice, because the pod's memory
// limit follows the sum of the bookings.
func TestPlan_ABookingChangeIsPricedAndRestarts(t *testing.T) {
	planStub(t, fxevmLive, `[]`)
	out, err := planIn(t, func(s string) string {
		return strings.Replace(s, `concurrent = true
    memory     = "64MiB"`, `concurrent = true
    memory     = "128MiB"`, 1)
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{
		"~ pulse POST /rpc",
		"memory 64MiB → 128MiB",
		"€5.76 a month → €7.68 a month",
		"Restart   yes",
		"192MiB → 256MiB",
		"--approve-price 768",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
}

// A slice that does not exist is "would create", priced, with nothing to
// remove.
func TestPlan_ASliceThatDoesNotExistWouldBeCreated(t *testing.T) {
	rec := planStub(t, "", `[]`)
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{"+ create", "+ pulse POST /rpc", "books 64MiB", "+ sql chain", "€5.76 a month", "Removes   nothing", "no: the slice is created"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if rec.count("POST /ops/slice/price") != 1 {
		t.Errorf("a new slice is priced once, by the platform: %v", rec.hits)
	}
}

// The control for the diff: a live slice that IS the file has nothing to
// change. Without it, a diff that reported a change for everything would pass
// every test above.
func TestPlan_ASliceThatIsTheFileHasNothingToChange(t *testing.T) {
	withRealSchema(t)
	fixture := filepath.Join(mustAbs(t, "testdata"), "fxevm", "Driftfile")
	c, err := CompileDriftfile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := json.Marshal(map[string]any{"name": "fxevm-testnode", "tier": "custom", "config": c.Shape})

	// What the slice reports deployed is exactly what this file would deploy.
	els, err := atomic_cmd.BuildElements(AtomicSpecs(c.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	atomic_cmd.SetDeclaredSchedules(declaredSchedules(c.Manifest))
	var deployed []map[string]string
	for _, el := range els {
		digest, err := atomic_cmd.ElementDigest(el.Dir, el.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range el.Funcs {
			method, name := f.Spec.Wire()
			deployed = append(deployed, map[string]string{
				"function_name": name, "method": method, "digest": atomic_cmd.DeployDigest(digest, f.Spec),
			})
		}
	}
	list, _ := json.Marshal(deployed)

	planStub(t, string(live), string(list))
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{"unchanged\n", "Nothing to change.", "Removes   nothing", "Restart   no"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	for _, mark := range []string{"\n  + ", "\n  - ", "\n  ~ "} {
		if strings.Contains(out, mark) {
			t.Errorf("a slice that is the file shows a change (%q):\n%s", mark, out)
		}
	}
}

// A YAML Driftfile is refused, and the slice is not read.
func TestPlan_AYAMLDriftfileIsRefused(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/get" {
			_, _ = io.WriteString(w, `{"name":"demo","tier":"hacker","config":{}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	withRealSchema(t)
	applyIn(t, "slice: demo\ncanvas:\n  sites:\n    - ./site\n", "site")
	_, err := planOutput(t)
	if err == nil || !strings.Contains(err.Error(), "the Driftfile is HCL now") {
		t.Fatalf("a YAML Driftfile was not refused as YAML: %v", err)
	}
	if n := rec.count("GET /ops/slice/get"); n != 0 {
		t.Errorf("a refused file still read the slice (%d reads)", n)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// planOutput runs `drift plan` with args and returns what it printed.
func planOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := GetPlanCmd()
		cmd.SetArgs(args)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		err = cmd.Execute()
	})
	return out, err
}
