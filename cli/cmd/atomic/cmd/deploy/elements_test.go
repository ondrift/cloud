package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSrc(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// specs is the shape `drift file apply` hands in: the Driftfile's entries,
// with each one's directory already resolved.
func spec(name, handler, element, dir string) FunctionSpec {
	return FunctionSpec{Name: name, Handler: handler, Element: element, Dir: dir}
}

// Functions group into elements by what the manifest says, and each binds to
// the file declaring its handler.
func TestBuildElements_GroupsByDeclaredElement(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "auth.go"),
		"package main\n\nfunc PostAuthLogin(body any, req any) {}\n")
	writeSrc(t, filepath.Join(root, "groups.go"),
		"package main\n\nfunc GetGroups(req any) {}\n")
	writeSrc(t, filepath.Join(root, "lib.go"), // a helper: never declared, never shipped
		"package main\n\ntype RequestBody = map[string]any\n")
	writeSrc(t, filepath.Join(root, "health", "health.go"),
		"package main\n\nfunc GetHealth(req any) {}\n")

	els, err := BuildElements([]FunctionSpec{
		spec("post:auth/login", "PostAuthLogin", "", root),
		spec("get:groups", "GetGroups", "", root),
		spec("get:health", "GetHealth", "health", filepath.Join(root, "health")),
	})
	if err != nil {
		t.Fatalf("BuildElements: %v", err)
	}
	if len(els) != 2 {
		t.Fatalf("got %d elements, want 2 (default + health): %+v", len(els), els)
	}
	def, health := els[0], els[1] // sorted by name → default, health
	if def.Name != "default" || def.Lang != "go" || len(def.Funcs) != 2 {
		t.Errorf("default element = %+v, want {default, go, 2 funcs}", def)
	}
	if health.Name != "health" || len(health.Funcs) != 1 {
		t.Errorf("health element = %+v, want {health, 1 func}", health)
	}

	// Funcs sort by declared name, so get:groups leads.
	if got := def.Funcs[0]; got.Spec.Handler != "GetGroups" || got.SourceFile != "groups.go" {
		t.Errorf("default func[0] = %+v, want GetGroups declared in groups.go", got)
	}
}

// A handler the manifest names and the source does not have is caught here,
// offline, rather than as a compiler error deep inside a per-function build.
func TestBuildElements_MissingHandlerIsRefusedUpFront(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "a.go"), "package main\n\nfunc GetA(req any) {}\n")

	_, err := BuildElements([]FunctionSpec{spec("get:b", "GetB", "", root)})
	if err == nil {
		t.Fatal("a handler that is not in the source must be refused")
	}
	// The message has to name what IS there, or the reader has no next step.
	if !strings.Contains(err.Error(), "GetA") {
		t.Errorf("the error should list the callables that do exist, got: %v", err)
	}
}

// The overwhelmingly common cause of "not found" is a case mismatch, so the
// error says so instead of listing everything and leaving the reader to spot it.
func TestBuildElements_CaseMismatchIsNamed(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "a.go"), "package main\n\nfunc GetUsers(req any) {}\n")

	_, err := BuildElements([]FunctionSpec{spec("get:users", "Getusers", "", root)})
	if err == nil {
		t.Fatal("expected a not-found error")
	}
	if !strings.Contains(err.Error(), "differ only in case") {
		t.Errorf("a case-only mismatch should be called out by name, got: %v", err)
	}
}

// Two languages in one element directory is incoherent → loud error.
func TestBuildElements_MixedLanguageRejected(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "a.go"), "package main\n\nfunc GetA(req any) {}\n")
	writeSrc(t, filepath.Join(root, "b.py"), "def get_b(req): pass\n")

	_, err := BuildElements([]FunctionSpec{spec("get:a", "GetA", "", root)})
	if err == nil {
		t.Fatal("expected a mixed-language error, got nil")
	}
}

// One handler declared twice in an element cannot be bound: the entry point
// imports it by name, so the build would pick one and drop the other.
func TestBuildElements_AmbiguousHandlerRejected(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "a.go"), "package main\n\nfunc Handle(req any) {}\n")
	writeSrc(t, filepath.Join(root, "b.go"), "package main\n\nfunc Handle(req any) {}\n")

	_, err := BuildElements([]FunctionSpec{spec("get:a", "Handle", "", root)})
	if err == nil {
		t.Fatal("a handler declared in two files must be refused")
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("the error should say the handler must be unique, got: %v", err)
	}
}

// The declared name IS the wire identity. A queue function splits into the
// synthetic `queue` method — which no HTTP request can match — and the queue's
// own name, and the slice re-joins them into the very string the Driftfile
// wrote, which is what makes the booking bind.
func TestFunctionSpec_WireSplitsTheDeclaredName(t *testing.T) {
	cases := []struct {
		declared     string
		method, name string
		trigger      string
	}{
		{"post:auth/challenge", "post", "auth/challenge", "http"},
		{"get:groups/:id", "get", "groups/:id", "http"},
		{"queue:orders", "queue", "orders", "queue"},
	}
	for _, c := range cases {
		s := FunctionSpec{Name: c.declared}
		m, n := s.Wire()
		if m != c.method || n != c.name {
			t.Errorf("%q → (%q, %q), want (%q, %q)", c.declared, m, n, c.method, c.name)
		}
		if got := s.Trigger(); got != c.trigger {
			t.Errorf("%q trigger = %q, want %q", c.declared, got, c.trigger)
		}
		if m+":"+n != c.declared {
			t.Errorf("%q does not round-trip through Wire() — the slice's booking key "+
				"is rebuilt this way, so a name that does not survive the split books nothing",
				c.declared)
		}
	}
}

// Two functions on one identity would shadow each other on the slice, with the
// survivor answering for both. Method is part of the identity, so get:x beside
// post:x is fine.
func TestCheckCollisions(t *testing.T) {
	if err := CheckCollisions([]FunctionSpec{
		{Name: "get:items"}, {Name: "post:items"}, {Name: "queue:orders"},
	}); err != nil {
		t.Errorf("distinct identities must be accepted, got: %v", err)
	}

	err := CheckCollisions([]FunctionSpec{
		{Name: "post:items"}, {Name: "post:items"},
	})
	if err == nil {
		t.Fatal("two functions on one identity must be refused")
	}
	if !strings.Contains(err.Error(), "post:items") {
		t.Errorf("the error must name the colliding identity, got: %v", err)
	}
}
