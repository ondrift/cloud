package atomic_common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A directory's language follows from what is in it. Nothing in the source has
// to declare it, and no comment marks the file that counts.
func TestElementLanguage(t *testing.T) {
	tests := []struct {
		filename  string
		content   string
		want      string
		wantBuild string
	}{
		{"main.go", "package main\n\nfunc GetHello(req any) {}\n", "go", "native"},
		{"app.py", "def get_hello(req): pass\n", "python", "python"},
		{"app.js", "function getHello(req) {}\n", "node", "node"},
		{"app.rb", "def get_hello(req); end\n", "ruby", "ruby"},
		{"app.php", "<?php\nfunction getHello($req) {}\n", "php", "php"},
		{"app.rs", "pub fn get_hello(req: Value) {}\n", "rust", "rust"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.filename, tt.content)

			lang, err := ElementLanguage(dir)
			if err != nil {
				t.Fatalf("ElementLanguage: %v", err)
			}
			if lang != tt.want {
				t.Fatalf("lang: got %q, want %q", lang, tt.want)
			}

			// Go compiles to a binary the slice runs directly, so the build and
			// the operator call it "native" while the parser calls it "go".
			build, file, derr := DetectLanguage(dir)
			if derr != nil {
				t.Fatalf("DetectLanguage: %v", derr)
			}
			if build != tt.wantBuild {
				t.Fatalf("build language: got %q, want %q", build, tt.wantBuild)
			}
			if file != tt.filename {
				t.Fatalf("file: got %q, want %q", file, tt.filename)
			}
		})
	}
}

// An element with no source cannot serve the functions the manifest declares
// there, and saying so beats a build that fails with "no input files".
func TestElementLanguage_EmptyDir(t *testing.T) {
	_, err := ElementLanguage(t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a directory with no source")
	}
}

// One element, one language: it has a single dependency manifest and a single
// runtime, so a directory holding two is refused rather than half-built.
func TestElementLanguage_MixedIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package main\n\nfunc GetA(req any) {}\n")
	write(t, dir, "b.py", "def get_b(req): pass\n")

	_, err := ElementLanguage(dir)
	if err == nil {
		t.Fatal("a mixed-language element must be refused")
	}
	if !strings.Contains(err.Error(), "element") {
		t.Errorf("the error should explain the element rule, got: %v", err)
	}
}

// FindCallable answers the one question the Driftfile cannot: which file holds
// the handler it named.
func TestFindCallable(t *testing.T) {
	tests := []struct {
		lang, filename, content, handler string
	}{
		{"go", "users.go", "package main\n\nfunc PostUsers(b any, r any) {}\n", "PostUsers"},
		{"python", "users.py", "def post_users(body, req): pass\n", "post_users"},
		{"node", "users.js", "async function postUsers(body, req) {}\n", "postUsers"},
		{"ruby", "users.rb", "def valid?(req)\nend\n", "valid?"},
		{"php", "users.php", "<?php\nfunction postUsers($body) {}\n", "postUsers"},
		{"rust", "users.rs", "pub fn post_users(b: Value) {}\n", "post_users"},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.filename, tt.content)

			c, err := FindCallable(dir, tt.handler)
			if err != nil {
				t.Fatalf("FindCallable: %v", err)
			}
			if c.SourceFile != tt.filename {
				t.Errorf("source file: got %q, want %q", c.SourceFile, tt.filename)
			}
			if c.Language != tt.lang {
				t.Errorf("language: got %q, want %q", c.Language, tt.lang)
			}
			if c.Handler != tt.handler {
				t.Errorf("handler: got %q, want %q", c.Handler, tt.handler)
			}
		})
	}
}

// A handler must be findable from outside its own file, because the generated
// entry point imports it by name. Where a language marks that, the mark is
// required — and the error has to say which mark, or the reader is left staring
// at a function that is plainly there.
func TestFindCallable_UnexportedIsNotAHandler(t *testing.T) {
	cases := []struct{ name, file, content, handler, wantShape string }{
		{"go lowercase", "a.go", "package main\n\nfunc postUsers(b any) {}\n", "postUsers", "capital first letter"},
		{"rust private", "a.rs", "fn post_users(b: Value) {}\n", "post_users", "must be pub"},
		{"node arrow", "a.js", "const postUsers = (b) => {};\n", "postUsers", "not an arrow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.file, c.content)

			_, err := FindCallable(dir, c.handler)
			if err == nil {
				t.Fatal("a callable the entry point cannot import must not bind")
			}
			if !strings.Contains(err.Error(), c.wantShape) {
				t.Errorf("the error must name the shape a handler takes (%q), got: %v", c.wantShape, err)
			}
		})
	}
}

// A helper is any callable the manifest does not name. It costs nothing, is
// never routed, and sharing a file with a handler changes neither fact.
func TestFindCallable_HelpersAreIgnored(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "users.go", "package main\n\n"+
		"func Validate(b any) bool { return true }\n\n"+
		"func PostUsers(b any, r any) {}\n\n"+
		"func Normalise(s string) string { return s }\n")

	c, err := FindCallable(dir, "PostUsers")
	if err != nil {
		t.Fatalf("FindCallable: %v", err)
	}
	if c.SourceFile != "users.go" {
		t.Errorf("source file: got %q, want users.go", c.SourceFile)
	}
}

// The search is scoped to ONE element directory, so two elements may each have
// a handler of the same name — the common case for `handle` or `index`.
func TestFindCallable_ScopedToItsElement(t *testing.T) {
	root := t.TempDir()
	billing := filepath.Join(root, "billing")
	if err := os.MkdirAll(billing, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.go", "package main\n\nfunc Handle(req any) {}\n")
	write(t, billing, "b.go", "package main\n\nfunc Handle(req any) {}\n")

	for _, dir := range []string{root, billing} {
		if _, err := FindCallable(dir, "Handle"); err != nil {
			t.Errorf("Handle should resolve within %s: %v", dir, err)
		}
	}
}

// Within one element the name has to be unique: the entry point imports it by
// name and cannot say which was meant.
func TestFindCallable_DuplicateInOneElement(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package main\n\nfunc Handle(req any) {}\n")
	write(t, dir, "b.go", "package main\n\nfunc Handle(req any) {}\n")

	_, err := FindCallable(dir, "Handle")
	if err == nil {
		t.Fatal("a duplicate handler must be refused")
	}
	if !strings.Contains(err.Error(), "a.go") || !strings.Contains(err.Error(), "b.go") {
		t.Errorf("the error must name both files, got: %v", err)
	}
}
