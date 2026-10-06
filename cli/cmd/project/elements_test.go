package project

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// elementProject writes a Driftfile beside three Go elements, under the schema
// fixture, and returns its path: `evm` and `bench` under ./atomic, and the
// default element's flat files directly in ./atomic.
func elementProject(t *testing.T, driftfile string) string {
	t.Helper()
	withMigrateSchema(t)
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

// The fxevm testnode, nested: two elements holding their Pulses and a Field.
const nestedDriftfile = `slice: fxevm-testnode
atomic:
  elements:
    - name: evm
      pulses:
        - route: rpc
          method: post
          handler: PostRPC
          response: json
        - route: admin/reset
          method: post
          handler: PostReset
          auth: apikey
          response: json
      fields:
        - name: sequencer
          handler: Sequencer
    - name: bench
      pulses:
        - route: bench
          method: get
          handler: GetBench
          response: json
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

// Every Pulse and Field declared inside an element reaches the same model a
// tagged entry did: its booking key, its element, and the element's directory.
// The slots are booked exactly as before, `post:rpc` and `field:sequencer`, and
// the elements build from their own folders.
func TestElements_NormaliseIntoTheFlatModel(t *testing.T) {
	path := elementProject(t, nestedDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the nested testnode must parse:\n%v", err)
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
		t.Fatalf("the nested elements must build:\n%v", err)
	}
	var names []string
	for _, el := range els {
		names = append(names, el.Name+"="+el.Lang)
	}
	if strings.Join(names, ",") != "bench=go,evm=go" {
		t.Errorf("elements built: %v, want bench and evm, both Go", names)
	}
}

// An element's `dir`, `env` and `secrets` apply to every Pulse and Field in it:
// the entry's own `env` key wins over the element's for the same name, the
// secrets are the union, and an entry's own `dir` overrides the element's. The
// top-level lists are the default element, and a top-level entry tagged with a
// declared element is merged into it.
func TestElements_ApplyTheElementsDirEnvAndSecrets(t *testing.T) {
	path := elementProject(t, `slice: demo
atomic:
  pulses:
    - route: ping
      method: get
      handler: GetPing
    - route: reset
      method: post
      handler: PostReset
      element: evm
  elements:
    - name: evm
      dir: ./atomic/evm
      env:
        CHAIN_ID: "31337"
        MODE: element
      secrets: [SIGNER_KEY, RPC_TOKEN]
      pulses:
        - route: rpc
          method: post
          handler: PostRPC
          env:
            MODE: pulse
          secrets: [RPC_TOKEN, ADMIN_KEY]
      fields:
        - name: sequencer
          handler: Sequencer
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
		t.Errorf("a top-level Pulse is the default element's, with nothing of evm's: %+v", ping)
	}

	rpc := byName["post:rpc"]
	if rpc.Env["CHAIN_ID"] != "31337" || rpc.Env["MODE"] != "pulse" || len(rpc.Env) != 2 {
		t.Errorf("the Pulse's env must be the element's with its own MODE winning, got %v", rpc.Env)
	}
	if strings.Join(rpc.Secrets, ",") != "SIGNER_KEY,RPC_TOKEN,ADMIN_KEY" {
		t.Errorf("the Pulse's secrets must be the union, element first, got %v", rpc.Secrets)
	}

	seq := byName["field:sequencer"]
	if specSummary(t, root, seq) != "field:sequencer@evm in atomic/evm" ||
		seq.Env["MODE"] != "element" || strings.Join(seq.Secrets, ",") != "SIGNER_KEY,RPC_TOKEN" {
		t.Errorf("the Field must carry the element's dir, env and secrets, got %+v", seq)
	}

	reset := byName["post:reset"]
	if specSummary(t, root, reset) != "post:reset@evm in atomic/evm" || reset.Env["CHAIN_ID"] != "31337" {
		t.Errorf("a top-level entry tagged evm is merged into the declared element, got %+v", reset)
	}

	// An entry's own `dir` overrides the element's.
	withDir := strings.Replace(nestedDriftfile, "        - route: bench\n",
		"        - route: bench\n          dir: ./atomic/evm\n", 1)
	m, err = ParseDriftfile(elementProject(t, withDir))
	if err != nil {
		t.Fatalf("parse:\n%v", err)
	}
	for _, s := range FunctionSpecs(m) {
		if s.Name == "get:bench" && filepath.Base(s.Dir) != "evm" {
			t.Errorf("an entry's own dir must override its element's, got %s", s.Dir)
		}
	}
}

// What the schema cannot express is refused after the elements are flattened,
// across the whole Driftfile, each naming what to change.
func TestElements_ValidationAcrossTheWholeDriftfile(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want []string
	}{
		{
			name: "an element declared twice",
			doc: "slice: demo\natomic:\n  elements:\n" +
				"    - name: evm\n      pulses:\n        - {route: rpc, method: post, handler: PostRPC}\n" +
				"    - name: evm\n      fields:\n        - {name: sequencer, handler: Sequencer}\n",
			want: []string{`atomic.elements declares the element "evm" 2 times`, "declare it once"},
		},
		{
			name: "the default element in elements and at the top level",
			doc: "slice: demo\natomic:\n  pulses:\n    - {route: ping, method: get, handler: GetPing}\n" +
				"  elements:\n    - name: default\n      pulses:\n        - {route: pong, method: get, handler: GetPing}\n",
			want: []string{`atomic.elements declares the element "default"`, "atomic.pulses", "one place"},
		},
		{
			name: "a route declared in two elements",
			doc: "slice: demo\natomic:\n  elements:\n" +
				"    - name: evm\n      pulses:\n        - {route: rpc, method: post, handler: PostRPC}\n" +
				"    - name: bench\n      pulses:\n        - {route: rpc, method: post, handler: GetBench}\n",
			want: []string{`the pulse "post:rpc" is declared 2 times (in the elements evm and bench)`, "the whole slice"},
		},
		{
			name: "a route in an element and at the top level",
			doc: "slice: demo\natomic:\n  pulses:\n    - {route: rpc, method: post, handler: GetPing}\n" +
				"  elements:\n    - name: evm\n      pulses:\n        - {route: rpc, method: post, handler: PostRPC}\n",
			want: []string{`the pulse "post:rpc" is declared 2 times (in the elements default and evm)`},
		},
		{
			name: "a Field name declared in two elements",
			doc: "slice: demo\natomic:\n  fields:\n    - {name: sequencer, handler: Sequencer, element: evm}\n" +
				"  elements:\n    - name: bench\n      fields:\n        - {name: sequencer, handler: Sequencer}\n",
			want: []string{`the field "sequencer" is declared 2 times (in the elements evm and bench)`},
		},
		{
			name: "a Field named like a Pulse route in another element",
			doc: "slice: demo\natomic:\n  elements:\n" +
				"    - name: evm\n      fields:\n        - {name: bench, handler: Sequencer}\n" +
				"    - name: bench\n      pulses:\n        - {route: bench, method: get, handler: GetBench}\n",
			want: []string{`the Field "bench" and the Pulse route "bench" would share logs and metrics; rename one`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := elementProject(t, tc.doc)
			if err := ValidateDriftfileSource([]byte(tc.doc)); err != nil {
				t.Fatalf("the schema must accept this document, so the refusal is the CLI's:\n%v", err)
			}
			_, err := ParseDriftfile(path)
			if err == nil {
				t.Fatal("the document must be refused")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal must say %q, got:\n%v", w, err)
				}
			}
			if strings.Contains(err.Error(), "\u2014") {
				t.Errorf("the refusal carries an em dash:\n%v", err)
			}
		})
	}

	// The controls: a top-level entry tagged with a declared element is merged
	// into it, and an `elements` entry naming `default` stands alone when
	// nothing is declared at the top level.
	for _, ok := range []string{
		"slice: demo\natomic:\n  pulses:\n    - {route: reset, method: post, handler: PostReset, element: evm}\n" +
			"  elements:\n    - name: evm\n      pulses:\n        - {route: rpc, method: post, handler: PostRPC}\n",
		"slice: demo\natomic:\n  elements:\n    - name: default\n      pulses:\n        - {route: ping, method: get, handler: GetPing}\n",
	} {
		if _, err := ParseDriftfile(elementProject(t, ok)); err != nil {
			t.Errorf("must parse:\n%s\n%v", ok, err)
		}
	}
}

// An environment overlay is merged before the elements are flattened, so an
// overlay replacing `elements` replaces them, and a clash it introduces is
// refused when that environment is selected.
func TestElements_AnOverlayIsFlattenedAfterTheMerge(t *testing.T) {
	doc := nestedDriftfile + `environments:
  prod: {}
  staging:
    atomic:
      elements:
        - name: bench
          pulses:
            - route: bench
              method: post
              handler: GetBench
  broken:
    atomic:
      pulses:
        - route: rpc
          method: post
          handler: GetPing
`
	m, err := ParseDriftfile(elementProject(t, doc))
	if err != nil {
		t.Fatalf("parse:\n%v", err)
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range AtomicSpecs(m) {
		names = append(names, s.Name+"@"+s.Element)
	}
	if strings.Join(names, ",") != "post:bench@bench" {
		t.Errorf("staging's elements replace the base's, got %v", names)
	}

	m, err = ParseDriftfile(elementProject(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SelectEnvironment("broken", true); err == nil ||
		!strings.Contains(err.Error(), `the pulse "post:rpc" is declared 2 times`) {
		t.Errorf("the clash the overlay introduces must be refused, got %v", err)
	}
}

// A Field in an element written in anything but Go is refused before
// building, as a tagged one was.
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

// The `element` tag on a top-level entry is deprecated: still read, announced
// once per list naming `drift file migrate`, and a nested file draws nothing.
func TestElements_TheElementTagIsAnnouncedNamingMigrate(t *testing.T) {
	notices := func(doc string) string {
		var buf bytes.Buffer
		t.Cleanup(common.RedirectDeprecationWarnings(&buf))
		common.ResetDeprecationState()
		t.Cleanup(common.ResetDeprecationState)
		if _, err := ParseDriftfile(elementProject(t, doc)); err != nil {
			t.Fatalf("parse:\n%v", err)
		}
		return buf.String()
	}

	tagged := notices("slice: demo\natomic:\n  pulses:\n" +
		"    - {route: rpc, method: post, handler: PostRPC, element: evm}\n" +
		"    - {route: bench, method: get, handler: GetBench, element: bench}\n" +
		"  fields:\n    - {name: sequencer, handler: Sequencer, element: evm}\n")
	for _, want := range []string{`"atomic.pulses[].element"`, `"atomic.fields[].element"`, common.MigrateHint} {
		if !strings.Contains(tagged, want) {
			t.Errorf("the notice must say %q, got:\n%s", want, tagged)
		}
	}
	if n := strings.Count(tagged, `"atomic.pulses[].element"`); n != 1 {
		t.Errorf("the pulse tag was announced %d times, want once:\n%s", n, tagged)
	}
	if strings.Contains(tagged, "\u2014") {
		t.Errorf("the notice carries an em dash:\n%s", tagged)
	}

	if nested := notices(nestedDriftfile); strings.TrimSpace(nested) != "" {
		t.Errorf("a nested file must draw no notice, got:\n%s", nested)
	}

	// A Pulse inside an element that still writes a `memory` hears what a
	// top-level one does: the booking is the slice shape's.
	withMemory := notices(strings.Replace(nestedDriftfile, "          handler: GetBench\n",
		"          handler: GetBench\n          memory: 32MB\n", 1))
	if !strings.Contains(withMemory, `"atomic.elements[].pulses[].memory" is deprecated`) ||
		!strings.Contains(withMemory, "drift slice resize") {
		t.Errorf("a nested Pulse's memory must be announced as a slice setting, got:\n%s", withMemory)
	}
}

// The elements' slots are what every downstream reader counts: the reference
// check, the schedule registry and the slot count see the nested entries.
func TestElements_DownstreamReadersSeeTheNestedSlots(t *testing.T) {
	doc := strings.Replace(nestedDriftfile, "          handler: GetBench\n",
		"          handler: GetBench\n          cron: \"*/5 * * * *\"\n", 1)
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
