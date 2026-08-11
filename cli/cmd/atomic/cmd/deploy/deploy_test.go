package atomic_cmd

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// The generated wrapper has to be valid Go. It is written into a user's build
// directory and compiled there, so a broken template surfaces as a compiler
// error inside a build the user never asked about — with no hint that the fault
// is ours. Parsing it here moves that failure to us.
func TestGeneratedNativeWrapperParses(t *testing.T) {
	for _, method := range []string{"get", "post", "put", "delete", "patch", "queue"} {
		dir := t.TempDir()
		if err := generateMain(dir, "Handle", method); err != nil {
			t.Fatalf("%s: generateMain: %v", method, err)
		}
		src, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "main.go", src, parser.AllErrors); err != nil {
			t.Errorf("%s: the generated wrapper is not valid Go: %v\n%s", method, err, src)
		}
		if bytes.Contains(src, []byte("{{")) {
			t.Errorf("%s: an unreplaced placeholder survived:\n%s", method, src)
		}
	}
}

// The wrapper serves more than one call, which is the entire point of warming.
// A template that decoded once would pass a parse check and then answer exactly
// one request per process, silently paying the sandbox install on every call.
func TestGeneratedNativeWrapperLoops(t *testing.T) {
	dir := t.TempDir()
	if err := generateMain(dir, "Handle", "post"); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"for {",           // it loops
		"json.NewDecoder", // over a stream, not one Decode
		"rss_bytes",       // and reports what it has reached
		"TMPDIR",          // pointing the runtime at this call's scratch
	} {
		if !bytes.Contains(src, []byte(want)) {
			t.Errorf("the generated wrapper is missing %q:\n%s", want, src)
		}
	}
}

// Only the language whose wrapper actually loops may claim it. Telling the slice
// "loop" about a one-shot artifact hangs its second call forever, so anything
// unrecognised has to answer with the empty string.
func TestOnlyNativeDeclaresTheLoopProtocol(t *testing.T) {
	if got := invocationProtocol("native"); got != "loop" {
		t.Errorf("native protocol = %q, want loop", got)
	}
	for _, lang := range []string{"python", "node", "ruby", "php", "rust", "", "golang", "NATIVE"} {
		if got := invocationProtocol(lang); got != "" {
			t.Errorf("%s declared %q — its wrapper hands the stdin cycle to the SDK, which "+
				"still reads one envelope and exits", lang, got)
		}
	}
}
