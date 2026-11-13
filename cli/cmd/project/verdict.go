package project

// verdict.go — what a plan concludes, as one value.
//
// A plan compares the file with the slice, and the comparison ends one of four
// ways. "Match" is a claim about every fact the plan compares, so it is made
// only for a slice the plan read in full: a fact it could not read is not a fact
// that agrees. The header and the closing line of `drift plan` are decided here,
// and nothing else restates the conclusion.

import (
	"slices"
	"strings"
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
	// not be read, so it cannot say that the slice is the file.
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
