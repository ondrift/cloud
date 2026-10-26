package project

import (
	"bytes"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

func deployedSet(keys ...string) []atomic_cmd.DeployedFunction {
	out := make([]atomic_cmd.DeployedFunction, 0, len(keys))
	for _, k := range keys {
		method, path, found := strings.Cut(k, ":")
		if !found {
			out = append(out, atomic_cmd.DeployedFunction{Key: k, Name: k, Method: "queue", Digest: "d"})
			continue
		}
		out = append(out, atomic_cmd.DeployedFunction{
			Key: k, Name: path, Method: method, Digest: "d",
		})
	}
	return out
}

// Renaming a route leaves the old one deployed. The slot is matched on name and
// method, so nothing moves and nothing on the apply path removes it: it keeps
// serving, comes back on every pod restart, and holds one of the function slots
// the slice was sold.
func TestReportOrphanedFunctions_NamesTheRouteTheManifestDropped(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge"}, nil, nil, nil)
	deployed := deployedSet("post:auth/challenge", "get:groups/:id")

	var out bytes.Buffer
	ReportOrphanedFunctions(m, deployed, &out)

	got := out.String()
	if !strings.Contains(got, "get:groups/:id") {
		t.Errorf("the dropped route must be reported, got %q", got)
	}
	if strings.Contains(got, "post:auth/challenge") {
		t.Errorf("a function the manifest still names is not an orphan, got %q", got)
	}
	if !strings.Contains(got, "drift atomic delete get:groups/:id") {
		t.Errorf("the report must carry the command that frees the slot, got %q", got)
	}
	if !strings.Contains(got, "slot") {
		t.Errorf("the report must say what the orphan costs, got %q", got)
	}
}

// The control: a manifest naming everything deployed reports nothing at all, so
// an ordinary apply prints no noise.
func TestReportOrphanedFunctions_ASupersetReportsNothing(t *testing.T) {
	m := manifestNaming([]string{"post:auth/challenge", "get:groups/:id", "get:new"}, nil, nil, nil)
	deployed := deployedSet("post:auth/challenge", "get:groups/:id")

	var out bytes.Buffer
	ReportOrphanedFunctions(m, deployed, &out)

	if out.Len() != 0 {
		t.Errorf("nothing is orphaned, so nothing must be printed, got %q", out.String())
	}
}

// A queue handler's stored method is "queue", so its key is the bare queue name
// on both sides. Keyed as "queue:orders" on one side and "orders" on the other,
// every queue function in the project would report as an orphan on every apply.
func TestReportOrphanedFunctions_AQueueHandlerIsNotAnOrphan(t *testing.T) {
	m := manifestNaming([]string{"queue:orders"}, nil, nil, nil)
	deployed := []atomic_cmd.DeployedFunction{
		{Key: "orders", Name: "orders", Method: "queue", Digest: "d"},
	}

	var out bytes.Buffer
	ReportOrphanedFunctions(m, deployed, &out)

	if out.Len() != 0 {
		t.Errorf("a declared queue handler must not report as an orphan, got %q", out.String())
	}
}

// A deployed Worker is matched by its booking key like any slot: a declared one
// is not an orphan, and one the Driftfile dropped is named with the key the
// delete command takes.
func TestReportOrphanedFunctions_AWorkerIsMatchedByItsKey(t *testing.T) {
	m := manifestWithWorker([]string{"get:ping"}, "sequencer")
	deployed := deployedSet("get:ping", "worker:sequencer", "worker:old-worker")

	var out bytes.Buffer
	ReportOrphanedFunctions(m, deployed, &out)

	got := out.String()
	if strings.Contains(got, "worker:sequencer") {
		t.Errorf("a declared Worker is not an orphan, got %q", got)
	}
	if !strings.Contains(got, "drift atomic delete worker:old-worker") {
		t.Errorf("the dropped Worker must be named with the key delete takes, got %q", got)
	}
}

// It reports and does nothing else. Nothing records which project owns a
// function, so two Driftfiles can share a slice — and a run that cannot tell
// "not mine" from "deleted" must neither refuse nor delete.
func TestReportOrphanedFunctions_ReportsWithoutRefusing(t *testing.T) {
	m := manifestNaming(nil, nil, nil, nil)
	deployed := deployedSet("get:someone-elses")

	var out bytes.Buffer
	ReportOrphanedFunctions(m, deployed, &out)

	got := out.String()
	if !strings.Contains(got, "get:someone-elses") {
		t.Fatalf("the orphan must still be reported, got %q", got)
	}
	if !strings.Contains(got, "Nothing was removed") {
		t.Errorf("the report must say that nothing was deleted, and why, got %q", got)
	}
}
