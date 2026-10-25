package slice

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// What the Driftfile declares has no command that sets it. `domain remove` and
// `auth disable` stay because the Driftfile has no declaration that does their
// job: apply leaves an undeclared host attached, and no key turns the gate off.
func TestRetiredMutations_OnlyVerbsWithNoDeclarativeFormRemain(t *testing.T) {
	cases := []struct {
		name  string
		build func() *cobra.Command
		want  string
	}{
		{"link", getLinkCmd, "list"},
		{"domain", getDomainCmd, "list,remove,verify"},
		{"auth", getAuthCmd, "disable,list"},
	}
	for _, c := range cases {
		var names []string
		for _, sub := range c.build().Commands() {
			names = append(names, sub.Name())
		}
		// Commands() is sorted by name.
		if got := strings.Join(names, ","); got != c.want {
			t.Errorf("`drift slice %s` has subcommands %q, want %q", c.name, got, c.want)
		}
	}
}
