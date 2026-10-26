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

// What `drift file new` writes, comments and all.
const scaffolded = `# acme: a Drift project.
slice "acme" {}

atomic {
  backend "default" {
    # A function answers one call with one result.
    function "GET /hello" {
      handler = "GetHello"
      memory = "32MiB"
    }
  }
}
`

// The case this exists for: `drift file new` wrote the block, and its printed
// hint sent the user here. Telling them to declare it again hands them a
// duplicate of a block three lines above, and two functions on one trigger is a
// compile error, not a harmless repeat.
func TestDeclaredRouteFindsWhatFileNewScaffolded(t *testing.T) {
	inDir(t, scaffolded)
	if !declaredRoute("get", "hello", "", false) {
		t.Error("the block `drift file new` writes was not recognised, so following its own hint tells the user to paste a duplicate")
	}
}

// A function the Driftfile does not name must still get the snippet: that is the
// normal path and the overwhelmingly common one.
func TestDeclaredRouteIgnoresADifferentFunction(t *testing.T) {
	inDir(t, scaffolded)
	if declaredRoute("post", "hello", "", false) {
		t.Error("a different METHOD on the same route was treated as declared: GET /hello and POST /hello are different functions")
	}
	if declaredRoute("get", "goodbye", "", false) {
		t.Error("a different route was treated as declared")
	}
}

// EVERY ambiguous answer is "not declared", because the snippet is the safe one.
// A user shown a block that already exists gets a lint error naming the
// duplicate; a user never shown it has nothing to paste at all.
func TestDeclaredRouteFailsOpen(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no Driftfile at all", ""},
		{"not HCL", "{{{ this is not hcl"},
		{"a YAML Driftfile", "slice: acme\natomic:\n  functions:\n    - route: hello\n      method: get\n"},
		{"no backend", "slice \"acme\" {}\n"},
		{"a function outside any backend", "slice \"acme\" {}\nfunction \"GET /hello\" {}\n"},
		{"a backend outside the atomic block", "slice \"acme\" {}\nbackend \"default\" {\n  function \"GET /hello\" {}\n}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inDir(t, c.body)
			if declaredRoute("get", "hello", "", false) {
				t.Errorf("answered `already declared` for %s: the snippet is the safe answer whenever this cannot tell", c.name)
			}
		})
	}
}

// A queue function is labelled with QUEUE and the queue it drains, which is the
// vocabulary the Driftfile uses and not the one the flags use.
func TestDeclaredRouteUnderstandsAQueueFunction(t *testing.T) {
	inDir(t, "atomic {\n  backend \"default\" {\n    function \"QUEUE jobs\" {\n      handler = \"ProcessJobs\"\n    }\n  }\n}\n")
	if !declaredRoute("queue", "process-jobs", "jobs", true) {
		t.Error("a queue function is declared by its QUEUE name, not the function's name, and was not matched")
	}
	if declaredRoute("queue", "process-jobs", "other", true) {
		t.Error("a different queue was treated as declared")
	}
}

// A function declared inside a named backend is recognised too, so the user is
// not handed a duplicate of it.
func TestDeclaredRouteFindsAFunctionInsideANamedBackend(t *testing.T) {
	inDir(t, "atomic {\n  backend \"ml\" {\n    function \"POST /train\" {\n      handler = \"post_train\"\n    }\n  }\n}\n")
	if !declaredRoute("post", "train", "", false) {
		t.Error("a function declared inside a backend was not recognised")
	}
	if declaredRoute("get", "train", "", false) {
		t.Error("a different method was treated as declared")
	}
}
