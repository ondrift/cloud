package common

import (
	"strings"
	"testing"
	"time"
)

// A 422 whose refused_by names the start check renders what was asked, that
// the new version never replaced the one that was running, the process's own
// failure, and what still serves — with the new code on its own line.
func TestAPIError_StartCheckRefusal_RendersWithStillRunning(t *testing.T) {
	deployedAt := time.Now().Add(-10 * time.Minute)
	body := `{"error":"deploy refused: the new version did not start: it exited with status 1. ` +
		`The previous version is still in place.","refused_by":"start_check",` +
		`"detail":"Traceback (most recent call last):\n  File \"app.py\", line 3, in <module>\n` +
		`ModuleNotFoundError: No module named 'payments'\n",` +
		`"still_running":{"digest":"sha256:abcdef123456789","deployed_at":"` + deployedAt.Format(time.RFC3339Nano) + `"}}`

	e := refusalFrom(t, 422, "", body)
	got := e.Error()

	for _, want := range []string{
		"Couldn't delete function",
		"did not replace the version that was running",
		"No module named 'payments'",
		"Still running: abcdef123456",
		"ago.",
		"\n  DRIFT-1023 · drift doctor explain DRIFT-1023",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering does not say %q:\n%s", want, got)
		}
	}
	if e.code() != "DRIFT-1023" {
		t.Errorf("code() = %q, want DRIFT-1023", e.code())
	}
	if _, ok := LookupErrorCode(e.code()); !ok {
		t.Errorf("%s is rendered but not registered, so `doctor explain` cannot answer it", e.code())
	}
	// The code sits on its own line, last.
	lines := strings.Split(got, "\n")
	if lines[len(lines)-1] != "  DRIFT-1023 · drift doctor explain DRIFT-1023" {
		t.Errorf("the code is not on its own last line:\n%s", got)
	}
}

// A first deploy that is refused has nothing still running, and says so rather
// than guessing a digest or an age out of nothing.
func TestAPIError_StartCheckRefusal_FirstDeploySaysNothingWasRunning(t *testing.T) {
	body := `{"error":"deploy refused: the new version did not start: it exited with status 1.",` +
		`"refused_by":"start_check","detail":"boom\n","still_running":null}`

	e := refusalFrom(t, 422, "", body)
	got := e.Error()

	if !strings.Contains(got, "Nothing was running before this deploy.") {
		t.Errorf("a first deploy's refusal must say nothing was running:\n%s", got)
	}
	if strings.Contains(got, "Still running:") {
		t.Errorf("a first deploy's refusal must not invent a still-running version:\n%s", got)
	}
}

// The code comes from the refused_by FIELD, never from the bare 422 status:
// 422 answers other refusals too, and a start-check refusal with no
// refused_by field (an older platform, or a different gate) keeps the
// ordinary 4xx code.
func TestAPIError_StartCheckRefusal_CodeComesFromTheFieldNotTheStatus(t *testing.T) {
	e := refusalFrom(t, 422, "", `{"error":"the request body is not valid JSON"}`)
	if e.code() != "DRIFT-1016" {
		t.Errorf("code() = %q, want the ordinary 4xx code DRIFT-1016 for a 422 with no refused_by", e.code())
	}
	if strings.Contains(e.Error(), "DRIFT-1023") {
		t.Errorf("a 422 with no refused_by must never render as the start-check code:\n%s", e.Error())
	}
}

// Never from wording: a sentence that talks about a "start check" with no
// refused_by field is not read as one.
func TestAPIError_StartCheckRefusal_NeverFromWording(t *testing.T) {
	e := refusalFrom(t, 422, "", `{"error":"the start check gate refused this, allegedly"}`)
	if e.code() == "DRIFT-1023" {
		t.Errorf("the code must come from refused_by, not from the word \"start check\" in the sentence")
	}
}
