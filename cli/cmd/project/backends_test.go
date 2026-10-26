package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// backendProject writes a Driftfile beside three Go backends and returns its
// path: `evm` and `bench` under ./atomic, and the default backend's flat files
// directly in ./atomic.
func backendProject(t *testing.T, driftfile string) string {
	t.Helper()
	withRealSchema(t)
	root := t.TempDir()
	sources := map[string]string{
		"atomic/main.go": "package main\n\nfunc GetPing() {}\n",
		"atomic/evm/main.go": "package main\n\nimport \"context\"\n\n" +
			"func PostRPC() {}\n\nfunc PostReset() {}\n\n" +
			"func Sequencer(ctx context.Context) error { <-ctx.Done(); return nil }\n",
		"atomic/bench/main.go": "package main\n\nfunc GetBench() {}\n",
	}
	for rel, src := range sources {
		mustWrite(t, filepath.Join(root, rel), src)
	}
	path := filepath.Join(root, "Driftfile")
	mustWrite(t, path, driftfile)
	return path
}

// The fxevm testnode: two backends holding their Functions and a Worker.
const nestedDriftfile = `slice "fxevm-testnode" {}

atomic {
  backend "evm" {
    function "POST /rpc" {
      handler  = "PostRPC"
      response = "json"
      memory   = "64MiB"
    }
    function "POST /admin/reset" {
      handler  = "PostReset"
      response = "json"
      memory   = "32MiB"
      auth {
        api_key = env("RESET_KEY")
      }
    }
    worker "sequencer" {
      handler = "Sequencer"
      memory  = "64MiB"
    }
  }

  backend "bench" {
    function "GET /bench" {
      handler  = "GetBench"
      response = "json"
      memory   = "32MiB"
    }
  }
}
`

// specSummary renders a spec as name@backend in dir, relative to root.
func specSummary(t *testing.T, root string, s atomic_cmd.FunctionSpec) string {
	t.Helper()
	rel, err := filepath.Rel(root, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.Name + "@" + s.Backend + " in " + filepath.ToSlash(rel)
}

// Every Function and Worker declared inside a backend reaches the flat model every
// reader consumes: its booking key, its backend, and the backend's directory.
// The slots are booked as `post:rpc` and `worker:sequencer`, and the backends
// build from their own folders.
func TestBackends_NormaliseIntoTheFlatModel(t *testing.T) {
	path := backendProject(t, nestedDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the testnode must parse:\n%v", err)
	}
	root := filepath.Dir(path)

	var got []string
	for _, s := range AtomicSpecs(m) {
		got = append(got, specSummary(t, root, s))
	}
	want := []string{
		"post:rpc@evm in atomic/evm",
		"post:admin/reset@evm in atomic/evm",
		"get:bench@bench in atomic/bench",
		"worker:sequencer@evm in atomic/evm",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("specs:\n got: %q\nwant: %q", got, want)
	}
	if m.Slice().Has("atomic", "backends") {
		t.Errorf("the resolved slice still carries atomic.backends, so a reader could count its entries twice")
	}

	els, err := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if err != nil {
		t.Fatalf("the backends must build:\n%v", err)
	}
	var names []string
	for _, el := range els {
		names = append(names, el.Name+"="+el.Lang)
	}
	if strings.Join(names, ",") != "bench=go,evm=go" {
		t.Errorf("backends built: %v, want bench and evm, both Go", names)
	}
}

// A slot's own `env` and `secrets` reach its spec, and its own `dir` overrides
// its backend's. The default backend's slots belong to no named backend and
// build from ./atomic itself.
func TestBackends_ASlotsOwnDirEnvAndSecrets(t *testing.T) {
	path := backendProject(t, `slice "demo" {}
atomic {
  backend "default" {
    function "GET /ping" {
      handler = "GetPing"
      memory  = "16MiB"
    }
  }
  backend "evm" {
    function "POST /rpc" {
      handler = "PostRPC"
      memory  = "64MiB"
      env     = { CHAIN_ID = "31337", MODE = "function" }
      secrets = ["RPC_TOKEN", "ADMIN_KEY"]
    }
  }
  backend "bench" {
    function "GET /bench" {
      handler = "GetBench"
      memory  = "32MiB"
      dir     = "atomic/evm"
    }
  }
}
`)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse:\n%v", err)
	}
	root := filepath.Dir(path)

	byName := map[string]atomic_cmd.FunctionSpec{}
	for _, s := range AtomicSpecs(m) {
		byName[s.Name] = s
	}

	ping := byName["get:ping"]
	if ping.Backend != "" || specSummary(t, root, ping) != "get:ping@ in atomic" || len(ping.Env) != 0 || len(ping.Secrets) != 0 {
		t.Errorf("the default backend's Function belongs to no named backend, with nothing of evm's: %+v", ping)
	}
	rpc := byName["post:rpc"]
	if rpc.Env["CHAIN_ID"] != "31337" || rpc.Env["MODE"] != "function" || strings.Join(rpc.Secrets, ",") != "RPC_TOKEN,ADMIN_KEY" {
		t.Errorf("the Function's own env and secrets: %+v", rpc)
	}
	if bench := byName["get:bench"]; filepath.Base(bench.Dir) != "evm" {
		t.Errorf("a slot's own dir must override its backend's, got %s", bench.Dir)
	}
}

// What the schema cannot express is refused across the whole Driftfile, each
// naming what to change.
func TestBackends_ValidationAcrossTheWholeDriftfile(t *testing.T) {
	const head = "slice \"demo\" {}\n"
	function := func(label, handler string) string {
		return "  function \"" + label + "\" {\n    handler = \"" + handler + "\"\n    memory = \"32MiB\"\n  }\n"
	}
	worker := func(name string) string {
		return "  worker \"" + name + "\" {\n    handler = \"Sequencer\"\n    memory = \"64MiB\"\n  }\n"
	}
	backend := func(name, body string) string { return "backend \"" + name + "\" {\n" + body + "}\n" }
	atomicOf := func(backends ...string) string { return "atomic {\n" + strings.Join(backends, "") + "}\n" }

	cases := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "a backend declared twice",
			doc:  head + atomicOf(backend("evm", function("POST /rpc", "PostRPC")), backend("evm", worker("sequencer"))),
			want: []string{`backend "evm" is declared twice`, "declared once"},
		},
		{
			name: "a route declared in two backends",
			doc:  head + atomicOf(backend("evm", function("POST /rpc", "PostRPC")), backend("bench", function("POST /rpc", "GetBench"))),
			want: []string{`the function "post:rpc" is declared 2 times (in the backends evm and bench)`, "the whole slice"},
		},
		{
			name: "a route in the default backend and a named one",
			doc:  head + atomicOf(backend("default", function("POST /rpc", "GetPing")), backend("evm", function("POST /rpc", "PostRPC"))),
			want: []string{`the function "post:rpc" is declared 2 times (in the backends default and evm)`},
		},
		{
			name: "a Worker name declared in two backends",
			doc:  head + atomicOf(backend("evm", worker("sequencer")), backend("bench", worker("sequencer"))),
			want: []string{`the worker "sequencer" is declared 2 times (in the backends evm and bench)`},
		},
		{
			name: "a Worker named like a Function route in another backend",
			doc:  head + atomicOf(backend("evm", worker("bench")), backend("bench", function("GET /bench", "GetBench"))),
			want: []string{`the Worker "bench" and the Function route "bench" would share logs and metrics; rename one`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDriftfile(backendProject(t, tc.doc))
			if err == nil {
				t.Fatal("the file must be refused")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal must say %q, got:\n%v", w, err)
				}
			}
			if strings.Contains(err.Error(), "—") {
				t.Errorf("the refusal carries an em dash:\n%v", err)
			}
		})
	}

	// The control: the default backend beside a named one, each with its own
	// slots, parses.
	ok := head + atomicOf(backend("default", function("GET /ping", "GetPing")), backend("evm", function("POST /rpc", "PostRPC")))
	if _, err := ParseDriftfile(backendProject(t, ok)); err != nil {
		t.Errorf("must parse:\n%s\n%v", ok, err)
	}
}

// A Worker in a backend written in anything but Go is refused before building.
func TestBackends_AWorkerInANonGoBackendIsRefused(t *testing.T) {
	path := backendProject(t, nestedDriftfile)
	evm := filepath.Join(filepath.Dir(path), "atomic", "evm")
	if err := os.Remove(filepath.Join(evm, "main.go")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(evm, "handlers.js"),
		"function PostRPC() {}\nfunction PostReset() {}\nasync function Sequencer() {}\n"+
			"module.exports = { PostRPC, PostReset, Sequencer };\n")
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, berr := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if berr == nil || !strings.Contains(berr.Error(), `backend "evm"`) ||
		!strings.Contains(berr.Error(), "a Worker runs Go in this release") {
		t.Errorf("a Worker in a node backend must be refused naming the backend, got %v", berr)
	}
}

// The backends' slots are what every downstream reader counts: the reference
// check, the schedule registry and the slot count see every backend's entries.
func TestBackends_DownstreamReadersSeeEverySlot(t *testing.T) {
	doc := strings.Replace(nestedDriftfile, "    handler  = \"GetBench\"\n",
		"    handler  = \"GetBench\"\n    cron     = \"*/5 * * * *\"\n", 1)
	m, err := ParseDriftfile(backendProject(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := CountAtomicFunctions(m); n != 4 {
		t.Errorf("CountAtomicFunctions = %d, want 4", n)
	}
	if n, _ := CountScheduledFunctions(m); n != 1 {
		t.Errorf("CountScheduledFunctions = %d, want 1", n)
	}
	named := namedFunctions(m)
	sort.Strings(named)
	if strings.Join(named, ",") != "worker:sequencer,get:bench,post:admin/reset,post:rpc" {
		t.Errorf("namedFunctions = %v", named)
	}
}
