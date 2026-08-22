package project

import (
	"path/filepath"
	"testing"
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

// The envelope a schedule is sized against belongs to the SLICE FORM.
//
// `MaxNumberOfScheduledJobs` is not reachable from a manifest: the Driftfile
// does not declare how many scheduled jobs a slice may run, so there is nothing
// here to size and nothing on this side to assert. What the Driftfile does own —
// that a declared cron reaches the slice keyed by the function it belongs to —
// is covered by the tests above.
