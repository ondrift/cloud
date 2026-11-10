package project

import (
	"strings"
	"testing"
)

// Removing a declaration removes the thing. A function the file stops declaring
// is a removal in the plan, named so the person agrees to it, and the plan says
// what goes: the code, its triggers and its history. Apply then asks for the
// answer (or takes --approve-remove) and the platform's resize deletes the
// deployed function; `drift atomic delete` is not part of the path.
//
// The block removed is the fixture's whole `bench` backend, so the live slice
// still books and serves GET /bench while the file no longer names it.
func TestPlan_AFunctionTheFileNoLongerDeclaresIsARemovalOfItsDeployedCode(t *testing.T) {
	planStub(t, fxevmLive, `[{"function_name":"bench","method":"get","digest":"bbbb2222"}]`)
	const benchBackend = `  backend "bench" {
    dir = "atomic/bench"

    function "GET /bench" {
      handler  = "GetBench"
      response = "json"
      memory   = "32MiB"
    }
  }
`
	out, err := planIn(t, func(s string) string {
		if !strings.Contains(s, benchBackend) {
			t.Fatalf("the fixture no longer holds the bench backend this test removes:\n%s", s)
		}
		return strings.Replace(s, benchBackend, "", 1)
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{
		"- function GET /bench",
		"deployed code, triggers and history are deleted",
		"Removes   function GET /bench",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	// The same plan names a removal only for what the file dropped: the functions
	// it still declares are not listed as removed.
	if strings.Contains(out, "- function POST /rpc") {
		t.Errorf("the plan removes POST /rpc, which the file still declares:\n%s", out)
	}
}

// The control: the same slice, nothing dropped, has no removal of that function
// and deploys nothing on its behalf. Without it the test above would pass on a
// plan that always listed the live function as removed.
func TestPlan_AFunctionTheFileStillDeclaresIsNotRemoved(t *testing.T) {
	planStub(t, fxevmLive, `[{"function_name":"bench","method":"get","digest":"bbbb2222"}]`)
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if strings.Contains(out, "- function GET /bench") || strings.Contains(out, "Removes   function GET /bench") {
		t.Errorf("the plan removes GET /bench, which the file declares:\n%s", out)
	}
}
