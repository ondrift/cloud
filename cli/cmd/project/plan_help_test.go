package project

import (
	"strings"
	"testing"
)

// The plan's help says what each way a plan ends means, in the words the plan
// prints, so someone reading the help and someone reading a plan agree on what
// "unread" is.
func TestPlan_HelpSaysWhatEachVerdictMeans(t *testing.T) {
	help := GetPlanCmd().Long

	for _, c := range verdictCases() {
		if !strings.Contains(help, "\n  "+c.name+" ") {
			t.Errorf("the plan's help does not say what %q means:\n%s", c.name, help)
		}
	}
	// The last lines of a match and of an unread plan are quoted, so a change to
	// either has to change the help with it.
	for _, line := range []string{closingMatch, closingUnread} {
		if !strings.Contains(help, `"`+line+`"`) {
			t.Errorf("the plan's help does not quote the line %q that a plan ends on:\n%s", line, help)
		}
	}
}

// A fact the plan could not read is unread and never a match, and a value that
// is write-only by design is not unread: the help says both, once.
func TestPlan_HelpSaysWhatIsUnreadAndWhatIsNot(t *testing.T) {
	help := GetPlanCmd().Long
	for _, want := range []string{
		"could not read",
		"never takes it for a match",
		"is not unread",
		"write-only",
	} {
		if n := strings.Count(help, want); n != 1 {
			t.Errorf("the plan's help says %q %d times, want once:\n%s", want, n, help)
		}
	}
}
