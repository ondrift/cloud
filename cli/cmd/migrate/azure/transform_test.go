package azure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPipeline_SnapshotToTransform runs the whole offline pipeline — snapshot
// produces azure_export/, transform turns it into drift_workspace/ — and pins
// the contract that matters most: the generated Driftfile passes the platform's
// own ParseDriftfile (called inside runTransform), so the output is deployable.
func TestPipeline_SnapshotToTransform(t *testing.T) {
	exportDir := t.TempDir()
	wsDir := t.TempDir()

	m, err := runSnapshot(fakeAz{t}, fakeProvider{}, nil, "Contoso-Prod", "demo-rg", exportDir, "orders", true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	res, err := runTransform(exportDir, wsDir, m)
	if err != nil {
		t.Fatalf("transform (incl. Driftfile validation): %v", err)
	}

	// 1 scaffold: the HTTP function. The timer is refused (Drift scheduling needs
	// a `# drift:schedule` source comment, not a cron= trigger) and the Node
	// function is v1-refused.
	eq(t, "scaffolds", res.scaffolds, 1)

	// The workspace has a deployable shape.
	for _, rel := range []string{
		"Driftfile", "REFUSED.md", "REPORT.md", "PLAN.md", ".env.migrate",
		"atomic/get-order/get-order.py",
		"backbone/nosql/orders.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(wsDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected workspace file %s: %v", rel, err)
		}
	}

	// The scaffold is source and nothing else. What the function IS lives in the
	// generated Driftfile, checked below — a migrated project reads as ported
	// code rather than as code wearing Drift markup.
	getOrder := readFile(t, wsDir, "atomic/get-order/get-order.py")
	if strings.Contains(getOrder, "@atomic") {
		t.Errorf("the scaffold must carry no Drift annotation:\n%s", getOrder)
	}
	if !strings.Contains(getOrder, "def get_orders_id(req):") {
		t.Errorf("get-order scaffold missing the Drift handler def:\n%s", getOrder)
	}
	if !strings.Contains(getOrder, "import azure.functions as func") {
		t.Errorf("get-order scaffold should preserve the original body verbatim (commented)")
	}

	// The timer trigger is refused (TIMER_TRIGGER), never scaffolded — a Driftfile
	// cron: alone would bill for a job that never fires (Drift scheduling is wired
	// from a `# drift:schedule` source comment, not yet emitted by transform).
	var timerRefused bool
	for _, r := range res.refusals {
		if r.Code == RefuseTimerTrigger {
			timerRefused = true
		}
	}
	if !timerRefused {
		t.Errorf("expected the timer trigger to be refused (TIMER_TRIGGER)")
	}
	if _, err := os.Stat(filepath.Join(wsDir, "atomic", "nightly-report", "nightly-report.py")); err == nil {
		t.Errorf("timer function must NOT be scaffolded while scheduling is unwired")
	}

	// Cosmos id → _id remap, original kept as _azure_id.
	orders := readFile(t, wsDir, "backbone/nosql/orders.jsonl")
	if !strings.Contains(orders, `"_id":"o1"`) || !strings.Contains(orders, `"_azure_id":"o1"`) {
		t.Errorf("cosmos id→_id remap not applied:\n%s", orders)
	}
	if strings.Contains(orders, `"id":"o1"`) {
		t.Errorf("original `id` key should have been removed after remap:\n%s", orders)
	}

	// The Driftfile carries the whole declaration: the function's real Azure
	// route (not a slug derived from its folder) in the element it was
	// scaffolded in, the callable serving it, the seeded collection and the
	// secrets, each read from the environment.
	df := readFile(t, wsDir, "Driftfile")
	for _, want := range []string{
		`slice "orders" {}`,
		`element "get-order"`,
		`pulse "GET /orders/:id"`,
		`handler = "get_orders_id"`,
		`nosql "orders"`,
		`seed = "backbone/nosql/orders.jsonl"`,
		`secret "STRIPE_KEY"`,
		`value = env("STRIPE_KEY")`,
	} {
		if strings.Contains(df, "nightly-report") {
			t.Errorf("refused timer must not appear in the Driftfile:\n%s", df)
		}
		if !strings.Contains(df, want) {
			t.Errorf("Driftfile missing %q:\n%s", want, df)
		}
	}

	if testing.Verbose() {
		t.Logf("\n===== generated Driftfile =====\n%s\n===== atomic/get-order/get-order.py =====\n%s\n===== REPORT.md =====\n%s",
			df, getOrder, readFile(t, wsDir, "REPORT.md"))
	}
}

// A function Azure gates with a key reads its key from a variable named for
// its folder, written env("NAME") in the Driftfile and left for the operator in
// .env.migrate: Azure's function keys are not exported, and `drift apply`
// refuses until the variable holds one.
func TestBuildDriftfile_AKeyGatedFunctionReadsItsKeyFromAVariable(t *testing.T) {
	df := buildDriftfile("orders", []driftFn{{
		slug: "get-order", handler: "get_orders_id",
		trigger: ManifestTrigger{Type: "http", Method: "get", Route: "orders/:id", Auth: "apikey"},
	}}, nil, nil)
	for _, want := range []string{
		`element "get-order"`,
		"      auth {\n        api_key = env(\"GET_ORDER_API_KEY\")\n      }\n",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Driftfile missing %q:\n%s", want, df)
		}
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}
