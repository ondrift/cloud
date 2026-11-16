package account

import (
	"strings"
	"testing"
)

// The platform's own actions on a slice are in the trail, so the help must not
// say they are not, and says where the recent ones are read.
func TestAuditHelpDoesNotSayPlatformActionsAreAbsent(t *testing.T) {
	long := GetAuditCmd().Long
	for _, stale := range []string{"Only YOUR events", "do not appear"} {
		if strings.Contains(long, stale) {
			t.Errorf("`drift account audit --help` still says %q:\n%s", stale, long)
		}
	}
	for _, want := range []string{"the platform", "drift slice info"} {
		if !strings.Contains(long, want) {
			t.Errorf("the help does not say %q:\n%s", want, long)
		}
	}
}
