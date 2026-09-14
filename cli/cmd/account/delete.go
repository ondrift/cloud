package account

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// GetDeleteCmd returns `drift account delete` — the self-service, irreversible
// removal of the logged-in account and everything tied to it. It calls
// DELETE /ops/account (which tears down every slice, wipes every record, and
// purges object storage), then clears the local session since the account it
// pointed at no longer exists.
func GetDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete [username]",
		Short:   "Delete your account and everything in it (irreversible)",
		Args:    cobra.MaximumNArgs(1),
		Example: "  drift account delete\n  drift account delete alice --yes",
		// RunE, not Run. Every refusal here — not logged in, --yes without the
		// name, a name that does not match — could only be printed and returned,
		// which exits 0. So a script whose delete was REFUSED read as one whose
		// delete succeeded, which on this command is the most misleading answer
		// available: the caller concludes the account is gone.
		//
		// The cancellations below stay a plain `return nil`. A person answering
		// "no" is not an error, and exiting non-zero on a deliberate cancel would
		// make the safe answer look like a failure.
		RunE: func(cmd *cobra.Command, args []string) error {
			username := common.GetUsername()
			if username == "" {
				return fmt.Errorf("you are not logged in — run `drift account login` first")
			}

			// --yes drops the PROMPTS. It must never drop the identity check,
			// so the name the prompt would have asked for has to be on the
			// command line instead — the same shape `slice delete` uses.
			//
			// Interactively the argument is optional, because the typed
			// confirmation below already asks for it and demanding both would
			// be friction with no extra proof of intent.
			if yes {
				if len(args) == 0 {
					//nolint:staticcheck // ST1005: user-facing copy, printed as written.
					return fmt.Errorf("Refusing to delete account '%s': --yes skips the prompts, so the account name must be given on the command line.\n"+
						"  drift account delete %s --yes", username, username)
				}
				if args[0] != username {
					//nolint:staticcheck // ST1005: user-facing copy, printed as written.
					return fmt.Errorf("Refusing to delete account '%s': you named '%s'.", username, args[0])
				}
			}

			if !yes {
				printAccountDeleteWarning(username)

				// First confirmation: plain yes/no.
				first := strings.ToLower(strings.TrimSpace(
					common.PromptForInput("Proceed with deletion? [y/N]"),
				))
				if first != "y" && first != "yes" {
					fmt.Println("Deletion cancelled.")
					return nil
				}

				// Second confirmation: type the username verbatim.
				typed := strings.TrimSpace(
					common.PromptForInput(fmt.Sprintf("Type '%s' to confirm", username)),
				)
				if typed != username {
					fmt.Println("Deletion cancelled — username did not match.")
					return nil
				}
			}

			resp, err := common.DoRequest(http.MethodDelete, common.APIBaseURL+"/ops/account", nil)
			if err != nil {
				return common.TransportError("delete account", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete account"); err != nil {
				return err
			}

			// The account (and every session for it) is gone — drop local creds.
			_ = common.ClearSession()

			fmt.Printf("Account '%s' deleted. Everything tied to it is gone, and the username is free again.\n", username)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompts (for scripts). The account name argument must still match exactly.")
	return cmd
}

// printAccountDeleteWarning spells out exactly what will be destroyed. Keep the
// tone honest and direct — this is even more destructive than deleting a single
// slice: it takes every slice with it.
func printAccountDeleteWarning(username string) {
	fmt.Printf(`
────────────────────────────────────────────────────────────
  You are about to delete your account '%s'.
────────────────────────────────────────────────────────────

This will PERMANENTLY destroy, with no recovery:

  • Every slice you own, and everything in each one — atomic
    functions, canvas sites, the entire backbone (NoSQL, queues,
    blobs, secrets, cache), and all deployment history
  • Every snapshot and deploy artifact stored for your account
  • Your account itself — the username becomes free for anyone

There is NO undo. There is NO backup we can restore from.

`, username)
}
