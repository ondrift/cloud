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
//
// # A MEMBER gets a second line, and it goes to stderr
//
// A member acts on somebody ELSE'S account: their token names the owner, so the
// one line on stdout is the owner's name — which is the right answer for the
// scripting use above, because it names the account every other command in the
// pipeline is about.
//
// It is the wrong answer to "who am I", so that is said too, on STDERR. A
// command substitution captures stdout alone, so the note reaches the person at
// the terminal and changes nothing for the script. Printing it on stdout would
// break every existing use of this command to keep one of them honest.

import (
	"fmt"
	"os"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func GetWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print the account this machine is acting on",
		Long: "Prints the username and nothing else, so it can be used directly:\n\n" +
			"  drift backbone secret set OWNER \"$(drift account whoami)\"\n\n" +
			"Reads the stored session locally — no network call, and no way for it to\n" +
			"fail because the platform is busy.\n\n" +
			"If you are a MEMBER of someone else's account, this prints THEIR username —\n" +
			"the account your commands act on. Your own name is noted on stderr, so a\n" +
			"command substitution still captures just the one word.",
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
			if actor := common.ActorFromToken(); actor != "" {
				fmt.Fprintf(os.Stderr,
					"(you are signed in as %s, a member of %s — this prints the account you act on)\n",
					actor, username)
			}
			return nil
		},
	}
}
