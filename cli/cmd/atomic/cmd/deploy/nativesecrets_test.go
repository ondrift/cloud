package atomic_cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE GENERATED GO WRAPPER RE-POINTS DRIFT_SECRET_* ON EVERY CALL.
//
// A Go function is the only one the slice keeps WARM through a wrapper of its
// own: the slice sets DRIFT_SECRET_<NAME> once when the worker spawns, and that
// process then serves up to MAX_SERVES (200) more calls, or until MAX_IDLE
// retires it. The SDK's accessor reads the environment, so a secret rotated in
// between kept serving its pre-rotation value for every one of those calls —
// silently, and for as long as traffic kept the worker alive.
//
// Nothing else has this shape. Python and Node reach a persistent language
// server that already re-injects per call (langserver.rs), and Rust, Ruby and
// PHP are spawned one-shot so they re-fetch by construction. Go was the gap.
//
// The slice was already sending the current values in every envelope. All that
// was missing was the wrapper reading them, which is what this pins.
func TestNativeWrapperRepointsSecretsPerCall(t *testing.T) {
	for _, method := range []string{"get", "post"} {
		src := renderNativeWrapper(t, method)

		for _, want := range []string{
			// Read off the per-call envelope, not the process environment.
			// Matched on the json tags rather than the field declarations,
			// because gofmt aligns the latter and the alignment moves whenever
			// a field is added — a test that broke on that would be about
			// whitespace rather than about secrets.
			`json:"secrets"`,
			`json:"secrets_previous"`,
			// And written where the SDK's accessors look.
			`os.Setenv("DRIFT_SECRET_"+strings.ToUpper(k), v)`,
			`os.Setenv("DRIFT_SECRET_"+strings.ToUpper(k)+"_PREVIOUS", v)`,
			// A name the envelope stopped carrying is REMOVED. A secret deleted
			// from the store has to stop being readable, and a leftover variable
			// is indistinguishable from a live one to the accessor. The same
			// holds for a grace window that has closed.
			`os.Unsetenv("DRIFT_SECRET_" + strings.ToUpper(k))`,
			`os.Unsetenv("DRIFT_SECRET_" + strings.ToUpper(k) + "_PREVIOUS")`,
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s: the generated wrapper is missing %q, so a warm Go worker "+
					"serves a rotated secret's OLD value for up to 200 calls:\n%s",
					method, want, src)
			}
		}

		if strings.Contains(src, "{{") {
			t.Errorf("%s: an unreplaced placeholder survived:\n%s", method, src)
		}
	}
}

// THE WRAPPER'S IMPORTS MUST MATCH WHAT IT USES, which parsing does not check.
//
// `TestGeneratedNativeWrapperParses` next door already proves the rendered file
// is valid Go for all six method shapes. That is not the same question: a file
// that uses `strings.ToUpper` and does not import `strings` parses perfectly and
// fails to build — as does one that imports a package it stopped using. Either
// breaks EVERY Go deploy on the platform, at the tenant's build step, pointing
// at generated code they never wrote.
//
// Adding `strings` for the secret injection above is exactly that hazard, which
// is why the check is here rather than left to a reviewer noticing.
func TestNativeWrapperImportsExactlyWhatItUses(t *testing.T) {
	for _, method := range []string{"get", "post"} {
		src := renderNativeWrapper(t, method)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "main.go", src, parser.AllErrors)
		if err != nil {
			t.Fatalf("%s: %v", method, err) // shape is pinned next door
		}

		imported := map[string]bool{}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			name := path[strings.LastIndex(path, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imported[name] = true
		}

		// Every package selector the body reaches for must be imported. This is
		// the check that catches "used `strings`, forgot the import" — which
		// parses cleanly and fails to build.
		used := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Obj == nil {
				used[ident.Name] = true
			}
			return true
		})
		for pkg := range used {
			// A local identifier can look like a package selector; only flag the
			// ones the wrapper is known to reach for.
			switch pkg {
			case "os", "json", "filepath", "strings", "syscall", "drift":
				if !imported[pkg] {
					t.Errorf("%s: the wrapper uses %s.* and does not import it — this "+
						"parses and does not build:\n%s", method, pkg, src)
				}
			}
		}

		// And nothing imported goes unused, which is equally a build failure.
		for name := range imported {
			if !used[name] {
				t.Errorf("%s: %q is imported and never used — Go refuses to build that:\n%s",
					method, name, src)
			}
		}
	}
}

// renderNativeWrapper produces the main.go a Go function of this method shape
// would be built from, through generateMain itself rather than a copy of what
// it does.
func renderNativeWrapper(t *testing.T, method string) string {
	t.Helper()
	dir := t.TempDir()
	// A handler for goBodyType to read, so the POST shape binds a real type
	// rather than falling back.
	handler := "package main\n\ntype LoginBody struct{}\n\n" +
		"func Handle(body LoginBody, req drift.Request) (int, string, any, map[string]string) {\n" +
		"\treturn 200, \"\", nil, nil\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := generateMain(dir, "Handle", method); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return readFile(t, filepath.Join(dir, "main.go"))
}
