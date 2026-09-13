package account

// whoami.go — `drift account whoami`: which account this machine is logged in
// as.
//
// # It prints the username and nothing else
//
// One line on stdout, no label, no decoration, so `$(drift account whoami)` is
// the username and not a sentence containing it. Everything a person might also
// want to know — the active slice, when the session expires — is a different
// question with a different command, and putting any of it here would make the
// output unusable in the place this command is most useful.
//
// # Where the name comes from
//
// The `username` claim inside the stored access token, read locally. No network
// call: the token was minted by the platform for this account and the CLI is
// reading its own copy, so a round trip would add a way for this to fail without
// adding anything it could learn.
//
// An EXPIRED token still answers. "Who am I logged in as" has a true answer
// after a token ages out — the session is refreshable and the account has not
// changed — and refusing there would make the command useless at exactly the
// moment someone is debugging why a call 401'd.

import (
	"fmt"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func GetWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print the username this machine is logged in as",
		Long: "Prints the username and nothing else, so it can be used directly:\n\n" +
			"  drift backbone secret set OWNER \"$(drift account whoami)\"\n\n" +
			"Reads the stored session locally — no network call, and no way for it to\n" +
			"fail because the platform is busy.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			username := common.GetUsername()
			if username == "" {
				// An error, not an empty line. A command substitution that
				// silently yields "" is how a script ends up writing an empty
				// owner, tagging an empty account, or comparing against nothing
				// and taking the wrong branch.
				return fmt.Errorf("not logged in — run `drift account login`")
			}
			fmt.Println(username)
			return nil
		},
	}
}
