package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// codeLine is the line a coded failure ends with.
func codeLine(code string) string {
	return "  " + code + " · drift doctor explain " + code
}

// requireOneCodeLine fails unless the rendered text carries the code's line once.
func requireOneCodeLine(t *testing.T, what, rendered, code string) {
	t.Helper()
	if n := strings.Count(rendered, codeLine(code)); n != 1 {
		t.Errorf("%s prints %s's line %d times, want once:\n%s", what, code, n, rendered)
	}
}

// noSchemaProject is a project in the working directory on a machine that has
// never fetched the Driftfile format: HOME is empty and the platform does not
// serve one.
func noSchemaProject(t *testing.T) {
	t.Helper()
	planStub(t, fxevmLive, `[]`) // sets an empty HOME, and serves no /driftfile/schema
	common.ResetDriftfileSchemaRefresh()
	t.Cleanup(common.ResetDriftfileSchemaRefresh)
	compiledSchema = nil
	dir := t.TempDir()
	if err := copyTree(filepath.Join(mustAbs(t, "testdata"), "fxevm"), dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("RESET_KEY", "reset-key-4321")
	t.Setenv("EXPLORER_PASS", "explorer-pass")
	t.Setenv("DRIFT_SLICE", "")
}

// A machine that has never fetched the format cannot validate a Driftfile, and
// `drift plan` says so with the code that explains it.
func TestPlan_NoSchemaOnTheMachineIsRefusedWithItsCode(t *testing.T) {
	t.Skip("DRIFT-1008 is printed by no source file yet")
	noSchemaProject(t)

	_, err := planOutput(t, "--no-env-file")
	if err == nil {
		t.Fatal("a plan with no schema on the machine succeeded")
	}
	if !strings.Contains(err.Error(), "no copy of the Driftfile format") {
		t.Fatalf("the refusal changed its words:\n%v", err)
	}
	requireOneCodeLine(t, "drift plan", err.Error(), "DRIFT-1008")
}

// `drift apply` refuses the same way, through the same value.
func TestApply_NoSchemaOnTheMachineIsRefusedWithItsCode(t *testing.T) {
	t.Skip("DRIFT-1008 is printed by no source file yet")
	noSchemaProject(t)

	err := runApply(applyOptions{noEnvFile: true, answers: noAnswers(), out: os.Stdout, in: strings.NewReader("")})
	if err == nil {
		t.Fatal("an apply with no schema on the machine succeeded")
	}
	requireOneCodeLine(t, "drift apply", err.Error(), "DRIFT-1008")
}

// A compiled function booked under the floor is refused with the code that
// explains the floor, once, whether the booking is the file's own (what `drift
// file lint` checks) or the slice's (what `drift plan` and `drift apply` check).
func TestFloorRefusal_CarriesItsCodeOnce(t *testing.T) {
	t.Skip("DRIFT-1010 is printed by no source file yet")
	withSchema(t, floorSchema)
	root := demoProject(t, demoDriftfile) // get:ping books 8MB, in Go
	err := checkFixture(t, root)
	if err == nil {
		t.Fatal("a Go function booked at 8MB was accepted")
	}
	requireOneCodeLine(t, "the compiled-booking refusal", err.Error(), "DRIFT-1010")

	path := workerProject(t, workerDriftfile)
	m, perr := ParseDriftfile(path)
	if perr != nil {
		t.Fatal(perr)
	}
	els, berr := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if berr != nil {
		t.Fatal(berr)
	}
	live := liveWith([]string{"get:ping"}, nil, nil, nil)
	live.Config.Atomic.Functions = append(live.Config.Atomic.Functions,
		AtomicFunction{Name: "worker:sequencer", MemoryBytes: 8 << 20})
	err = CheckSlotBookings(m, els, live)
	if err == nil {
		t.Fatal("a Go Worker booked at 8MiB on the slice was accepted")
	}
	requireOneCodeLine(t, "the slot refusal", err.Error(), "DRIFT-1010")
}

// A Driftfile with two unknown arguments is refused with both listed and the
// code printed once, after the list, not after each.
func TestCompileDriftfile_AnInvalidDriftfilePrintsItsCodeOnce(t *testing.T) {
	t.Skip("DRIFT-1009 is printed by no source file yet")
	withRealSchema(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Driftfile"), "slice \"demo\" {\n  first_unknown = 1\n  second_unknown = 2\n}\n")

	_, err := CompileDriftfile(filepath.Join(root, "Driftfile"))
	if err == nil {
		t.Fatal("a Driftfile with two unknown arguments was accepted")
	}
	rendered := err.Error()
	for _, want := range []string{"first_unknown", "second_unknown"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the refusal does not list %q:\n%s", want, rendered)
		}
	}
	requireOneCodeLine(t, "the compile", rendered, "DRIFT-1009")
	if !strings.HasSuffix(rendered, codeLine("DRIFT-1009")) {
		t.Errorf("the code's line is not the last line of the refusal:\n%s", rendered)
	}
}
