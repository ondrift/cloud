package project

import (
	"os"
	"path/filepath"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// projectWith returns a Manifest whose atomic.functions are fns.
//
// It needs no source tree: what a project declares is a property of the
// document, so every count below runs on the manifest alone.
func projectWith(t *testing.T, fns []Node) *Manifest {
	t.Helper()
	items := make([]any, len(fns))
	for i, fn := range fns {
		items[i] = map[string]any(fn)
	}
	slice := Node{"name": "test", "atomic": map[string]any{"functions": items}}
	return &Manifest{doc: slice, slice: slice, baseDir: t.TempDir()}
}

// A Driftfile declaring `cron:` must size its envelope for those jobs. Zero is
// not a small number here: enforceScheduleLimit treats `max <= 0` as "no quota
// to enforce", so a slice sized at zero is billed for none AND gated at none.
func TestCountScheduledFunctions_CountsTheDriftfileField(t *testing.T) {
	m := projectWith(t, []Node{
		{"name": "get:menu", "handler": "GetMenu"},
		{"name": "post:nightly-rollup", "handler": "Rollup", "cron": "0 2 * * *"},
		{"name": "post:hourly-cleanup", "handler": "Cleanup", "cron": "0 * * * *"},
	})

	got, err := CountScheduledFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Errorf("two declared schedules must size the envelope at 2 — a zero envelope is billed for none and enforced at none, got %d", got)
	}
}

// The count must not depend on reading the source tree. A manifest preflight
// runs before any deploy, and returning zero there would size a slice for no
// scheduled jobs and then let it register them unmetered.
func TestCountScheduledFunctions_SurvivesAnUnreadableTree(t *testing.T) {
	slice := Node{"name": "test", "atomic": map[string]any{"functions": []any{
		map[string]any{"name": "post:nightly-rollup", "handler": "Rollup", "cron": "0 2 * * *"},
		map[string]any{"name": "get:menu", "handler": "GetMenu"},
	}}}
	m := &Manifest{doc: slice, slice: slice, baseDir: filepath.Join(t.TempDir(), "does-not-exist")}

	got, _ := CountScheduledFunctions(m)
	if got != 1 {
		t.Errorf("the count is a property of the document and must not need source access, got %d", got)
	}
}

// A project with no schedules sizes at zero — which is correct here, and means
// "nothing to enforce" rather than "enforced at nothing".
func TestCountScheduledFunctions_NoSchedulesIsZero(t *testing.T) {
	m := projectWith(t, []Node{{"name": "get:menu", "handler": "GetMenu"}})

	got, err := CountScheduledFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("want 0, got %d", got)
	}
}

// Every declared function is one billed function, whatever the source looks
// like. Helpers beside them in the same file are free and uncounted, which is
// the point of declaring the surface rather than inferring it.
func TestCountAtomicFunctions_IsTheDeclaredList(t *testing.T) {
	m := projectWith(t, []Node{
		{"name": "get:menu", "handler": "GetMenu"},
		{"name": "post:orders", "handler": "PostOrders"},
		{"name": "queue:orders", "handler": "HandleOrder"},
	})

	got, err := CountAtomicFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3 {
		t.Errorf("three declared functions is three billed functions, got %d", got)
	}
}

// declaredSchedules is what reaches the deploy artifact. It keys on the
// function NAME because that is what the operator resolves the trigger target
// from — for an HTTP function the deployed name IS its route.
func TestDeclaredSchedules_KeyedByFunctionName(t *testing.T) {
	slice := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"name": "get:menu", "handler": "GetMenu"},
		map[string]any{"name": "post:nightly-rollup", "handler": "Rollup", "cron": "0 2 * * *"},
		map[string]any{"name": "", "cron": "0 5 * * *"}, // nameless: unroutable, must be dropped
	}}}
	m := &Manifest{doc: slice, slice: slice}

	got := declaredSchedules(m)
	if len(got) != 1 {
		t.Fatalf("only the named, cron-bearing entry may be published, got %v", got)
	}
	if got["post:nightly-rollup"] != "0 2 * * *" {
		t.Errorf("want the cron keyed by function name, got %v", got)
	}
}

// THE SEAM, and the test that was missing while the feature was dead.
//
// declaredSchedules WRITES a map that triggersFor READS, and the two are in
// different packages. Each half had tests; each half passed; and they keyed on
// different strings — `get:cronprobe` written against `cronprobe` read — so
// every `cron:` in every Driftfile was dropped, with the deploy reporting
// success.
//
// Nothing here calls triggersFor (it is unexported, one package over). What it
// asserts is the contract underneath: the key declaredSchedules produces must be
// a name that appears in FunctionSpecs, which is what the deploy path looks up
// by. A key that matches no spec can only ever miss.
func TestTheScheduleKeyIsTheKeyTheDeployLooksUp(t *testing.T) {
	// THE MODERN SPELLING, which is the whole point: `route` + `method`, no
	// `name`. Every other test in this file writes the deprecated `name:`, which
	// is why they all passed against a broken feature — a hand-written `name`
	// happens to look like the composite the normaliser would have produced.
	doc := Node{"slice": "lab", "atomic": map[string]any{"functions": []any{
		map[string]any{"route": "cronprobe", "method": "get", "handler": "GetCronprobe", "cron": "*/1 * * * *"},
		map[string]any{"route": "menu", "method": "get", "handler": "GetMenu"},
	}}}
	normaliseFunctionIdentities(doc)
	m := &Manifest{doc: doc, slice: doc, baseDir: t.TempDir()}

	schedules := declaredSchedules(m)
	if len(schedules) != 1 {
		t.Fatalf("a Driftfile written the documented way declared one cron and produced %d: %v",
			len(schedules), schedules)
	}

	specNames := map[string]bool{}
	for _, s := range FunctionSpecs(m) {
		specNames[s.Name] = true
	}
	for key := range schedules {
		if !specNames[key] {
			t.Errorf("the schedule is filed under %q, and no function ships under that name (%v).\n"+
				"  The deploy looks a schedule up by FunctionSpec.Name, so this key can only miss — "+
				"which is exactly how every declared cron was dropped in silence.",
				key, specNames)
		}
	}
}

// `drift atomic deploy` PUBLISHES THE SCHEDULES TOO.
//
// It reads a Driftfile — FunctionSpecsInDir walks up to the project manifest —
// and for the whole life of the feature it read one and never published what it
// found. So the same Driftfile produced a schedule under `drift file apply` and
// none under `drift atomic deploy`, with nothing said either way.
//
// That is the exact failure `refuseScheduleComments` refuses the RETIRED
// spelling for: "a schedule that exists depending on which deploy command you
// typed is worse than one that does not exist."
func TestAtomicDeployPublishesTheDeclaredSchedules(t *testing.T) {
	t.Cleanup(func() { atomic_cmd.SetDeclaredSchedules(nil) })
	atomic_cmd.SetDeclaredSchedules(nil)

	dir := t.TempDir()
	fnDir := filepath.Join(dir, "atomic")
	if err := os.MkdirAll(fnDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fnDir, "tick.js"),
		[]byte("function GetTick(req) { return [200, 'OK', {}]; }\nmodule.exports = { GetTick };\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"),
		[]byte("slice: demo\natomic:\n  functions:\n"+
			"    - route: tick\n      method: get\n      handler: GetTick\n      memory: 32MB\n"+
			"      dir: atomic\n      cron: \"*/1 * * * *\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	specs, err := FunctionSpecsInDir(fnDir)
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("want one spec, got %d", len(specs))
	}
	if got := atomic_cmd.DeclaredScheduleFor(specs[0].Name); got != "*/1 * * * *" {
		t.Errorf("`drift atomic deploy` resolved %q and published no schedule for it (got %q).\n"+
			"  The Driftfile declares one, so this command ships the function with its cron "+
			"dropped — and `drift file apply` on the same file ships it with the cron.",
			specs[0].Name, got)
	}
}

// And the composite is what it is, stated once so a change to the normaliser
// has to come past this line.
func TestAScheduleIsKeyedByMethodAndRouteTogether(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"route": "cronprobe", "method": "GET", "handler": "H", "cron": "*/1 * * * *"},
	}}}
	normaliseFunctionIdentities(doc)
	m := &Manifest{doc: doc, slice: doc}

	got := declaredSchedules(m)
	if got["get:cronprobe"] != "*/1 * * * *" {
		t.Errorf("want the cron under \"get:cronprobe\", got %v", got)
	}
}

// The envelope a schedule is sized against belongs to the SLICE FORM.
//
// `MaxNumberOfScheduledJobs` is not reachable from a manifest: the Driftfile
// does not declare how many scheduled jobs a slice may run, so there is nothing
// here to size and nothing on this side to assert. What the Driftfile does own —
// that a declared cron reaches the slice keyed by the function it belongs to —
// is covered by the tests above.
