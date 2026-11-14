package project

// verdict.go — what a plan concludes, as one value.
//
// A plan compares the file with the slice, and the comparison ends one of four
// ways. "Match" is a claim about every fact the plan compares, so it is made
// only for a slice the plan read in full: a fact it could not read is not a fact
// that agrees. The header and the closing line of `drift plan`, and the status
// `drift plan --exit-code` exits with, are decided here, and nothing else restates
// the conclusion.

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// VerdictState is what a plan concludes about a slice and its file.
type VerdictState string

const (
	// VerdictCreate: the slice does not exist, and applying creates it.
	VerdictCreate VerdictState = "create"
	// VerdictDiffers: the slice exists and a line of the plan carries a mark:
	// applying adds, changes or removes something, or refuses.
	VerdictDiffers VerdictState = "differs"
	// VerdictUnread: no line carries a mark, but a fact the plan compares could
	// not be read, so it cannot say that the slice is the file. A fact is unread
	// when the platform or the build was asked for it and gave no answer. A value
	// that is write-only by design (a secret, an API key, a gate password) is never
	// read back, so the plan not having it is not unread.
	VerdictUnread VerdictState = "unread"
	// VerdictMatch: no line carries a mark and every fact was read.
	VerdictMatch VerdictState = "match"
)

// header is the status the plan's slice line carries.
func (s VerdictState) header() string {
	switch s {
	case VerdictCreate:
		return "+ create"
	case VerdictDiffers:
		return "~ change"
	case VerdictUnread:
		return "unchanged (not fully read)"
	}
	return "unchanged"
}

// Verdict is what a plan concludes, for a reader that is not the plan's printed
// text.
type Verdict struct {
	State VerdictState
	// Changes is one entry for each marked line of the plan: the mark, the
	// address and what applying does there.
	Changes []string
	// Unread is one entry for each fact the plan could not read, with what it is
	// and why.
	Unread []string
	// Attention is what a person has to do before the slice is the file, though
	// the plan has no difference to show for it. A plan that reports no such fact
	// leaves it empty.
	Attention []string
}

// The statuses `drift plan --exit-code` exits with besides 0 for a match. A plan
// that fails exits 1 like every command that fails, and that status is not here.
const (
	exitStatusDiffers = 2
	exitStatusUnread  = 3
)

// exitError is what `drift plan --exit-code` returns for this verdict: nil for a
// match, and for the others an error that carries the status a script reads and
// says why in words. A verdict that is none of the four states is a failure and
// never a match.
func (v Verdict) exitError() error {
	switch v.State {
	case VerdictMatch:
		return nil
	case VerdictCreate:
		return &common.ExitStatusError{Status: exitStatusDiffers, Reason: "the slice does not exist yet, and applying the Driftfile creates it"}
	case VerdictDiffers:
		return &common.ExitStatusError{Status: exitStatusDiffers, Reason: "the slice differs from the Driftfile"}
	case VerdictUnread:
		return &common.ExitStatusError{Status: exitStatusUnread, Reason: "part of the slice could not be read, so it is not known to match the Driftfile"}
	}
	return errors.New("the plan reached no verdict")
}

// PlanVerdict is what `drift plan` concludes about the Driftfile in the working
// directory and the slice it names, as the value a command in another package
// reads, without the plan being printed.
//
// It reads what `drift plan` reads, by the same path: it sets the variables the
// file reads in the process environment, refreshes the Driftfile schema, compiles
// the file and reads the live slice. What it says on the way (the variables it
// set, the environment it plans) goes to out, so a caller that hands it io.Discard
// prints nothing of its own. The compiler's warnings about a queue no block
// declares and the wait for a slice that is warming up go to stderr whatever out
// is.
//
// It needs a session and the network. A caller without them gets the error and a
// zero Verdict: a verdict is a conclusion about a slice that was read, and a
// zero Verdict is none of its four states.
//
// The slice the reads name is the Driftfile's, whichever slice the caller has
// active, and the caller's active slice is its own again when PlanVerdict returns.
func PlanVerdict(o PlanOptions, out io.Writer) (Verdict, error) {
	active, set := os.LookupEnv(common.SliceEnv)
	defer func() {
		if set {
			_ = os.Setenv(common.SliceEnv, active)
		} else {
			_ = os.Unsetenv(common.SliceEnv)
		}
	}()

	p, _, err := readPlan(o, out, out)
	if err != nil {
		return Verdict{}, err
	}
	return p.verdict(p.diff()), nil
}

// verdict is the conclusion of the plan whose diff is d.
func (p *planState) verdict(d *planDiff) Verdict {
	v := Verdict{State: VerdictMatch, Unread: d.unreadLines()}
	for _, l := range d.lines {
		if l.mark != " " {
			v.Changes = append(v.Changes, l.mark+" "+l.address+"  "+l.detail)
		}
	}
	switch {
	case p.live == nil:
		v.State = VerdictCreate
	case d.changed:
		v.State = VerdictDiffers
	case len(d.unreadFacts) > 0:
		v.State = VerdictUnread
	}
	return v
}

// unreadLines is one line for each fact the plan could not read: what it is, the
// addresses it was read for, and why, on one line. The same fact with the same
// cause at several addresses is one line; a fact that sits at an address of its
// own name says it once.
func (d *planDiff) unreadLines() []string {
	type fact struct {
		what, reason string
		addresses    []string
	}
	var facts []*fact
	for _, f := range d.unreadFacts {
		reason := ""
		if f.err != nil {
			reason, _, _ = strings.Cut(f.err.Error(), "\n")
		}
		var g *fact
		for _, x := range facts {
			if x.what == f.what && x.reason == reason {
				g = x
				break
			}
		}
		if g == nil {
			g = &fact{what: f.what, reason: reason}
			facts = append(facts, g)
		}
		if !slices.Contains(g.addresses, f.address) {
			g.addresses = append(g.addresses, f.address)
		}
	}

	lines := make([]string, 0, len(facts))
	for _, g := range facts {
		subject := g.what
		if len(g.addresses) != 1 || g.addresses[0] != g.what {
			subject = strings.Join(g.addresses, ", ") + ": " + g.what
		}
		line := subject + " could not be read"
		if g.reason != "" {
			line += " (" + g.reason + ")"
		}
		lines = append(lines, line)
	}
	return lines
}
