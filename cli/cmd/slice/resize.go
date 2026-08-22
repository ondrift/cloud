package slice

import (
	"fmt"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// getResizeCmd builds `drift slice resize`.
//
// It draws the slice's shape in the terminal, opened on what the slice already
// is, exactly as `drift slice create` draws a new one. `--browser` opens the
// configurator instead.
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
  drift slice resize my-slice
  drift slice resize my-slice --browser   # shape it in the configurator instead`,
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

	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "Authorise shrinks that lower a resource limit. Required for any non-zero shrink.")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months for the resize")
	return cmd
}

// getShrinkCmd is `drift slice shrink` — apply a Driftfile INCLUDING the
// reductions in it.
//
// It exists because the remedy for a refused deploy was
// `drift slice resize --from Driftfile --allow-destructive`: six words, for the
// one verdict where a user is stopped and needs to act. That is the worst place
// for the three-word rule to break, and no shorter spelling of that flag pair is
// possible — so the verb had to exist rather than the string get shorter.
//
// The destructive intent lives in the VERB now, which is also better than a
// flag: "shrink" is what the user meant, and a name is harder to pass by
// accident than an option they copied from somewhere.
//
// It now opens the configurator on the slice the file names, exactly as
// `resize --from` does, so the two spellings cannot diverge — and the reduction
// it was named for is chosen and confirmed where the shape lives.
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

func centsToEuros(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("%d", cents/100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func confirmYesNo(autoYes bool, prompt string) bool {
	if autoYes {
		fmt.Printf("  %s [y/N] (auto-yes)\n", prompt)
		return true
	}
	fmt.Printf("  %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	return ans == "y" || ans == "Y" || ans == "yes" || ans == "YES"
}
