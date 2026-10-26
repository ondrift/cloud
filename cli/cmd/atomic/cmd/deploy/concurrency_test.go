package atomic_cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// --- the refusal -------------------------------------------------------------

// concurrencyBackend writes one backend directory in lang holding the handler
// each spec names, and returns it.
func concurrencyBackend(t *testing.T, lang string, handlers ...string) string {
	t.Helper()
	dir := t.TempDir()
	var src, file string
	switch lang {
	case "go":
		file, src = "api.go", "package main\n\n"
		for _, h := range handlers {
			src += "func " + h + "(body any, req any) {}\n"
		}
	case "python":
		file = "api.py"
		for _, h := range handlers {
			src += "def " + h + "(body, req):\n    return {}\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A concurrent Function, declared either way, must be Go, buffered and reached
// over HTTP. Each refusal happens in BuildBackends, before anything is staged
// or built, carries the operator's own 400 text naming the declaration as it
// was written, and names the Function.
func TestBuildBackends_RefusesAConcurrencyTheSliceCannotHonour(t *testing.T) {
	for name, tc := range map[string]struct {
		lang string
		spec FunctionSpec
		want string
	}{
		"a Python function": {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc", Concurrent: true},
			`concurrent: true needs a Go Function: a concurrent Function runs Go in this release, and this one is built as "python". ` +
				`Set concurrent to false, or move the handler into a Go backend`},
		"a streaming function": {"go", FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "sse", Concurrent: true},
			`concurrent: true cannot be combined with stream "sse": a concurrent Function serves buffered requests in this release. ` +
				`Set concurrent to false, or drop the stream`},
		"a queue function": {"go", FunctionSpec{Name: "queue:orders", Handler: "HandleOrder", Concurrent: true},
			`concurrent: true cannot be combined with method queue: a queue Function is invoked one call at a time by its trigger. ` +
				`Set concurrent to false`},
		"a Python function, the deprecated spelling": {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc", Concurrency: 4},
			`concurrency: 4 needs a Go Function: a concurrent Function runs Go in this release`},
		"a queue function, the deprecated spelling": {"go", FunctionSpec{Name: "queue:orders", Handler: "HandleOrder", Concurrency: 8},
			`concurrency: 8 cannot be combined with method queue`},
		"above the deprecated maximum": {"go", FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Concurrency: 65},
			`concurrency 65 is not usable: it must be between 1 and 64`},
	} {
		t.Run(name, func(t *testing.T) {
			tc.spec.Dir = concurrencyBackend(t, tc.lang, tc.spec.Handler)
			_, err := BuildBackends([]FunctionSpec{tc.spec})
			if err == nil {
				t.Fatalf("%s declared concurrent (%+v) was accepted", tc.spec.Name, tc.spec)
			}
			if !errors.Is(err, ErrConcurrency) {
				t.Errorf("the refusal does not answer errors.Is(err, ErrConcurrency), so `drift apply` "+
					"would not report it before deploying anything: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal is not the operator's text:\n got %v\nwant %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.spec.Name) {
				t.Errorf("the refusal does not name the function: %v", err)
			}
		})
	}
}

// The controls: a concurrent buffered Go Function over HTTP builds in either
// spelling, and so does every Function that is not concurrent, whatever its
// language, stream or method.
func TestBuildBackends_AcceptsEveryConcurrencyTheSliceHonours(t *testing.T) {
	for name, tc := range map[string]struct {
		lang string
		spec FunctionSpec
	}{
		"Go, concurrent":          {"go", FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Concurrent: true}},
		"Go, the deprecated 16":   {"go", FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Concurrency: 16}},
		"Go at the old maximum":   {"go", FunctionSpec{Name: "get:rpc", Handler: "GetRPC", Concurrency: MaxFunctionConcurrency}},
		"Python undeclared":       {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc"}},
		"Python at 1":             {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc", Concurrency: 1}},
		"a stream at 1":           {"go", FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "ws", Concurrency: 1}},
		"a queue undeclared":      {"go", FunctionSpec{Name: "queue:orders", Handler: "HandleOrder"}},
		"a stream not concurrent": {"go", FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "sse"}},
	} {
		t.Run(name, func(t *testing.T) {
			tc.spec.Dir = concurrencyBackend(t, tc.lang, tc.spec.Handler)
			if _, err := BuildBackends([]FunctionSpec{tc.spec}); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// The schema the platform serves, which this machine holds (CI fetches it
// before the suite runs), carries `concurrent` as a boolean. Format 1 also
// published the deprecated count with a bound, and while a schema still does,
// the CLI's bound is a copy of it; format 2 publishes no count, since the
// compiled document never carries one.
func TestMaxFunctionConcurrencyIsTheSchemasMaximum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".drift", "driftfile.schema.json"))
	if err != nil {
		t.Fatalf("no platform schema at ~/.drift/driftfile.schema.json (run any online drift command once): %v", err)
	}
	var doc struct {
		Definitions struct {
			AtomicEntry struct {
				Properties struct {
					Concurrent struct {
						Type string `json:"type"`
					} `json:"concurrent"`
					Concurrency struct {
						Maximum    int  `json:"maximum"`
						Minimum    int  `json:"minimum"`
						Deprecated bool `json:"deprecated"`
					} `json:"concurrency"`
				} `json:"properties"`
			} `json:"atomicEntry"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if c := doc.Definitions.AtomicEntry.Properties.Concurrency; c.Maximum != 0 {
		if c.Maximum != MaxFunctionConcurrency || c.Minimum != 1 {
			t.Errorf("the schema bounds concurrency to %d..%d, this CLI to 1..%d", c.Minimum, c.Maximum, MaxFunctionConcurrency)
		}
		if !c.Deprecated {
			t.Error("the schema publishes concurrency and does not mark it deprecated")
		}
	}
	if got := doc.Definitions.AtomicEntry.Properties.Concurrent.Type; got != "boolean" {
		t.Errorf("the schema this CLI implements has concurrent as %q, want a boolean", got)
	}
}

// --- the metadata ------------------------------------------------------------

// sentMetadata runs operatorSink against a stand-in API and returns the
// metadata part it posted.
func sentMetadata(t *testing.T, a FuncArtifact) map[string]any {
	t.Helper()
	var meta map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first API contact also refreshes the Driftfile schema.
		if r.URL.Path != "/ops/atomic" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		if err := json.Unmarshal([]byte(r.FormValue("metadata")), &meta); err != nil {
			t.Errorf("metadata is not JSON: %v", err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatal(err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })

	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.SourcePath = bin
	if err := operatorSink(a); err != nil {
		t.Fatalf("operatorSink: %v", err)
	}
	return meta
}

// placedMetadata runs localSlotSink and returns the metadata.json it wrote.
func placedMetadata(t *testing.T, a FuncArtifact) map[string]any {
	t.Helper()
	runner := t.TempDir()
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.SourcePath = bin
	if err := localSlotSink(runner)(a); err != nil {
		t.Fatalf("localSlotSink: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(runner, slotDirName(a.Backend, a.Method, a.Name), "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

// Both sinks carry `concurrent: true` for a concurrent Function and omit it
// otherwise, so a Function that is not concurrent is sent exactly the metadata it
// always was. Neither ever sends a count: the slice decides the number.
func TestSinks_CarryConcurrentOnlyWhenTrue(t *testing.T) {
	for name, sink := range map[string]func(*testing.T, FuncArtifact) map[string]any{
		"operatorSink":  sentMetadata,
		"localSlotSink": placedMetadata,
	} {
		t.Run(name, func(t *testing.T) {
			art := FuncArtifact{Name: "rpc", Method: "post", Language: "go", Backend: "evm"}
			meta := sink(t, art)
			if meta["protocol"] != "loop" {
				t.Fatalf("not the Function metadata this test means: %v", meta)
			}
			for _, key := range []string{"concurrent", "concurrency"} {
				if v, present := meta[key]; present {
					t.Errorf("a Function that is not concurrent was sent %s = %v", key, v)
				}
			}
			art.Concurrent = true
			meta = sink(t, art)
			if got := meta["concurrent"]; got != true {
				t.Errorf("a concurrent Function was sent concurrent = %v (%T), want true", got, got)
			}
			if v, present := meta["concurrency"]; present {
				t.Errorf("a concurrent Function was sent a count, %v: the slice decides the number", v)
			}
		})
	}
}

// --- the digest --------------------------------------------------------------

// Digests taken from the code before `concurrency` existed. A declaration that
// is not concurrent must hash exactly as it did, or every function deployed so
// far redeploys on the next apply.
var goldenDeclarationDigests = []struct {
	spec FunctionSpec
	want string
}{
	{FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Backend: "evm", Auth: "apikey", Response: "raw",
		Secrets: []string{"B", "A"}, Env: map[string]string{"FXEVM_DEBUG_RPC": "1", "REGION": "eu"}},
		"5632412ac7670241c3a22f68514b7b21d74ea784993feb929e4e5cbec2291371"},
	{FunctionSpec{Name: "get:tick", Handler: "GetTick"},
		"13b41bccc1837a9217f1570687087f09ea6e43d625bf0d894bf8b95645577d63"},
	{FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "sse"},
		"fb8fa1b5452a45774d2d60ace35e8f78f0ba71fca9dbd23a9d2892fee53ad281"},
	{FunctionSpec{Name: "worker:sequencer", Handler: "Sequencer", Backend: "evm"},
		"c6bf27202716cb95ccf9e5777a370deb6671b9a39f241012130a54795bf8d886"},
}

func TestDeclarationDigest_IsUnchangedForEveryExistingDeclaration(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"get:tick": "*/5 * * * *"})
	for _, g := range goldenDeclarationDigests {
		for _, n := range []int{0, 1} {
			spec := g.spec
			spec.Concurrency = n
			if got := DeclarationDigest(spec); got != g.want {
				t.Errorf("%s at concurrency %d hashes %s, and hashed %s before the key existed: "+
					"every function like it would redeploy", spec.Name, n, got, g.want)
			}
		}
	}
}

// Declaring a Function concurrent moves its digest, so the change is never
// skipped as unchanged; and the deprecated count hashes as `concurrent: true`
// whatever its number, because the number is neither sent nor used.
func TestDeclarationDigest_MovesWhenAFunctionIsDeclaredConcurrent(t *testing.T) {
	base := FunctionSpec{Name: "post:rpc", Handler: "PostRPC"}
	concurrent := base
	concurrent.Concurrent = true
	want := DeclarationDigest(concurrent)
	if want == DeclarationDigest(base) {
		t.Fatal("declaring the Function concurrent leaves its digest unchanged: the change would be skipped")
	}
	for _, n := range []int{2, 16, 64} {
		spec := base
		spec.Concurrency = n
		if got := DeclarationDigest(spec); got != want {
			t.Errorf("concurrency %d hashes %s, concurrent: true hashes %s: the same declaration would "+
				"redeploy when its spelling changes", n, got, want)
		}
	}
}
