package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// fieldProject writes a Driftfile beside a Go default element holding a Pulse
// handler and a Field handler, and returns its path.
func fieldProject(t *testing.T, driftfile string) string {
	t.Helper()
	withRealSchema(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "atomic"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nimport \"context\"\n\n" +
		"func GetPing() {}\n\nfunc Sequencer(ctx context.Context) error { <-ctx.Done(); return nil }\n"
	if err := os.WriteFile(filepath.Join(root, "atomic", "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Driftfile")
	if err := os.WriteFile(path, []byte(driftfile), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const fieldDriftfile = `slice "demo" { billing = "monthly" }
element "default" {
  pulse "GET /ping" {
    handler = "GetPing"
    memory  = "16MiB"
  }
  field "sequencer" {
    handler = "Sequencer"
    memory  = "64MiB"
    secrets = ["LEDGER_KEY"]
    env     = { BATCH = "50" }
  }
}
`

// A field block parses into a spec booked as `field:<name>`, carrying the
// handler, the element directory, the secrets and the env the operator is sent,
// and nothing a Pulse has that a Field cannot. Its booking is in the shape.
func TestFieldSpecs_ParseTheFieldBlocks(t *testing.T) {
	path := fieldProject(t, fieldDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("a Driftfile declaring a Field must parse:\n%v", err)
	}

	fields := FieldSpecs(m)
	if len(fields) != 1 {
		t.Fatalf("want one Field spec, got %d: %+v", len(fields), fields)
	}
	f := fields[0]
	if f.Name != "field:sequencer" || !f.IsField() {
		t.Errorf("the Field is booked as %q (IsField=%v), want field:sequencer", f.Name, f.IsField())
	}
	if method, name := f.Wire(); method != "field" || name != "sequencer" {
		t.Errorf("the operator is sent method %q and name %q, want field and sequencer", method, name)
	}
	if f.Handler != "Sequencer" || f.Dir != filepath.Join(m.BaseDir(), "atomic") {
		t.Errorf("handler %q in %s, want Sequencer in the default element", f.Handler, f.Dir)
	}
	if strings.Join(f.Secrets, ",") != "LEDGER_KEY" || f.Env["BATCH"] != "50" {
		t.Errorf("secrets %v and env %v did not reach the spec", f.Secrets, f.Env)
	}
	if f.Auth != "" || f.Stream != "" || f.Response != "" {
		t.Errorf("a Field has no gate, stream or reply shape, got %+v", f)
	}

	// Every slot is in AtomicSpecs, the Pulse first, and only the Pulse is in
	// FunctionSpecs.
	var all []string
	for _, s := range AtomicSpecs(m) {
		all = append(all, s.Name)
	}
	if strings.Join(all, ",") != "get:ping,field:sequencer" {
		t.Errorf("AtomicSpecs = %v, want the Pulse then the Field", all)
	}
	if len(FunctionSpecs(m)) != 1 {
		t.Errorf("FunctionSpecs is the Pulses alone, got %d", len(FunctionSpecs(m)))
	}

	// The Field books its memory in the file, beside it.
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	booked := map[string]int{}
	for _, s := range c.Shape.Atomic.Functions {
		booked[s.Name] = s.MemoryBytes
	}
	if booked["field:sequencer"] != 64*mib {
		t.Errorf("the Field books %d bytes, want 64MiB", booked["field:sequencer"])
	}

	// And a Field that books nothing is refused: the figure is its pool and its
	// price, and nobody chose a default.
	noMemory := strings.Replace(fieldDriftfile, "    memory  = \"64MiB\"\n", "", 1)
	if _, err := ParseDriftfile(fieldProject(t, noMemory)); err == nil || !strings.Contains(err.Error(), "Missing memory") {
		t.Errorf("a Field with no memory must be refused, got %v", err)
	}
}

// The compiled floor is checked against the bookings the slice holds, or the
// file's own: a Go slot under the floor is refused naming it and where its
// booking is written; at the floor passes.
func TestCheckSlotBookings_ASlotIsCheckedAgainstItsBooking(t *testing.T) {
	path := fieldProject(t, fieldDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	els, err := atomic_cmd.BuildElements(AtomicSpecs(m))
	if err != nil {
		t.Fatal(err)
	}
	slots := func(fieldBytes int) *LiveSlice {
		l := liveWith([]string{"get:ping"}, nil, nil, nil)
		l.Config.Atomic.Functions = append(l.Config.Atomic.Functions,
			AtomicFunction{Name: "field:sequencer", MemoryBytes: fieldBytes})
		return l
	}

	err = CheckSlotBookings(m, els, slots(8<<20))
	if err == nil {
		t.Fatal("a Go Field booked at 8MiB on the slice must be refused")
	}
	for _, want := range []string{"field:sequencer booked 8MiB", "16MB", "memory in the Driftfile", "drift size"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, err)
		}
	}
	if err := CheckSlotBookings(m, els, slots(16<<20)); err != nil {
		t.Errorf("a Field booked at the floor must pass: %v", err)
	}

	// Offline, against the file's own bookings: a Go Pulse under the floor is
	// refused, which only the build can know, since the file never says the
	// language. The control is the fixture, booked at the floor.
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCompiledBookings(c, els); err != nil {
		t.Errorf("the fixture's bookings are at the floor and must pass: %v", err)
	}
	low := strings.Replace(fieldDriftfile, `memory  = "16MiB"`, `memory  = "8MiB"`, 1)
	cLow, err := CompileDriftfile(fieldProject(t, low))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCompiledBookings(cLow, els); err == nil || !strings.Contains(err.Error(), "get:ping booked 8MiB, in go") {
		t.Errorf("a Go Pulse booked at 8MiB must be refused offline, got %v", err)
	}
}

// A Field may not share its name with a Pulse's route: the slice keys logs and
// metrics by that bare name. Refused at the parse, naming both, before anything
// is built.
func TestFieldAndPulseNames_MayNotCollide(t *testing.T) {
	clash := strings.Replace(fieldDriftfile, `pulse "GET /ping"`, `pulse "POST /sequencer"`, 1)
	_, err := ParseDriftfile(fieldProject(t, clash))
	if err == nil {
		t.Fatal("a Field named like a Pulse route must be refused")
	}
	want := `the Field "sequencer" and the Pulse route "sequencer" would share logs and metrics; rename one`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal must say %q, got:\n%v", want, err)
	}

	// The control: the fixture's names differ, and it parses.
	if _, err := ParseDriftfile(fieldProject(t, fieldDriftfile)); err != nil {
		t.Errorf("a Field whose name no Pulse route shares must parse: %v", err)
	}
}

// A Field built from an element in any language but Go is refused by the
// build-elements pass, before anything is staged, naming the rule; and the
// apply preflight recognises that refusal as one to stop on.
func TestFieldInANonGoElement_IsRefusedBeforeBuilding(t *testing.T) {
	path := fieldProject(t, fieldDriftfile)
	root := filepath.Dir(path)
	if err := os.Remove(filepath.Join(root, "atomic", "main.go")); err != nil {
		t.Fatal(err)
	}
	src := "function GetPing(req) { return [200, 'OK', {}]; }\n" +
		"async function Sequencer() {}\nmodule.exports = { GetPing, Sequencer };\n"
	if err := os.WriteFile(filepath.Join(root, "atomic", "handlers.js"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the file is legal; the language is what is refused: %v", err)
	}
	_, berr := atomic_cmd.BuildElements(AtomicSpecs(m))
	if berr == nil {
		t.Fatal("a Field in a node element must be refused")
	}
	for _, want := range []string{`field "sequencer"`, "node", "a Field runs Go in this release"} {
		if !strings.Contains(berr.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, berr)
		}
	}
	if !errors.Is(berr, atomic_cmd.ErrFieldLanguage) {
		t.Errorf("the apply preflight stops on ErrFieldLanguage, and this refusal does not carry it: %v", berr)
	}

	// The same element with the Pulse alone builds: the refusal is the Field's.
	if _, err := atomic_cmd.BuildElements(FunctionSpecs(m)); err != nil {
		t.Errorf("the Pulse alone in a node element must build: %v", err)
	}
}
