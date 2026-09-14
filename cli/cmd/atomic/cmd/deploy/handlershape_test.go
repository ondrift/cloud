package atomic_cmd

import (
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
// else in the element is the user's own bug, and claiming it is the entry point's
// shape points them at the wrong file.
func TestHandlerShapeHint_OnlyFiresForTheFunctionBeingBuilt(t *testing.T) {
	other := `./lib.go:9:2: assignment mismatch: 2 variables but helper returns 1 value`
	if h := handlerShapeHint("Head", other); h != "" {
		t.Errorf("a mismatch in unrelated code was blamed on the handler:\n%s", h)
	}
}
