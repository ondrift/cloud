package account

import (
	"github.com/spf13/cobra"
)

// GetAccountCmd returns the "drift account" command group.
// Subcommands: create, login, whoami, mfa, token, reset-password, audit, delete.
func GetAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage your Drift account",
		Example: `  drift account create
  drift account login
  drift account whoami
  drift account list
  drift account use alice
  drift account mfa enrol
  drift account token create ci --scope slice:read --scope slice:write
  drift account reset-password
  drift account audit
  drift account delete`,
		GroupID: "account",
	}
	cmd.AddCommand(
		GetCreateCmd(),
		GetLoginCmd(),
		GetWhoamiCmd(),
		GetAccountListCmd(),
		GetAccountUseCmd(),
		GetMFACmd(),
		tokenCmd(),
		GetResetPasswordCmd(),
		GetAuditCmd(),
		GetDeleteCmd(),
	)
	return cmd
}
