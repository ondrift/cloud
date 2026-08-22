package slice

import (
	"fmt"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// getResizeCmd builds `drift slice resize`.
//
// It draws the slice's shape in the terminal, opened on what the slice already
// is, exactly as `drift slice create` draws a new one.
//
// A resize is where the platform asks two questions a create never has to: it
// refuses one that reprices the whole slice until the new figure is sent back,
// and one that takes something away until the slice is named. Both are answered
// on the form — see resizeform.go.
func getResizeCmd() *cobra.Command {
	var (
		allowDestructive bool
		autoYes          bool
		billingMonths    int
	)

	cmd := &cobra.Command{
		Use:   "resize [name]",
		Short: "Resize a slice — defaults to the active slice",
		Example: `  drift slice resize
  drift slice resize my-slice`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// No name given → resize the currently active slice (the one
			// `drift slice use` selected), matching every other slice subcommand.
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				active, err := common.RequireActiveSlice()
				if err != nil {
					return fmt.Errorf("%w, or pass a slice name", err)
				}
				name = active
			}
			return resizeFromPrompts(name, billingMonths)
		},
	}

	// Both are inert: the form asks each question where the answer belongs. A
	// removal is authorised by typing the slice's name on the form, and the
	// price is confirmed on the row that shows it. They keep parsing so a
	// script that passes them is not stopped by an unknown flag.
	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "Deprecated: does nothing; the form confirms a removal by naming the slice")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Deprecated: does nothing; the form confirms")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months for the resize")
	return cmd
}

// getShrinkCmd is `drift slice shrink`, a deprecated spelling of
// `drift slice resize`.
//
// It forwards to the resize form on the active slice and adds nothing of its
// own, which is all a deprecated spelling is allowed to be: the reduction the
// verb was named for is chosen and confirmed on the form, where the shape lives.
func getShrinkCmd() *cobra.Command {
	var (
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "shrink",
		Short: "Deprecated: use drift slice resize",
		Long: "Deprecated — use `drift slice resize`.\n\n" +
			"This verb existed to apply the reductions a Driftfile declared, back when a\n" +
			"Driftfile declared a slice's shape. It no longer does, and a reduction is\n" +
			"chosen and confirmed on the resize form like any other change.\n\n" +
			"It still works, and resizes the active slice.",
		Example: "  drift slice shrink",
		Args:    cobra.NoArgs,
		// A refused shrink is the platform answering, not the command being held
		// wrong, so the usage block would bury the list of what it would remove.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A thin alias onto the real verb, which is all a deprecated spelling
			// is allowed to be — two implementations of one job is the cost the
			// shim exists to avoid.
			name, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}
			return resizeFromPrompts(name, billingMonths)
		},
	}

	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Deprecated: does nothing; the form confirms")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months for the resize")

	return common.DeprecateCommand(cmd, common.Deprecation{
		Old:         "drift slice shrink",
		New:         "drift slice resize",
		RemoveAfter: "",
		Because:     "A reduction is chosen and confirmed on the resize form like any other change, so there is no longer a destructive spelling of resize for this to be.",
	})
}
