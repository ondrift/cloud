package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a Go handler of the wrong shape is told.
//
// The generated entry point calls the handler as
// `status, message, payload, headers := Name(req)`, so any other shape fails as
// `assignment mismatch: 4 variables but Name returns 1 value` — in a main.go the
// author never wrote and cannot open. The shape it collides with most is the
// SDK's own `func(drift.Request) drift.Response`, which is what `drift.Run`
// takes and what every handler in Drift's platform repo is.

const mismatch = `# audit
./main.go:41:40: assignment mismatch: 4 variables but Head returns 1 value`

// THE ONE THAT MATTERS: the hint has to name the drift.Response case, because
// that is the shape someone arrives with, and the compiler's message gives them
// no way to discover that a second convention exists.
func TestHandlerShapeHint_NamesTheResponseShapeAndTheWayOut(t *testing.T) {
	hint := handlerShapeHint("Head", mismatch)
	if hint == "" {
		t.Fatal("an assignment mismatch on the handler produced no hint at all")
	}
	for _, want := range []string{
		"drift.Response", // the shape they have
		"drift.Run",      // where they got it
		"handler:",       // where the fix is declared
		"r.Status",       // the unpacking, spelled out
		"Head",           // their function, by name
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint never mentions %q:\n%s", want, hint)
		}
	}
}

// THE CONTROL, and the reason the match is narrow. An ordinary compile error in
// the user's own code must not be dressed up as a signature problem — that would
// send someone rewriting a correct handler to chase a mistake they did not make.
func TestHandlerShapeHint_StaysSilentOnAnOrdinaryBuildError(t *testing.T) {
	for _, out := range []string{
		"./ingest.go:12:2: undefined: fmt",
		"./verify.go:7:1: syntax error: unexpected }",
		"# audit\n./record.go:40:9: cannot use x (variable of type int) as string value",
		"",
	} {
		if h := handlerShapeHint("Head", out); h != "" {
			t.Errorf("a plain build error produced a signature hint:\n  build: %q\n  hint: %s", out, h)
		}
	}
}

// The mismatch has to be about THIS handler. An assignment mismatch somewhere
// else in the backend is the user's own bug, and claiming it is the entry point's
// shape points them at the wrong file.
func TestHandlerShapeHint_OnlyFiresForTheFunctionBeingBuilt(t *testing.T) {
	other := `./lib.go:9:2: assignment mismatch: 2 variables but helper returns 1 value`
	if h := handlerShapeHint("Head", other); h != "" {
		t.Errorf("a mismatch in unrelated code was blamed on the handler:\n%s", h)
	}
	for _, out := range []string{
		"./lib.go:9:2: not enough arguments in call to helper",
		"./lib.go:9:2: cannot use x (variable of type int) as string value in argument to helper",
	} {
		if h := handlerShapeHint("Head", out); h != "" {
			t.Errorf("a bad call in unrelated code was blamed on the handler:\n  build: %s\n  hint: %s", out, h)
		}
	}
}

// A handler whose arguments do not fit the call the generated main makes is a
// shape mismatch too, and the hint names the context-first shapes beside the
// two it always named, since a handler half-converted to take a context is the
// likeliest way to get here now.
func TestHandlerShapeHint_FiresOnAWrongArgumentListAndNamesTheContextShapes(t *testing.T) {
	for _, out := range []string{
		"./main.go:16:44: not enough arguments in call to PostRPC\n\thave (context.Context, Body)\n\twant (context.Context, Body, drift.Request)",
		"./main.go:16:44: too many arguments in call to PostRPC",
		"./main.go:16:48: cannot use body (variable of type Body) as context.Context value in argument to PostRPC",
	} {
		hint := handlerShapeHint("PostRPC", out)
		if hint == "" {
			t.Errorf("no hint for a call that does not fit the handler:\n%s", out)
			continue
		}
		for _, want := range []string{
			"func PostRPC(ctx context.Context, req drift.Request)",
			"func PostRPC(ctx context.Context, body map[string]any, req drift.Request)",
			"func PostRPC(body map[string]any, req drift.Request)",
		} {
			if !strings.Contains(hint, want) {
				t.Errorf("the hint does not name %q:\n%s", want, hint)
			}
		}
	}
}

// writeHandler writes one Go source file declaring a handler and returns its
// directory.
func writeHandler(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main\n\n"+src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The four handler shapes, read the way the body type always was: the body is
// the first parameter after a leading context.Context, and only a first
// parameter of that type says the handler takes the call's context.
func TestGoHandlerShape_ReadsEveryShape(t *testing.T) {
	for name, tc := range map[string]struct {
		src      string
		body     string
		takesCtx bool
	}{
		"body":         {"func H(body LoginBody, req drift.Request) (int, string, any, map[string]string)", "LoginBody", false},
		"request only": {"func H(req drift.Request) (int, string, any, map[string]string)", "drift.Request", false},
		"ctx, body":    {"func H(ctx context.Context, body LoginBody, req drift.Request) (int, string, any, map[string]string)", "LoginBody", true},
		"ctx, request": {"func H(ctx context.Context, req drift.Request) (int, string, any, map[string]string)", "drift.Request", true},
		"unnamed ctx":  {"func H(_ context.Context, body map[string]any, req drift.Request) (int, string, any, map[string]string)", "map[string]any", true},
		"over lines": {"func H(\n\tctx context.Context,\n\tbody json.RawMessage,\n\treq drift.Request,\n) (int, string, any, map[string]string)",
			"json.RawMessage", true},
		"a body named ctx": {"func H(ctx LoginBody, req drift.Request) (int, string, any, map[string]string)", "LoginBody", false},
	} {
		t.Run(name, func(t *testing.T) {
			body, takesCtx := GoHandlerShape(writeHandler(t, tc.src+" {\n\treturn 200, \"\", nil, nil\n}\n"), "H")
			if body != tc.body || takesCtx != tc.takesCtx {
				t.Errorf("GoHandlerShape = (%q, %v), want (%q, %v)", body, takesCtx, tc.body, tc.takesCtx)
			}
		})
	}
}

// A handler it cannot find keeps the historical fallback and no context, so
// the main renders exactly as it did; and the generated main.go is never read
// as the user's declaration.
func TestGoHandlerShape_FallsBackWhereItCannotRead(t *testing.T) {
	dir := writeHandler(t, "func Other(body LoginBody, req drift.Request) {}\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc H(ctx context.Context, body Wrong, req drift.Request) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body, takesCtx := GoHandlerShape(dir, "H"); body != "RequestBody" || takesCtx {
		t.Errorf("GoHandlerShape = (%q, %v), want the fallback (\"RequestBody\", false)", body, takesCtx)
	}
}
