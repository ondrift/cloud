package account

import (
	"strings"
	"testing"
)

// `remove` takes EXACTLY ONE identifier, and the command refuses the other two
// shapes itself rather than sending an ambiguous request and letting the
// platform decide.
//
// The two name different things — a member by username, a pending invite by
// email — and a command that quietly preferred one would remove the wrong one
// the first time somebody's username looked like an address.
func TestMemberRemoveNeedsExactlyOneIdentifier(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		email string
	}{
		"neither": {nil, ""},
		"both":    {[]string{"erica"}, "erica@example.com"},
	} {
		cmd := memberRemoveCmd()
		if tc.email != "" {
			if err := cmd.Flags().Set("email", tc.email); err != nil {
				t.Fatalf("%s: setting --email: %v", name, err)
			}
		}
		err := cmd.RunE(cmd, tc.args)
		if err == nil {
			t.Errorf("%s: accepted, and would have sent a request naming nothing definite", name)
			continue
		}
		// The refusal has to say how to pick one — this is the only place a user
		// finds out the two forms exist.
		for _, want := range []string{"username", "--email"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error does not mention %q: %v", name, want, err)
			}
		}
	}
}

// WHAT A MEMBER CAN AND CANNOT DO IS ONLY WRITTEN DOWN IN THE HELP, so it has to
// be there and it has to be right.
//
// Nothing in the CLI enforces the role — the platform's scope check does — which
// means this text is the entire basis on which an owner decides whether to invite
// somebody. Missing the secrets line, in particular, would have an owner believe
// they are sharing less than they are.
func TestTheMemberHelpStatesTheBoundaryInBothDirections(t *testing.T) {
	help := memberCmd().Long + "\n" + memberListCmd().Long

	for _, can := range []string{"deploy", "slices", "Backbone"} {
		if !strings.Contains(help, can) {
			t.Errorf("the help never says a member can %q — an owner cannot tell what "+
				"they are granting", can)
		}
	}
	for _, cannot := range []string{"secrets", "audit trail", "delete the account"} {
		if !strings.Contains(help, cannot) {
			t.Errorf("the help never says a member CANNOT touch %q — an owner cannot tell "+
				"what they are keeping", cannot)
		}
	}
}

// A member is a PERSON and a token is a THING, and the help has to send the
// reader to the right one. Using a member account for a CI pipeline works, and
// makes the audit trail name a person for something a machine did.
func TestTheMemberHelpPointsPipelinesAtTokensInstead(t *testing.T) {
	long := memberCmd().Long
	if !strings.Contains(long, "drift account token") {
		t.Error("the help does not point a CI pipeline at `drift account token` — the " +
			"audit trail is written on the assumption that a member is a person")
	}
}

// The invite help must say the code is single-use and bound to the address. Both
// are refusals the user will otherwise meet as an unexplained 403 at signup.
func TestTheInviteHelpStatesHowTheCodeIsBounded(t *testing.T) {
	long := memberInviteCmd().Long
	for _, want := range []string{"once", "email address"} {
		if !strings.Contains(long, want) {
			t.Errorf("the invite help does not mention %q", want)
		}
	}
}

// Removing somebody must not read as deleting them. An owner reaching for this
// command needs to know it ends access and nothing else — the member's own
// account, password and second factor are theirs.
func TestTheRemoveHelpSaysItDoesNotDeleteTheirAccount(t *testing.T) {
	long := memberRemoveCmd().Long
	if !strings.Contains(long, "DOES NOT DELETE") {
		t.Error("the remove help does not say the member's own account survives")
	}
}

// Every subcommand carries an example. `drift account member` is a command
// nobody runs twice a day, so the example is what the user actually copies.
func TestEveryMemberSubcommandShowsAnExample(t *testing.T) {
	for _, c := range memberCmd().Commands() {
		if strings.TrimSpace(c.Example) == "" && strings.TrimSpace(c.Use) != "" {
			t.Errorf("`drift account member %s` has no example", c.Name())
		}
	}
}
