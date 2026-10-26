package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// workerProject writes a Driftfile beside a Go default backend holding a Function
// handler and a Worker handler, and returns its path.
func workerProject(t *testing.T, driftfile string) string {
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

const workerDriftfile = `slice "demo" {}
atomic {
  backend "default" {
    function "GET /ping" {
      handler = "GetPing"
      memory  = "16MiB"
    }
    worker "sequencer" {
      handler = "Sequencer"
      memory  = "64MiB"
      secrets = ["LEDGER_KEY"]
      env     = { BATCH = "50" }
    }
  }
}
`

// A worker block parses into a spec booked as `worker:<name>`, carrying the
// handler, the backend directory, the secrets and the env the operator is sent,
// and nothing a Function has that a Worker cannot. Its booking is in the shape.
func TestWorkerSpecs_ParseTheWorkerBlocks(t *testing.T) {
	path := workerProject(t, workerDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("a Driftfile declaring a Worker must parse:\n%v", err)
	}

	workers := WorkerSpecs(m)
	if len(workers) != 1 {
		t.Fatalf("want one Worker spec, got %d: %+v", len(workers), workers)
	}
	f := workers[0]
	if f.Name != "worker:sequencer" || !f.IsWorker() {
		t.Errorf("the Worker is booked as %q (IsWorker=%v), want worker:sequencer", f.Name, f.IsWorker())
	}
	if method, name := f.Wire(); method != "worker" || name != "sequencer" {
		t.Errorf("the operator is sent method %q and name %q, want worker and sequencer", method, name)
	}
	if f.Handler != "Sequencer" || f.Dir != filepath.Join(m.BaseDir(), "atomic") {
		t.Errorf("handler %q in %s, want Sequencer in the default backend", f.Handler, f.Dir)
	}
	if strings.Join(f.Secrets, ",") != "LEDGER_KEY" || f.Env["BATCH"] != "50" {
		t.Errorf("secrets %v and env %v did not reach the spec", f.Secrets, f.Env)
	}
	if f.Auth != "" || f.Stream != "" || f.Response != "" {
		t.Errorf("a Worker has no gate, stream or reply shape, got %+v", f)
	}

	// Every slot is in AtomicSpecs, the Function first, and only the Function is in
	// FunctionSpecs.
	var all []string
	for _, s := range AtomicSpecs(m) {
		all = append(all, s.Name)
	}
	if strings.Join(all, ",") != "get:ping,worker:sequencer" {
		t.Errorf("AtomicSpecs = %v, want the Function then the Worker", all)
	}
	if len(FunctionSpecs(m)) != 1 {
		t.Errorf("FunctionSpecs is the Functions alone, got %d", len(FunctionSpecs(m)))
	}

	// The Worker books its memory in the file, beside it.
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	booked := map[string]int{}
	for _, s := range c.Shape.Atomic.Functions {
		booked[s.Name] = s.MemoryBytes
	}
	if booked["worker:sequencer"] != 64*mib {
		t.Errorf("the Worker books %d bytes, want 64MiB", booked["worker:sequencer"])
	}

	// And a Worker that books nothing is refused: the figure is its pool and its
	// price, and nobody chose a default.
	noMemory := strings.Replace(workerDriftfile, "    memory  = \"64MiB\"\n", "", 1)
	if _, err := ParseDriftfile(workerProject(t, noMemory)); err == nil || !strings.Contains(err.Error(), "Missing memory") {
		t.Errorf("a Worker with no memory must be refused, got %v", err)
	}
}

// The compiled floor is checked against the bookings the slice holds, or the
// file's own: a Go slot under the floor is refused naming it and where its
// booking is written; at the floor passes.
func TestCheckSlotBookings_ASlotIsCheckedAgainstItsBooking(t *testing.T) {
	path := workerProject(t, workerDriftfile)
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	els, err := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if err != nil {
		t.Fatal(err)
	}
	slots := func(workerBytes int) *LiveSlice {
		l := liveWith([]string{"get:ping"}, nil, nil, nil)
		l.Config.Atomic.Functions = append(l.Config.Atomic.Functions,
			AtomicFunction{Name: "worker:sequencer", MemoryBytes: workerBytes})
		return l
	}

	err = CheckSlotBookings(m, els, slots(8<<20))
	if err == nil {
		t.Fatal("a Go Worker booked at 8MiB on the slice must be refused")
	}
	for _, want := range []string{"worker:sequencer booked 8MiB", "16MB", "memory in the Driftfile", "drift size"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, err)
		}
	}
	if err := CheckSlotBookings(m, els, slots(16<<20)); err != nil {
		t.Errorf("a Worker booked at the floor must pass: %v", err)
	}

	// Offline, against the file's own bookings: a Go Function under the floor is
	// refused, which only the build can know, since the file never says the
	// language. The control is the fixture, booked at the floor.
	c, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCompiledBookings(c, els); err != nil {
		t.Errorf("the fixture's bookings are at the floor and must pass: %v", err)
	}
	low := strings.Replace(workerDriftfile, `memory  = "16MiB"`, `memory  = "8MiB"`, 1)
	cLow, err := CompileDriftfile(workerProject(t, low))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCompiledBookings(cLow, els); err == nil || !strings.Contains(err.Error(), "get:ping booked 8MiB, in go") {
		t.Errorf("a Go Function booked at 8MiB must be refused offline, got %v", err)
	}
}

// A Worker may not share its name with a Function's route: the slice keys logs and
// metrics by that bare name. Refused at the parse, naming both, before anything
// is built.
func TestWorkerAndFunctionNames_MayNotCollide(t *testing.T) {
	clash := strings.Replace(workerDriftfile, `function "GET /ping"`, `function "POST /sequencer"`, 1)
	_, err := ParseDriftfile(workerProject(t, clash))
	if err == nil {
		t.Fatal("a Worker named like a Function route must be refused")
	}
	want := `the Worker "sequencer" and the Function route "sequencer" would share logs and metrics; rename one`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal must say %q, got:\n%v", want, err)
	}

	// The control: the fixture's names differ, and it parses.
	if _, err := ParseDriftfile(workerProject(t, workerDriftfile)); err != nil {
		t.Errorf("a Worker whose name no Function route shares must parse: %v", err)
	}
}

// A Worker built from a backend in any language but Go is refused by the
// build-backends pass, before anything is staged, naming the rule; and the
// apply preflight recognises that refusal as one to stop on.
func TestWorkerInANonGoBackend_IsRefusedBeforeBuilding(t *testing.T) {
	path := workerProject(t, workerDriftfile)
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
	_, berr := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if berr == nil {
		t.Fatal("a Worker in a node backend must be refused")
	}
	for _, want := range []string{`worker "sequencer"`, "node", "a Worker runs Go in this release"} {
		if !strings.Contains(berr.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, berr)
		}
	}
	if !errors.Is(berr, atomic_cmd.ErrWorkerLanguage) {
		t.Errorf("the apply preflight stops on ErrWorkerLanguage, and this refusal does not carry it: %v", berr)
	}

	// The same backend with the Function alone builds: the refusal is the Worker's.
	if _, err := atomic_cmd.BuildBackends(FunctionSpecs(m)); err != nil {
		t.Errorf("the Function alone in a node backend must build: %v", err)
	}
}
