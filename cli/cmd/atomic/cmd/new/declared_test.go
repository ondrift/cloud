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
slice "acme" {
  billing = "free"
}

atomic {
  element "default" {
    # A pulse answers one call with one result.
    pulse "GET /hello" {
      handler = "GetHello"
      memory = "32MiB"
    }
  }
}
`

// The case this exists for: `drift file new` wrote the block, and its printed
// hint sent the user here. Telling them to declare it again hands them a
// duplicate of a block three lines above, and two pulses on one trigger is a
// compile error, not a harmless repeat.
func TestDeclaredRouteFindsWhatFileNewScaffolded(t *testing.T) {
	inDir(t, scaffolded)
	if !declaredRoute("get", "hello", "", false) {
		t.Error("the block `drift file new` writes was not recognised, so following its own hint tells the user to paste a duplicate")
	}
}

// A pulse the Driftfile does not name must still get the snippet: that is the
// normal path and the overwhelmingly common one.
func TestDeclaredRouteIgnoresADifferentPulse(t *testing.T) {
	inDir(t, scaffolded)
	if declaredRoute("post", "hello", "", false) {
		t.Error("a different METHOD on the same route was treated as declared: GET /hello and POST /hello are different pulses")
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
		{"a YAML Driftfile", "slice: acme\natomic:\n  pulses:\n    - route: hello\n      method: get\n"},
		{"no element", "slice \"acme\" { billing = \"free\" }\n"},
		{"a pulse outside any element", "slice \"acme\" { billing = \"free\" }\npulse \"GET /hello\" {}\n"},
		{"an element outside the atomic block", "slice \"acme\" { billing = \"free\" }\nelement \"default\" {\n  pulse \"GET /hello\" {}\n}\n"},
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

// A queue pulse is labelled with QUEUE and the queue it drains, which is the
// vocabulary the Driftfile uses and not the one the flags use.
func TestDeclaredRouteUnderstandsAQueuePulse(t *testing.T) {
	inDir(t, "atomic {\n  element \"default\" {\n    pulse \"QUEUE jobs\" {\n      handler = \"ProcessJobs\"\n    }\n  }\n}\n")
	if !declaredRoute("queue", "process-jobs", "jobs", true) {
		t.Error("a queue pulse is declared by its QUEUE name, not the pulse's name, and was not matched")
	}
	if declaredRoute("queue", "process-jobs", "other", true) {
		t.Error("a different queue was treated as declared")
	}
}

// A pulse declared inside a named element is recognised too, so the user is
// not handed a duplicate of it.
func TestDeclaredRouteFindsAPulseInsideANamedElement(t *testing.T) {
	inDir(t, "atomic {\n  element \"ml\" {\n    pulse \"POST /train\" {\n      handler = \"post_train\"\n    }\n  }\n}\n")
	if !declaredRoute("post", "train", "", false) {
		t.Error("a pulse declared inside an element was not recognised")
	}
	if declaredRoute("get", "train", "", false) {
		t.Error("a different method was treated as declared")
	}
}
