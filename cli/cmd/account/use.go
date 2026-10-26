// use.go — `drift account list` and `drift account use <name>`.
//
// The session file holds several accounts. These say which are logged in and
// which one commands act as.
package account

import (
	"fmt"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func GetAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the accounts you are logged in to",
		Long: `List the accounts you are logged in to, marking the current one.

To act as another account for a single command, without switching:

    DRIFT_ACCOUNT=other drift slice list`,
		Example: "  drift account list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, current, err := common.Accounts()
			if err != nil || len(names) == 0 {
				fmt.Println("(not logged in)")
				fmt.Println("  Log in with `drift account login`")
				return nil
			}

			fmt.Printf("%s Accounts\n\n", common.Highlight(""))
			for _, n := range names {
				marker := "  "
				if n == current {
					marker = "* "
				}
				fmt.Printf("%s%s\n", marker, n)
			}
			fmt.Printf("\n%d account(s); * is current\n", len(names))
			return nil
		},
	}
}

func GetAccountUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch which account commands act as",
		Long: `Switch which account commands act as.

This PERSISTS. For a single command, set the environment instead — it changes
nothing on disk, which is what makes it safe in a script:

    DRIFT_ACCOUNT=other drift slice list`,
		Example: "  drift account use alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := common.UseAccount(args[0]); err != nil {
				return err
			}
			fmt.Printf("Now acting as %s\n", args[0])
			return nil
		},
	}
}
