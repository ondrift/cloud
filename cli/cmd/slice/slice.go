package slice

import "github.com/spf13/cobra"

// GetCmd returns the "drift slice" command group for managing slices.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "slice",
		Short:   "Manage your Drift slices (projects)",
		Example: "  drift slice list\n  drift slice info\n  drift slice resize my-slice\n  drift slice delete my-slice",
		GroupID: "account",
	}
	cmd.AddCommand(
		getResizeCmd(),
		getListCmd(),
		getInfoCmd(),
		getDeleteCmd(),
		getRestartCmd(),
		getWarmupCmd(),
		getAuthCmd(),
		getSnapshotCmd(),
		getDomainCmd(),
		getLinkCmd(),
		getTransferCmd(),
	)
	return cmd
}
