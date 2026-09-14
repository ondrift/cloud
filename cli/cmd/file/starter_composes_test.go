package file

import (
	"strings"
	"testing"

	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
)

// The starter Driftfile declares a function, and NOTHING ON DISK DEFINES IT.
// That is correct — a Driftfile declares functions, it does not create them —
// and it is why `drift file new` followed by `drift file apply` used to
// dead-end on a new user's second command, with a handler-resolution error
// naming the missing callable rather than the command that writes it.
//
// This is the test that keeps the two halves able to compose. The scaffolded
// entry's `handler:` must be exactly what `drift atomic new` produces when run
// with the flags the scaffolder prints beside it — same route, same method,
// same language — and that is checked against the REAL derivation rather than
// against a restated "GetHello", because a literal agrees with the rule until
// the day the rule changes.
func TestStarterEntryAndItsCommandAgree(t *testing.T) {
	df := starterDriftfile("acme", "")
	cmd := starterFnCommand()

	// The command must name the same three values the entry is built from,
	// because those are what decide the handler.
	for _, want := range []string{
		"drift atomic new " + starterRoute,
		"-l " + starterLang,
		"-m " + starterMethod,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the printed command %q does not carry %q — it would scaffold a different function than the Driftfile declares", cmd, want)
		}
	}

	// And the entry must name the handler that command will actually write.
	handler := atomic_cmd_new.HandlerName(starterMethod, starterRoute, starterLang)
	for _, want := range []string{
		"route: " + starterRoute,
		"method: " + starterMethod,
		"handler: " + handler,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("the starter Driftfile does not contain %q\n%s", want, df)
		}
	}

	// The sharp end: run the scaffolder's own naming rule over the command's
	// flags and require the Driftfile to have named the result. If someone
	// changes how handlers are derived, or edits one of the three constants,
	// this fails here rather than as a handler-resolution error in a user's
	// first hour.
	if !strings.Contains(df, "handler: "+handler) {
		t.Fatalf("the Driftfile declares a handler that `%s` does not produce — "+
			"the two scaffolders have stopped composing, which is the exact "+
			"dead-end this pair exists to prevent", cmd)
	}
}

// A user reading the output must be told the command, not left to deduce it.
func TestStarterOutputNamesTheCommandThatCompletesIt(t *testing.T) {
	cmd := starterFnCommand()
	if !strings.HasPrefix(cmd, "drift atomic new ") {
		t.Fatalf("the hint %q is not a runnable command", cmd)
	}
	// A bare `drift atomic new hello` would scaffold an interactive prompt
	// rather than the declared function: both flags have to be there or the
	// hint is not the one command that completes the file.
	if !strings.Contains(cmd, " -l ") || !strings.Contains(cmd, " -m ") {
		t.Errorf("the hint %q omits a flag, so following it prompts instead of "+
			"scaffolding the declared function", cmd)
	}
}
