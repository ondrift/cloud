package project

import (
	"net/http"
	"strings"
	"testing"
)

// Alerts are not part of the Driftfile: they are set with `drift atomic alert`,
// and an apply neither reads nor writes them. An apply that read them found
// every live alert undeclared, because no file can declare one, and told the
// person to remove the alerts the docs tell them to set.
func TestApply_LeavesAlertsAlone(t *testing.T) {
	base := idleSliceStub("custom", []string{"get:ping"})
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/alert" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"name":"get:ping-errors"},{"name":"checkout-errors"}]`))
			return
		}
		base(w, r)
	})
	applyWithFunctions(t, []string{"ping"})

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Done in") {
		t.Fatalf("the apply never finished, so its silence about alerts proves nothing:\n%s", out)
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if n := rec.count(m + " /ops/atomic/alert"); n != 0 {
			t.Errorf("the apply issued %d × %s /ops/atomic/alert; alerts are not the file's", n, m)
		}
	}
	if strings.Contains(out, "alert") {
		t.Errorf("the apply talks about alerts it does not own:\n%s", out)
	}
}
