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

// concurrencyElement writes one element directory in lang holding the handler
// each spec names, and returns it.
func concurrencyElement(t *testing.T, lang string, handlers ...string) string {
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

// Above 1 a Pulse must be Go, buffered and reached over HTTP. Each refusal
// happens in BuildElements, before anything is staged or built, carries the
// operator's own 400 text, and names the Pulse.
func TestBuildElements_RefusesAConcurrencyTheSliceCannotHonour(t *testing.T) {
	for name, tc := range map[string]struct {
		lang string
		spec FunctionSpec
		want string
	}{
		"a Python pulse": {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc", Concurrency: 4},
			`concurrency 4 needs a Go Pulse: a concurrent Pulse runs Go in this release, and this one is built as "python". ` +
				`Set concurrency to 1, or move the handler into a Go element`},
		"a streaming pulse": {"go", FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "sse", Concurrency: 2},
			`concurrency 2 cannot be combined with stream "sse": a concurrent Pulse serves buffered requests in this release. ` +
				`Set concurrency to 1, or drop the stream`},
		"a queue pulse": {"go", FunctionSpec{Name: "queue:orders", Handler: "HandleOrder", Concurrency: 8},
			`concurrency 8 cannot be combined with method queue: a queue Pulse is invoked one call at a time by its trigger. ` +
				`Set concurrency to 1`},
		"above the maximum": {"go", FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Concurrency: 65},
			`concurrency 65 is not usable: it must be between 1 and 64`},
	} {
		t.Run(name, func(t *testing.T) {
			tc.spec.Dir = concurrencyElement(t, tc.lang, tc.spec.Handler)
			_, err := BuildElements([]FunctionSpec{tc.spec})
			if err == nil {
				t.Fatalf("%s with concurrency %d was accepted", tc.spec.Name, tc.spec.Concurrency)
			}
			if !errors.Is(err, ErrConcurrency) {
				t.Errorf("the refusal does not answer errors.Is(err, ErrConcurrency), so `drift file apply` "+
					"would not report it before deploying anything: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal is not the operator's text:\n got %v\nwant %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.spec.Name) {
				t.Errorf("the refusal does not name the pulse: %v", err)
			}
		})
	}
}

// The controls: a concurrent buffered Go Pulse over HTTP builds, and so does
// every Pulse that declares none or 1, whatever its language, stream or method.
func TestBuildElements_AcceptsEveryConcurrencyTheSliceHonours(t *testing.T) {
	for name, tc := range map[string]struct {
		lang string
		spec FunctionSpec
	}{
		"Go at 16":           {"go", FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Concurrency: 16}},
		"Go at the maximum":  {"go", FunctionSpec{Name: "get:rpc", Handler: "GetRPC", Concurrency: MaxPulseConcurrency}},
		"Python undeclared":  {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc"}},
		"Python at 1":        {"python", FunctionSpec{Name: "post:rpc", Handler: "post_rpc", Concurrency: 1}},
		"a stream at 1":      {"go", FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "ws", Concurrency: 1}},
		"a queue undeclared": {"go", FunctionSpec{Name: "queue:orders", Handler: "HandleOrder"}},
	} {
		t.Run(name, func(t *testing.T) {
			tc.spec.Dir = concurrencyElement(t, tc.lang, tc.spec.Handler)
			if _, err := BuildElements([]FunctionSpec{tc.spec}); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// The CLI's bound is a copy of the platform's, published in the schema. The
// frozen copy of the schema this CLI implements is byte-identical to the one
// the platform serves, so pinning to it pins to the platform.
func TestMaxPulseConcurrencyIsTheSchemasMaximum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "project", "testdata", "migrate", "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions struct {
			AtomicEntry struct {
				Properties struct {
					Concurrency struct {
						Maximum int `json:"maximum"`
						Minimum int `json:"minimum"`
					} `json:"concurrency"`
				} `json:"properties"`
			} `json:"atomicEntry"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	c := doc.Definitions.AtomicEntry.Properties.Concurrency
	if c.Maximum != MaxPulseConcurrency || c.Minimum != 1 {
		t.Errorf("the schema bounds concurrency to %d..%d, this CLI to 1..%d", c.Minimum, c.Maximum, MaxPulseConcurrency)
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
	raw, err := os.ReadFile(filepath.Join(runner, slotDirName(a.Element, a.Method, a.Name), "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

// Both sinks carry `concurrency` above 1 and omit it otherwise, so a Pulse
// that declares none is sent exactly the metadata it always was.
func TestSinks_CarryConcurrencyOnlyAboveOne(t *testing.T) {
	for name, sink := range map[string]func(*testing.T, FuncArtifact) map[string]any{
		"operatorSink":  sentMetadata,
		"localSlotSink": placedMetadata,
	} {
		t.Run(name, func(t *testing.T) {
			art := FuncArtifact{Name: "rpc", Method: "post", Language: "go", Element: "evm"}
			for _, n := range []int{0, 1} {
				art.Concurrency = n
				if meta := sink(t, art); meta["protocol"] != "loop" {
					t.Fatalf("concurrency %d: not the Pulse metadata this test means: %v", n, meta)
				} else if v, present := meta["concurrency"]; present {
					t.Errorf("concurrency %d was sent as %v; a Pulse declaring none must carry no key", n, v)
				}
			}
			art.Concurrency = 16
			if got := sink(t, art)["concurrency"]; got != float64(16) {
				t.Errorf("concurrency 16 was sent as %v (%T), want the integer 16", got, got)
			}
		})
	}
}

// --- the digest --------------------------------------------------------------

// Digests taken from the code before `concurrency` existed. A declaration that
// does not declare it, or declares 1, must hash exactly as it did, or every
// function deployed so far redeploys on the next apply.
var goldenDeclarationDigests = []struct {
	spec FunctionSpec
	want string
}{
	{FunctionSpec{Name: "post:rpc", Handler: "PostRPC", Element: "evm", Auth: "apikey", Response: "raw",
		Secrets: []string{"B", "A"}, Env: map[string]string{"FXEVM_DEBUG_RPC": "1", "REGION": "eu"}},
		"de14e7d3d3c7ed7789872ae605cbbf2e4b62d5bdc1a327d9bee3fcb38eed1318"},
	{FunctionSpec{Name: "get:tick", Handler: "GetTick"},
		"c987a42786b18e01c7ae1702d04e9223b9a3dfb4673796baa93e6ef0d29e8676"},
	{FunctionSpec{Name: "get:events", Handler: "GetEvents", Stream: "sse"},
		"87bbc952b85252546242909f892087b55defbd078ecdedc931ae81df8392dfb4"},
	{FunctionSpec{Name: "field:sequencer", Handler: "Sequencer", Element: "evm"},
		"4674fc3c8dd8f7270c07a71ce5524e8b1661832663b705f620a38340e5a77dd9"},
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

// And a concurrency above 1 moves it, as does any change between two of them,
// so declaring or changing one is never skipped as unchanged.
func TestDeclarationDigest_MovesWithAConcurrencyAboveOne(t *testing.T) {
	base := FunctionSpec{Name: "post:rpc", Handler: "PostRPC"}
	seen := map[string]int{DeclarationDigest(base): 1}
	for _, n := range []int{2, 4, 16, 64} {
		spec := base
		spec.Concurrency = n
		d := DeclarationDigest(spec)
		if prev, dup := seen[d]; dup {
			t.Errorf("concurrency %d hashes like concurrency %d: the change would be skipped as unchanged", n, prev)
		}
		seen[d] = n
	}
}
