package atomic_cmd_new

import (
	"os"
	"path/filepath"
	"testing"
)

// inDir runs f with the working directory set to a fresh temp dir holding the
// given Driftfile content ("" writes no file at all).
func inDir(t *testing.T, driftfile string) {
	t.Helper()
	dir := t.TempDir()
	if driftfile != "" {
		if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(driftfile), 0o600); err != nil {
			t.Fatalf("write Driftfile: %v", err)
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

const scaffolded = `slice: acme

atomic:
  functions:
    - route: hello
      method: get
      handler: GetHello
`

// The case this exists for: `drift file new` wrote the entry, and its printed
// hint sent the user here. Telling them to declare it again hands them a
// duplicate of an entry three lines above — and two entries for one route is a
// parse error, not a harmless repeat.
func TestDeclaredRouteFindsWhatFileNewScaffolded(t *testing.T) {
	inDir(t, scaffolded)
	if !declaredRoute("get", "hello", "", false) {
		t.Error("the entry `drift file new` writes was not recognised, so following its own hint tells the user to paste a duplicate")
	}
}

// A function the Driftfile does not name must still get the snippet — that is
// the normal path and the overwhelmingly common one.
func TestDeclaredRouteIgnoresADifferentFunction(t *testing.T) {
	inDir(t, scaffolded)
	if declaredRoute("post", "hello", "", false) {
		t.Error("a different METHOD on the same route was treated as declared — get:hello and post:hello are different functions")
	}
	if declaredRoute("get", "goodbye", "", false) {
		t.Error("a different route was treated as declared")
	}
}

// EVERY ambiguous answer is "not declared", because the snippet is the safe one.
// A user shown an entry that already exists gets a lint error naming the
// duplicate; a user never shown it has nothing to paste at all.
func TestDeclaredRouteFailsOpen(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no Driftfile at all", ""},
		{"not YAML", "{{{ this is not yaml"},
		{"no atomic block", "slice: acme\n"},
		{"an empty function list", "slice: acme\natomic:\n  functions: []\n"},
		{"the retired grammar, which names no route", "slice: acme\natomic:\n  functions:\n    - name: \"get:hello\"\n      handler: GetHello\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inDir(t, c.body)
			if declaredRoute("get", "hello", "", false) {
				t.Errorf("answered `already declared` for %s — the snippet is the safe answer whenever this cannot tell", c.name)
			}
		})
	}
}

// A queue function is `method: queue` with the QUEUE as its route, which is the
// vocabulary the Driftfile uses and not the one the flags use.
func TestDeclaredRouteUnderstandsAQueueFunction(t *testing.T) {
	inDir(t, "slice: acme\natomic:\n  functions:\n    - route: jobs\n      method: queue\n      handler: ProcessJobs\n")
	if !declaredRoute("queue", "process-jobs", "jobs", true) {
		t.Error("a queue function is declared by its QUEUE name, not the function's name, and was not matched")
	}
	if declaredRoute("queue", "process-jobs", "other", true) {
		t.Error("a different queue was treated as declared")
	}
}

// The method comparison is case-insensitive: a Driftfile written `method: GET`
// is the same function, and printing a duplicate for it would be the bug.
func TestDeclaredRouteIsCaseInsensitiveOnMethod(t *testing.T) {
	inDir(t, "slice: acme\natomic:\n  functions:\n    - route: hello\n      method: GET\n      handler: GetHello\n")
	if !declaredRoute("get", "hello", "", false) {
		t.Error("`method: GET` was not matched against `get`")
	}
}
