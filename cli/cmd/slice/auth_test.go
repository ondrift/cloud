package slice

import (
	"strings"
	"testing"
)

// The gate is the one command here that hands a credential to the platform, so
// the flag surface is the part worth pinning: a --password flag would put the
// password into `ps` output and shell history, which is exactly why
// `account login` reads it from stdin instead.
func TestSliceAuthSet_HasNoPasswordFlag(t *testing.T) {
	set := getAuthSetCmd()

	if f := set.Flags().Lookup("password"); f != nil {
		t.Error("`--password` exists — a password passed as an argument is visible " +
			"in ps and shell history; read it from stdin instead")
	}
	if f := set.Flags().Lookup("password-stdin"); f == nil {
		t.Error("`--password-stdin` is missing — CI has no terminal to prompt at")
	}
	if f := set.Flags().Lookup("user"); f == nil {
		t.Error("`--user` is missing")
	}
}

// A gate with no users would lock the site to nobody, which is a footgun rather
// than a configuration. The api rejects it too; failing in the CLI means the
// user is told before a round trip.
func TestSliceAuthSet_RequiresAUser(t *testing.T) {
	set := getAuthSetCmd()
	if !strings.Contains(set.Long, "--user") {
		t.Error("the help does not mention --user, which is the one required flag")
	}
}

// `set` REPLACES the credential set rather than appending. That is what makes
// removing someone one predictable command, and it is surprising enough to be
// worth stating where the user reads it.
func TestSliceAuthSet_SaysItReplaces(t *testing.T) {
	set := getAuthSetCmd()
	if !strings.Contains(strings.ToUpper(set.Long), "REPLACES") {
		t.Error("`set` replaces the whole credential set and the help must say so — " +
			"a user expecting it to append would silently lock out everyone else")
	}
}

// Disabling is the direction that EXPOSES the site, so it confirms. Enabling
// does not need to: putting a wall up harms nobody.
func TestSliceAuthDisable_WarnsBeforeExposingTheSite(t *testing.T) {
	dis := getAuthDisableCmd()
	if !strings.Contains(dis.Long, "EXPOSES") {
		t.Error("`disable` must say it exposes the site — it is the only direction " +
			"here that makes something public")
	}
}

// The three subcommands the api already routes. A missing one sends the user
// back to hand-rolling curl, which is the gap this command closes.
func TestSliceAuth_HasSetListAndDisable(t *testing.T) {
	want := map[string]bool{"set": false, "list": false, "disable": false}
	for _, c := range getAuthCmd().Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("`drift slice auth %s` is missing", name)
		}
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
