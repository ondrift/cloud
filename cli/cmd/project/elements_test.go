package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// elementProject writes a Driftfile beside three Go elements and returns its
// path: `evm` and `bench` under ./atomic, and the default element's flat files
// directly in ./atomic.
func elementProject(t *testing.T, driftfile string) string {
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

// The fxevm testnode: two elements holding their Pulses and a Field.
const nestedDriftfile = `slice "fxevm-testnode" { billing = "monthly" }

atomic {
  element "evm" {
    pulse "POST /rpc" {
      handler  = "PostRPC"
      response = "json"
      memory   = "64MiB"
    }
    pulse "POST /admin/reset" {
      handler  = "PostReset"
      response = "json"
      memory   = "32MiB"
      auth {
        api_key = env("RESET_KEY")
      }
    }
    field "sequencer" {
      handler = "Sequencer"
      memory  = "64MiB"
    }
  }

  element "bench" {
    pulse "GET /bench" {
      handler  = "GetBench"
      response = "json"
      memory   = "32MiB"
    }
  }
}
`

// specSummary renders a spec as name@element in dir, relative to root.
func specSummary(t *testing.T, root string, s atomic_cmd.FunctionSpec) string {
	t.Helper()
	rel, err := filepath.Rel(root, s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.Name + "@" + s.Element + " in " + filepath.ToSlash(rel)
}

// Every Pulse and Field declared inside an element reaches the flat model every
// reader consumes: its booking key, its element, and the element's directory.
// The slots are booked as `post:rpc` and `field:sequencer`, and the elements
// build from their own folders.
func TestElements_NormaliseIntoTheFlatModel(t *testing.T) {
	path := elementProject(t, nestedDriftfile)
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
		"field:sequencer@evm in atomic/evm",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("specs:\n got: %q\nwant: %q", got, want)
	}
	if m.Slice().Has("atomic", "elements") {
		t.Errorf("the resolved slice still carries atomic.elements, so a reader could count its entries twice")
	}

	els, err := atomic_cmd.BuildElements(AtomicSpecs(m))
	if err != nil {
		t.Fatalf("the elements must build:\n%v", err)
	}
	var names []string
	for _, el := range els {
		names = append(names, el.Name+"="+el.Lang)
	}
	if strings.Join(names, ",") != "bench=go,evm=go" {
		t.Errorf("elements built: %v, want bench and evm, both Go", names)
	}
}

// A slot's own `env` and `secrets` reach its spec, and its own `dir` overrides
// its element's. The default element's slots belong to no named element and
// build from ./atomic itself.
func TestElements_ASlotsOwnDirEnvAndSecrets(t *testing.T) {
	path := elementProject(t, `slice "demo" { billing = "monthly" }
atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "GetPing"
      memory  = "16MiB"
    }
  }
  element "evm" {
    pulse "POST /rpc" {
      handler = "PostRPC"
      memory  = "64MiB"
      env     = { CHAIN_ID = "31337", MODE = "pulse" }
      secrets = ["RPC_TOKEN", "ADMIN_KEY"]
    }
  }
  element "bench" {
    pulse "GET /bench" {
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
	if ping.Element != "" || specSummary(t, root, ping) != "get:ping@ in atomic" || len(ping.Env) != 0 || len(ping.Secrets) != 0 {
		t.Errorf("the default element's Pulse belongs to no named element, with nothing of evm's: %+v", ping)
	}
	rpc := byName["post:rpc"]
	if rpc.Env["CHAIN_ID"] != "31337" || rpc.Env["MODE"] != "pulse" || strings.Join(rpc.Secrets, ",") != "RPC_TOKEN,ADMIN_KEY" {
		t.Errorf("the Pulse's own env and secrets: %+v", rpc)
	}
	if bench := byName["get:bench"]; filepath.Base(bench.Dir) != "evm" {
		t.Errorf("a slot's own dir must override its element's, got %s", bench.Dir)
	}
}

// What the schema cannot express is refused across the whole Driftfile, each
// naming what to change.
func TestElements_ValidationAcrossTheWholeDriftfile(t *testing.T) {
	const head = "slice \"demo\" { billing = \"monthly\" }\n"
	pulse := func(label, handler string) string {
		return "  pulse \"" + label + "\" {\n    handler = \"" + handler + "\"\n    memory = \"32MiB\"\n  }\n"
	}
	field := func(name string) string {
		return "  field \"" + name + "\" {\n    handler = \"Sequencer\"\n    memory = \"64MiB\"\n  }\n"
	}
	element := func(name, body string) string { return "element \"" + name + "\" {\n" + body + "}\n" }
	atomicOf := func(elements ...string) string { return "atomic {\n" + strings.Join(elements, "") + "}\n" }

	cases := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "an element declared twice",
			doc:  head + atomicOf(element("evm", pulse("POST /rpc", "PostRPC")), element("evm", field("sequencer"))),
			want: []string{`element "evm" is declared twice`, "declared once"},
		},
		{
			name: "a route declared in two elements",
			doc:  head + atomicOf(element("evm", pulse("POST /rpc", "PostRPC")), element("bench", pulse("POST /rpc", "GetBench"))),
			want: []string{`the pulse "post:rpc" is declared 2 times (in the elements evm and bench)`, "the whole slice"},
		},
		{
			name: "a route in the default element and a named one",
			doc:  head + atomicOf(element("default", pulse("POST /rpc", "GetPing")), element("evm", pulse("POST /rpc", "PostRPC"))),
			want: []string{`the pulse "post:rpc" is declared 2 times (in the elements default and evm)`},
		},
		{
			name: "a Field name declared in two elements",
			doc:  head + atomicOf(element("evm", field("sequencer")), element("bench", field("sequencer"))),
			want: []string{`the field "sequencer" is declared 2 times (in the elements evm and bench)`},
		},
		{
			name: "a Field named like a Pulse route in another element",
			doc:  head + atomicOf(element("evm", field("bench")), element("bench", pulse("GET /bench", "GetBench"))),
			want: []string{`the Field "bench" and the Pulse route "bench" would share logs and metrics; rename one`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDriftfile(elementProject(t, tc.doc))
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

	// The control: the default element beside a named one, each with its own
	// slots, parses.
	ok := head + atomicOf(element("default", pulse("GET /ping", "GetPing")), element("evm", pulse("POST /rpc", "PostRPC")))
	if _, err := ParseDriftfile(elementProject(t, ok)); err != nil {
		t.Errorf("must parse:\n%s\n%v", ok, err)
	}
}

// A Field in an element written in anything but Go is refused before building.
func TestElements_AFieldInANonGoElementIsRefused(t *testing.T) {
	path := elementProject(t, nestedDriftfile)
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
	_, berr := atomic_cmd.BuildElements(AtomicSpecs(m))
	if berr == nil || !strings.Contains(berr.Error(), `element "evm"`) ||
		!strings.Contains(berr.Error(), "a Field runs Go in this release") {
		t.Errorf("a Field in a node element must be refused naming the element, got %v", berr)
	}
}

// The elements' slots are what every downstream reader counts: the reference
// check, the schedule registry and the slot count see every element's entries.
func TestElements_DownstreamReadersSeeEverySlot(t *testing.T) {
	doc := strings.Replace(nestedDriftfile, "    handler  = \"GetBench\"\n",
		"    handler  = \"GetBench\"\n    cron     = \"*/5 * * * *\"\n", 1)
	m, err := ParseDriftfile(elementProject(t, doc))
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
	if strings.Join(named, ",") != "field:sequencer,get:bench,post:admin/reset,post:rpc" {
		t.Errorf("namedFunctions = %v", named)
	}
}
