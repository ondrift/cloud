package doctor

// checkup.go — the frame of bare `drift doctor`: a fixed list of checks, what one
// answers, and the runner that asks them all.
//
// # The shape every check fills
//
// A check answers with a Result, and a Result holds named fields only: a status,
// one sentence, a registry code, and a reason when it could not ask. Nothing in it
// is a command. A red line's fix is read from the registry by the code it carries,
// so a check never types one and the registry stays the one place a remedy is
// written. Nothing is free text beyond the sentence either, so a later step that
// collects the answers can call the checklist and read fields instead of parsing
// lines.
//
// # A check that cannot answer says so
//
// The checkup runs when something is wrong, which is when a check is likeliest to
// have nothing to ask. A check that cannot answer is unknown WITH the reason, a
// check that does not apply is skipped WITH the reason, and a Result that says
// either without one is refused: a bare "unknown" is the line a person cannot act
// on, and it is the only line worse than a failure.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// Status is what a check found.
type Status string

const (
	// StatusOK means the check asked and found nothing wrong.
	StatusOK Status = "ok"
	// StatusProblem means the check asked and found something wrong.
	StatusProblem Status = "problem"
	// StatusUnknown means the check could not ask, or did not finish.
	StatusUnknown Status = "unknown"
	// StatusSkipped means the check does not apply here.
	StatusSkipped Status = "skipped"
)

// Result is what one check found.
type Result struct {
	Status Status
	// Sentence says what was found, in one sentence.
	Sentence string
	// Code is the registry code of the failure. Required on a problem; the fix
	// printed beside it is the one the registry holds for it.
	Code string
	// Reason says why the check could not ask or does not apply. Required on an
	// unknown or skipped result.
	Reason string
}

// Validate reports why a Result cannot be shown, or nil when it can.
func (r Result) Validate() error {
	switch r.Status {
	case StatusOK, StatusProblem, StatusUnknown, StatusSkipped:
	default:
		return fmt.Errorf("status %q is not one of ok, problem, unknown or skipped", r.Status)
	}
	if strings.TrimSpace(r.Sentence) == "" {
		return fmt.Errorf("a %s result has no sentence", r.Status)
	}
	switch r.Status {
	case StatusProblem:
		if _, ok := common.LookupErrorCode(r.Code); !ok {
			return fmt.Errorf("a problem carries %q, which is not a registered code", r.Code)
		}
	case StatusUnknown, StatusSkipped:
		if strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("a %s result has no reason", r.Status)
		}
	}
	return nil
}

// Check is one entry of the checklist.
type Check struct {
	// Name is what the line is about, one word.
	Name string
	// Run asks. It is given a context that ends at the check's bound and should
	// stop when it ends; one that does not is abandoned and reported as unknown.
	Run func(ctx context.Context) Result
	// Possible is every Result Run can return, one for each way it can end, built
	// without asking anything. A test walks them to hold every code a check can
	// carry to the registry and every fix to the command tree.
	Possible func() []Result
}

// Outcome is a check and what it found.
type Outcome struct {
	Name   string
	Result Result
}

// checkBound is how long one check may take before the run reports it as unknown.
// Every check is bounded, because the checkup is what a person runs while the
// platform is degraded and a check that waits on it must not hold the others.
var checkBound = 5 * time.Second

// Run asks every check at once, each under its own bound, and returns what each
// found in the order the checks are listed. A check that panics, overruns its
// bound or returns a Result that cannot be shown is unknown with a reason, and
// the others are unaffected.
func Run(ctx context.Context, checks []Check, bound time.Duration) []Outcome {
	outcomes := make([]Outcome, len(checks))
	done := make(chan struct{}, len(checks))
	for i, c := range checks {
		go func() {
			outcomes[i] = Outcome{Name: c.Name, Result: runOne(ctx, c, bound)}
			done <- struct{}{}
		}()
	}
	for range checks {
		<-done
	}
	return outcomes
}

func runOne(ctx context.Context, c Check, bound time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	answered := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				answered <- Result{Status: StatusUnknown, Sentence: "did not finish",
					Reason: fmt.Sprintf("the check panicked: %v", p)}
			}
		}()
		answered <- c.Run(ctx)
	}()

	select {
	case r := <-answered:
		if err := r.Validate(); err != nil {
			return Result{Status: StatusUnknown, Sentence: "gave an answer that cannot be shown", Reason: err.Error()}
		}
		return r
	case <-ctx.Done():
		return Result{Status: StatusUnknown, Sentence: "did not finish",
			Reason: fmt.Sprintf("it gave no answer within %s", bound)}
	}
}

// Render writes one line for each outcome, through w. A line starts with its
// status. A problem ends with its code and the fix the registry holds for that
// code, where it holds one; an unknown or skipped line ends with its reason.
func Render(w io.Writer, outcomes []Outcome) {
	for _, o := range outcomes {
		r := o.Result
		line := fmt.Sprintf("%-8s %s: %s", r.Status, o.Name, r.Sentence)
		switch r.Status {
		case StatusProblem:
			line += " (" + r.Code + ")"
			if entry, ok := common.LookupErrorCode(r.Code); ok && entry.Command != "" {
				line += "  fix: " + entry.Command
			}
		case StatusUnknown, StatusSkipped:
			line += " (" + r.Reason + ")"
		}
		fmt.Fprintln(w, line)
	}
}

// problems counts the problem lines and returns the code of the first.
func problems(outcomes []Outcome) (n int, firstCode string) {
	for _, o := range outcomes {
		if o.Result.Status != StatusProblem {
			continue
		}
		if n == 0 {
			firstCode = o.Result.Code
		}
		n++
	}
	return n, firstCode
}

// verdict is the error the checkup returns: nil with no problem line, and
// otherwise how many lines are problems under the code of the first, so main
// prints it once on stderr and exits 1.
func verdict(outcomes []Outcome) error {
	n, code := problems(outcomes)
	if n == 0 {
		return nil
	}
	noun := "problems"
	if n == 1 {
		noun = "problem"
	}
	return common.Coded(code, fmt.Errorf("drift doctor found %d %s", n, noun))
}
