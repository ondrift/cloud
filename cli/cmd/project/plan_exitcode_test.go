package project

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The plan file is written before the status is returned: a script that asks for
// both gets the file whatever the plan found.
func TestPlan_ExitCodeStillWritesThePlanFile(t *testing.T) {
	planStub(t, fxevmLive, `[]`)
	out, err := planIn(t, nil, "--exit-code", "--out", "plan.json")
	if got := exitStatusOf(err); got != 2 {
		t.Errorf("a plan of a slice that differs exits %d with --exit-code and --out, want 2 (error: %v)", got, err)
	}
	if _, serr := os.Stat("plan.json"); serr != nil {
		t.Errorf("the plan file is not written: %v", serr)
	}
	if !strings.Contains(out, "Plan written to plan.json") {
		t.Errorf("the plan does not say it wrote the file:\n%s", out)
	}
}

// What a script reads from the status is in the plan's help: each status, and
// what it means.
func TestPlan_HelpSaysWhatEachExitStatusMeans(t *testing.T) {
	help := GetPlanCmd().Long
	for _, status := range []int{0, 1, 2, 3} {
		if !strings.Contains(help, fmt.Sprintf("%d when", status)) {
			t.Errorf("the plan's help does not say when --exit-code exits %d:\n%s", status, help)
		}
	}
	if !strings.Contains(help, "Without the flag a difference exits 0") {
		t.Errorf("the plan's help does not say that a difference exits 0 without the flag:\n%s", help)
	}
}

// A value that is not one of the four states is no match: a script reading it
// gets a failure.
func TestVerdict_ZeroVerdictExitsAsAFailure(t *testing.T) {
	err := Verdict{}.exitError()
	if err == nil {
		t.Fatal("a verdict with no state is a match to a script")
	}
	if got := exitStatusOf(err); got != 1 {
		t.Errorf("a verdict with no state exits %d, want 1", got)
	}
}
