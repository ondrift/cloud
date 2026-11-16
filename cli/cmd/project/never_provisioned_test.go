package project

import (
	"os"
	"strings"
	"testing"
)

// neverProvisionedLive is the fxevm slice as the platform answers for one that
// never provisioned: the record, plus the derived flag.
func neverProvisionedLive() string {
	return strings.Replace(fxevmLive, `"tier":"custom",`, `"tier":"custom","never_provisioned":true,`, 1)
}

// assertNothingReadOrSent fails when the stub saw a read of the deployed
// functions or any request that is not a GET.
func assertNothingReadOrSent(t *testing.T, rec *recorder) {
	t.Helper()
	if rec.count("GET /ops/slice") == 0 {
		t.Fatalf("the slice was never read, so the flag was never seen: %v", rec.hits)
	}
	if n := rec.count("GET /ops/atomic"); n != 0 {
		t.Errorf("the deployed functions were read %d time(s) for a slice that never provisioned: %v", n, rec.hits)
	}
	for _, hit := range rec.hits {
		if !strings.HasPrefix(hit, "GET ") {
			t.Errorf("a request that writes was sent: %s", hit)
		}
	}
}

// A plan stops at the live slice carrying never_provisioned, before it reads the
// deployed functions, and says which delete clears it.
func TestPlan_StopsOnANeverProvisionedLiveSlice(t *testing.T) {
	rec := planStub(t, neverProvisionedLive(), `[]`)
	_, err := planIn(t, nil)
	if err == nil {
		t.Fatal("a plan of a slice that never provisioned succeeded")
	}
	want := "slice fxevm-testnode was never provisioned; nothing was applied. " +
		"Run drift slice delete fxevm-testnode, then apply again"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the plan says %q, want it to contain %q", err.Error(), want)
	}
	assertNothingReadOrSent(t, rec)
}

// An apply stops at the same place: nothing is created, resized or deployed.
func TestApply_StopsOnANeverProvisionedLiveSlice(t *testing.T) {
	rec := planStub(t, neverProvisionedLive(), `[]`)
	inFixture(t, nil)
	a := noAnswers()
	a.price = 0
	var err error
	captureStdout(t, func() {
		err = runApply(applyOptions{noEnvFile: true, answers: a, out: os.Stdout, in: strings.NewReader("")})
	})
	if err == nil {
		t.Fatal("an apply of a slice that never provisioned succeeded")
	}
	want := "slice fxevm-testnode was never provisioned; nothing was applied. " +
		"Run drift slice delete fxevm-testnode, then apply again"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the apply says %q, want it to contain %q", err.Error(), want)
	}
	assertNothingReadOrSent(t, rec)
}

// A slice whose JSON has no never_provisioned key is planned as before: the
// field is read, so an API that does not send it reads as false.
func TestPlan_ASliceWithoutTheKeyIsPlannedAsBefore(t *testing.T) {
	rec := planStub(t, fxevmLive, `[]`)
	if _, err := planIn(t, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if rec.count("GET /ops/atomic") == 0 {
		t.Errorf("the deployed functions were not read: %v", rec.hits)
	}
}
