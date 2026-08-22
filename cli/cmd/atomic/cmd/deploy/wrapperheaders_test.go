package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A handler must be able to set response headers, because that is the whole of
// the raw-response escape hatch.
//
// The slice decides the shape of what it writes from the function's own
// Content-Type: one that is not JSON means `payload` is base64 bytes to write
// directly, and anything else gets the {status, message, payload} envelope
// (`Atomic::build_func_response`). That branch is shared by the native and
// langserver paths alike, so nothing in the slice is Go-specific — a handler
// that cannot name a header simply cannot reach it. That is what stopped a Node
// function serving an OpenID4VP request object a wallet could parse.
//
// The wrapper was the only thing in the way: it destructured three elements and
// rebuilt three keys, so the header never left the function.
//
// Asserted on the generated source rather than by running it, because no
// interpreter is assumed present here — a test that skips when node is missing
// reports the same `ok` as one that ran. The behaviour is proven by deploying
// against a real slice.
func TestNodeWrapperForwardsHandlerHeaders(t *testing.T) {
	for _, method := range []string{"get", "post"} {
		dir := t.TempDir()
		if err := generateNodeWrapper(dir, "fn", "Handle", method); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		src := readFile(t, filepath.Join(dir, "app.js"))

		// The fourth element exists and reaches the response.
		for _, want := range []string{
			", headers]", // destructured off the handler's return
			"out.headers = headers",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s: the generated wrapper is missing %q, so a handler cannot set "+
					"a header and the raw-response hatch is unreachable:\n%s", method, want, src)
			}
		}

		// And it is OPTIONAL, which is what keeps this additive: every handler
		// already written returns three elements, and an unconditional
		// assignment would put an empty headers key on every one of them.
		if !strings.Contains(src, "if (headers)") {
			t.Errorf("%s: headers are set unconditionally — a three-element return would "+
				"then carry an undefined headers key, changing every existing handler:\n%s",
				method, src)
		}

		if strings.Contains(src, "{{") {
			t.Errorf("%s: an unreplaced placeholder survived:\n%s", method, src)
		}
	}
}

// Python unpacking is STRICT on arity, which is why its wrapper indexes instead.
//
// `status, message, payload = f(req)` against a four-element return raises
// ValueError, so widening the contract by unpacking a fourth name would break
// every handler that returns three — the opposite of additive. Indexing with a
// length check is what makes the element optional.
func TestPythonWrapperForwardsHandlerHeaders(t *testing.T) {
	for _, method := range []string{"get", "post"} {
		dir := t.TempDir()
		if err := generatePythonWrapper(dir, "fn", "Handle", method); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		src := readFile(t, filepath.Join(dir, "app.py"))

		for _, want := range []string{
			`len(result) > 3`, // optional, and checked before it is read
			`out["headers"]`,  // and it reaches the response
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s: the generated wrapper is missing %q, so a handler cannot set "+
					"a header and the raw-response hatch is unreachable:\n%s", method, want, src)
			}
		}

		// The strict-unpack form is what this replaces. If it comes back, a
		// four-element return raises ValueError inside the user's function and
		// reads as their bug.
		if strings.Contains(src, "status, message, payload = ") {
			t.Errorf("%s: the wrapper unpacks a fixed three, so a fourth element raises "+
				"ValueError rather than being ignored:\n%s", method, src)
		}

		if strings.Contains(src, "{{") {
			t.Errorf("%s: an unreplaced placeholder survived:\n%s", method, src)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
