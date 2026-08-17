package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plan against a slice that does not exist is a REFUSAL naming the
// configurator, not a description of what would be created.
//
// This is the whole shape of the change: a Driftfile no longer declares a
// slice, so "it does not exist yet" stopped being a thing this file can fix.
func TestRunPlan_RefusesWhenTheSliceDoesNotExist(t *testing.T) {
	m := planManifest(t, "slice: ghost\n")

	err := runPlan(m, nil)
	if err == nil {
		t.Fatal("a plan against a slice that does not exist must refuse — apply can no " +
			"longer create one, so describing a creation would describe nothing")
	}
	for _, want := range []string{"ghost", "configurator"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("the refusal should mention %q, got: %v", want, err)
		}
	}
}

// The plan prices NOTHING. A Driftfile no longer declares a shape, so a cost
// printed here would be a second pricing model computed from a document that
// does not describe what is billed — and the configurator already shows the
// price of the shape that exists.
func TestRunPlan_PricesNothing(t *testing.T) {
	m := planManifest(t, "slice: demo\natomic:\n  functions:\n"+
		"    - route: ping\n      method: get\n      handler: GetPing\n")

	out := capturePlan(t, m, &LiveSlice{})

	for _, forbidden := range []string{"€", "/mo", "cost", "price", "monthly", "tier"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("the plan mentions %q, so it is still pricing a shape this file no "+
				"longer declares:\n%s", forbidden, out)
		}
	}
}

// What it DOES report: the things this file actually deploys.
func TestRunPlan_ReportsWhatWouldBeDeployed(t *testing.T) {
	m := planManifest(t, "slice: demo\natomic:\n  functions:\n"+
		"    - route: ping\n      method: get\n      handler: GetPing\n"+
		"    - route: orders\n      method: queue\n      handler: HandleOrders\n"+
		"backbone:\n  nosql:\n    - slot: events\n")

	out := capturePlan(t, m, &LiveSlice{})

	for _, want := range []string{"demo", "get:ping", "queue:orders", "events"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan should report %q, got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "nothing was deployed") {
		t.Errorf("the plan must say it deployed nothing, got:\n%s", out)
	}
}

// capturePlan runs runPlan with stdout redirected, and returns what it printed.
func capturePlan(t *testing.T, m *Manifest, live *LiveSlice) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()

	perr := runPlan(m, live)
	os.Stdout = prev
	_ = w.Close()
	out := <-done
	_ = r.Close()

	if perr != nil {
		t.Fatalf("runPlan: %v", perr)
	}
	return out
}

func planManifest(t *testing.T, body string) *Manifest {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	return m
}
