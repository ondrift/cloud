package slice

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// The verbs the Driftfile now declares say so when run, and the ones it cannot
// declare stay silent. `domain remove` and `auth disable` have no declarative
// form (apply leaves an undeclared host attached and has no key that turns the
// gate off), so deprecating them would point at nothing.
func TestRetiredMutations_DeprecatedVerbsPointAtApplyAndTheRestStaySilent(t *testing.T) {
	cases := []struct {
		name       string
		build      func() *cobra.Command
		path       []string
		deprecated bool
	}{
		{"link add", getLinkCmd, []string{"add"}, true},
		{"link remove", getLinkCmd, []string{"remove"}, true},
		{"link list", getLinkCmd, []string{"list"}, false},
		{"domain add", getDomainCmd, []string{"add"}, true},
		{"domain remove", getDomainCmd, []string{"remove"}, false},
		{"domain verify", getDomainCmd, []string{"verify"}, false},
		{"domain list", getDomainCmd, []string{"list"}, false},
		{"auth set", getAuthCmd, []string{"set"}, true},
		{"auth disable", getAuthCmd, []string{"disable"}, false},
		{"auth list", getAuthCmd, []string{"list"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			common.ResetDeprecationState()
			var warned bytes.Buffer
			defer common.RedirectDeprecationWarnings(&warned)()

			root := c.build()
			sub, _, err := root.Find(c.path)
			if err != nil {
				t.Fatal(err)
			}
			// Run only the pre-run chain: the notice is what is under test, and
			// the verb's own request needs a platform.
			for p := sub; p != nil; p = p.Parent() {
				if p.PersistentPreRunE != nil {
					if err := p.PersistentPreRunE(sub, nil); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			got := warned.String()
			if c.deprecated {
				if !strings.Contains(got, "is deprecated") || !strings.Contains(got, `"drift apply"`) {
					t.Errorf("%s does not tell its user to use `drift apply`: %q", c.name, got)
				}
			} else if got != "" {
				t.Errorf("%s has no declarative form and must not be deprecated, but said %q", c.name, got)
			}
		})
	}
}
