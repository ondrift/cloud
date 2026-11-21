package doctor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

func fixed(name string, r Result) Check {
	return Check{Name: name, Run: func(context.Context) Result { return r }}
}

// An unknown or skipped result says why, and one that does not is refused: the
// line a person cannot act on is the one that says "unknown" and stops.
func TestResultWithoutAReasonIsRefused(t *testing.T) {
	for _, status := range []Status{StatusUnknown, StatusSkipped} {
		bare := Result{Status: status, Sentence: "could not ask"}
		if err := bare.Validate(); err == nil || !strings.Contains(err.Error(), "no reason") {
			t.Errorf("%s with no reason: %v, want a refusal saying so", status, err)
		}
		if err := (Result{Status: status, Sentence: "could not ask", Reason: "no session"}).Validate(); err != nil {
			t.Errorf("%s with a reason: %v, want it accepted", status, err)
		}
	}
}

func TestResultRefusesWhatItCannotShow(t *testing.T) {
	for name, r := range map[string]Result{
		"no status":              {Sentence: "x"},
		"an invented status":     {Status: "fine", Sentence: "x"},
		"no sentence":            {Status: StatusOK},
		"a problem with no code": {Status: StatusProblem, Sentence: "x"},
		"a problem with a code nobody registered": {Status: StatusProblem, Sentence: "x", Code: "DRIFT-9999"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := (Result{Status: StatusOK, Sentence: "fine"}).Validate(); err != nil {
		t.Errorf("an ok result: %v", err)
	}
}

// The runner holds a check that returns a result it cannot show to the same rule:
// the line printed is an unknown with the reason, not the bare status.
func TestRunReplacesAResultThatCannotBeShown(t *testing.T) {
	got := Run(context.Background(), []Check{fixed("a", Result{Status: StatusSkipped, Sentence: "nothing"})}, time.Second)
	r := got[0].Result
	if r.Status != StatusUnknown || r.Reason == "" {
		t.Errorf("a skipped result with no reason became %+v, want unknown with a reason", r)
	}
}

// A check that panics or overruns its bound is unknown "did not finish", and the
// others answer as they would have.
func TestRunIsolatesACheckThatPanicsOrOverruns(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	checks := []Check{
		fixed("first", Result{Status: StatusOK, Sentence: "fine"}),
		{Name: "panics", Run: func(context.Context) Result { panic("boom") }},
		{Name: "hangs", Run: func(context.Context) Result { <-release; return Result{Status: StatusOK, Sentence: "late"} }},
		fixed("last", Result{Status: StatusProblem, Sentence: "broken", Code: "DRIFT-1006"}),
	}
	start := time.Now()
	got := Run(context.Background(), checks, 150*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the run took %s, want it bounded by the check's 150ms", elapsed)
	}

	if len(got) != 4 {
		t.Fatalf("%d outcomes for 4 checks", len(got))
	}
	for i, name := range []string{"first", "panics", "hangs", "last"} {
		if got[i].Name != name {
			t.Errorf("outcome %d is %q, want %q: the order is the checklist's", i, got[i].Name, name)
		}
	}
	for _, i := range []int{1, 2} {
		r := got[i].Result
		if r.Status != StatusUnknown || r.Sentence != "did not finish" || r.Reason == "" {
			t.Errorf("%s: %+v, want unknown \"did not finish\" with a reason", got[i].Name, r)
		}
	}
	if got[0].Result.Status != StatusOK || got[3].Result.Status != StatusProblem {
		t.Errorf("the other lines were affected: %+v and %+v", got[0].Result, got[3].Result)
	}
}

// The error says how many lines are problems and carries the first problem's code;
// no problem is no error.
func TestVerdictCountsProblemsAndCarriesTheFirstCode(t *testing.T) {
	ok := Outcome{Name: "a", Result: Result{Status: StatusOK, Sentence: "fine"}}
	unknown := Outcome{Name: "b", Result: Result{Status: StatusUnknown, Sentence: "?", Reason: "no"}}
	if err := verdict([]Outcome{ok, unknown}); err != nil {
		t.Errorf("no problem line: %v, want nil", err)
	}

	err := verdict([]Outcome{ok,
		{Name: "c", Result: Result{Status: StatusProblem, Sentence: "x", Code: "DRIFT-1007"}},
		{Name: "d", Result: Result{Status: StatusProblem, Sentence: "y", Code: "DRIFT-1006"}},
	})
	if err == nil || !strings.Contains(err.Error(), "2 problems") {
		t.Fatalf("two problem lines: %v, want an error saying 2 problems", err)
	}
	if code := common.CodeOf(err); code != "DRIFT-1007" {
		t.Errorf("code = %q, want the first problem's DRIFT-1007", code)
	}
	if one := verdict([]Outcome{{Name: "c", Result: Result{Status: StatusProblem, Sentence: "x", Code: "DRIFT-1007"}}}); one == nil ||
		!strings.Contains(one.Error(), "1 problem") || strings.Contains(one.Error(), "1 problems") {
		t.Errorf("one problem line: %v", one)
	}
}

// A line starts with its status, an unknown or skipped one ends with its reason,
// and a problem's fix is whatever the registry holds for the code.
func TestRenderPrintsOneLineEachFromTheRegistry(t *testing.T) {
	var out bytes.Buffer
	Render(&out, []Outcome{
		{Name: "a", Result: Result{Status: StatusOK, Sentence: "fine"}},
		{Name: "b", Result: Result{Status: StatusProblem, Sentence: "slow", Code: "DRIFT-1007"}},
		{Name: "c", Result: Result{Status: StatusProblem, Sentence: "down", Code: "DRIFT-1022"}},
		{Name: "d", Result: Result{Status: StatusUnknown, Sentence: "cannot ask", Reason: "no session"}},
		{Name: "e", Result: Result{Status: StatusSkipped, Sentence: "not here", Reason: "no Driftfile"}},
	})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines for 5 checks:\n%s", len(lines), out.String())
	}
	for i, word := range []string{"ok", "problem", "problem", "unknown", "skipped"} {
		if !strings.HasPrefix(lines[i], word+" ") {
			t.Errorf("line %d = %q, want it to start with %q", i, lines[i], word)
		}
	}
	entry, _ := common.LookupErrorCode("DRIFT-1007")
	if !strings.Contains(lines[1], "DRIFT-1007") || !strings.Contains(lines[1], "fix: "+entry.Command) {
		t.Errorf("the problem line = %q, want its code and the registry's fix %q", lines[1], entry.Command)
	}
	if strings.Contains(lines[2], "fix:") {
		t.Errorf("DRIFT-1022 has no command, yet its line says %q", lines[2])
	}
	if !strings.Contains(lines[3], "no session") || !strings.Contains(lines[4], "no Driftfile") {
		t.Errorf("an unknown or skipped line must carry its reason: %q, %q", lines[3], lines[4])
	}
}

// The platform line, for every way the feed can come back.
func TestPlatformResult(t *testing.T) {
	op := common.StatusRow{Name: "API", Status: "operational"}
	for name, tc := range map[string]struct {
		feed   common.StatusFeed
		status Status
		code   string
		naming string
	}{
		"healthy":    {common.StatusFeed{Read: true, Rows: []common.StatusRow{op}}, StatusOK, "", ""},
		"degraded":   {common.StatusFeed{Read: true, Rows: []common.StatusRow{op, {Name: "Routing", Status: "degraded"}}}, StatusProblem, "DRIFT-1022", "Routing"},
		"down":       {common.StatusFeed{Read: true, Rows: []common.StatusRow{{Name: "Routing", Status: "down"}}}, StatusProblem, "DRIFT-1022", "Routing"},
		"refused":    {common.StatusFeed{Failure: common.FeedRefused, Reason: "r"}, StatusProblem, "DRIFT-1006", ""},
		"timed out":  {common.StatusFeed{Failure: common.FeedTimedOut, Reason: "r"}, StatusProblem, "DRIFT-1007", ""},
		"unreadable": {common.StatusFeed{Failure: common.FeedUnreadable, Reason: "it answered 404"}, StatusUnknown, "", ""},
		"no rows":    {common.StatusFeed{Read: true}, StatusUnknown, "", ""},
	} {
		r := platformResult(tc.feed)
		if r.Status != tc.status || r.Code != tc.code || !strings.Contains(r.Sentence, tc.naming) {
			t.Errorf("%s: %+v, want %s %s naming %q", name, r, tc.status, tc.code, tc.naming)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r := platformResult(common.StatusFeed{Failure: common.FeedUnreadable, Reason: "it answered 404"}); r.Reason != "it answered 404" {
		t.Errorf("an unreadable feed's reason is %q, want the feed's own", r.Reason)
	}
}

func TestEveryCheckHasAWayToBeWalked(t *testing.T) {
	if len(Checklist()) == 0 {
		t.Fatal("the checklist is empty, so no line prints")
	}
	for _, c := range Checklist() {
		if c.Name == "" || c.Run == nil || c.Possible == nil {
			t.Errorf("check %q is missing its name, run function or possible results", c.Name)
		}
	}
}
