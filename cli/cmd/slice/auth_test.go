package slice

import (
	"strings"
	"testing"
)

// Disabling is the direction that EXPOSES the site, so it confirms.
func TestSliceAuthDisable_WarnsBeforeExposingTheSite(t *testing.T) {
	dis := getAuthDisableCmd()
	if !strings.Contains(dis.Long, "EXPOSES") {
		t.Error("`disable` must say it exposes the site — it is the only direction " +
			"here that makes something public")
	}
}

// Credentials reach the platform only through a Driftfile's `site_auth` and
// `drift apply`. A `set` verb here would be a second place a password is typed,
// and the only one that can put it into shell history.
func TestSliceAuth_HasOnlyListAndDisable(t *testing.T) {
	var names []string
	for _, c := range getAuthCmd().Commands() {
		names = append(names, c.Name())
	}
	if got := strings.Join(names, ","); got != "disable,list" {
		t.Errorf("`drift slice auth` has subcommands %v, want list and disable", names)
	}
}

// The gate covers the SITE. `drift atomic auth` guards functions, and confusing
// the two is the likeliest way someone leaves an API open believing it is not.
func TestSliceAuth_DistinguishesItselfFromAtomicAuth(t *testing.T) {
	if !strings.Contains(getAuthCmd().Long, "atomic auth") {
		t.Error("the help should point at `drift atomic auth` for functions — " +
			"otherwise this reads as though it gates everything")
	}
}
