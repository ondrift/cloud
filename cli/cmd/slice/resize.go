package slice

import (
	"fmt"

	project "github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// getResizeCmd builds `drift slice resize`.
//
// Two modes:
//
//  1. **Browser mode** (default, no args + no flags). Opens the
//     configurator in the browser with the slice's current config
//     pre-loaded. Same UX the platform has had since v0.
//
//  2. **Driftfile mode** (`--from <path>`). Reads a Driftfile,
//     diffs it against the live slice, and applies the divergence
//     directly via /ops/slice/resize. Unlike `drift file apply`
//     (which aborts on shrink), this command is the named verb for
//     shrinking — it requires `--allow-destructive` to actually
//     apply any field that goes down.
//
// The Driftfile mode is the load-bearing answer to the spec's
// reconcile rule #3: "deploy never shrinks." Shrinks live here, with
// a separate flag, separate prompt, and separate code path.
func getResizeCmd() *cobra.Command {
	var (
		fromPath         string
		allowDestructive bool
		autoYes          bool
		billingMonths    int
	)

	cmd := &cobra.Command{
		Use:   "resize [name]",
		Short: "Resize a slice — defaults to the active slice; browser by default, or --from a Driftfile",
		Example: `  drift slice resize
  drift slice resize my-slice
  drift slice resize --from Driftfile
  drift slice resize --from Driftfile --allow-destructive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromPath != "" {
				if len(args) > 0 {
					return fmt.Errorf("cannot pass <name> when --from is set; the slice name comes from the Driftfile")
				}
				common.DeprecateFlag(cmd, "from", common.Deprecation{
					Old:         "drift slice resize --from",
					New:         "drift slice resize",
					RemoveAfter: removeAfterShapeIsConfiguratorOwned,
					Because:     "A Driftfile no longer declares a slice's shape. This opens the configurator on the slice the file names, pre-filled with what the slice currently is.",
				})()
				return handoffFromDriftfile("resize slice", fromPath, common.ModeResize)
			}

			// No name given → resize the currently active slice (the one
			// `drift slice use` selected), matching every other slice subcommand.
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				active, err := common.RequireActiveSlice()
				if err != nil {
					return fmt.Errorf("%w, or pass a slice name / use --from <Driftfile>", err)
				}
				name = active
			}
			existing, err := project.FetchSliceConfigRaw(name, "resize slice")
			if err != nil {
				return err
			}
			result, err := common.RunBrowserHandoff("resize slice", name, common.ModeResize, existing)
			if err != nil {
				return err
			}
			printSliceSummary("resized", result)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromPath, "from", "", "Read the target shape from a Driftfile (default: open the browser configurator)")
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
		fromPath      string
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "shrink",
		Short: "Deprecated: opens the configurator on the slice a Driftfile names",
		Long: "Deprecated — use `drift slice resize`.\n\n" +
			"This verb existed to apply the reductions a Driftfile declared, back when a\n" +
			"Driftfile declared a slice's shape. It no longer does: the configurator owns\n" +
			"the shape, and it is where a reduction is chosen and confirmed.\n\n" +
			"It still works, and opens the configurator on the slice the file names.",
		Example: "  drift slice shrink\n  drift slice shrink --from Driftfile",
		Args:    cobra.NoArgs,
		// A refused shrink is the platform answering, not the command being held
		// wrong, so the usage block would bury the list of what it would remove.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return handoffFromDriftfile("resize slice", fromPath, common.ModeResize)
		},
	}

	cmd.Flags().StringVar(&fromPath, "from", "", "Driftfile naming the slice to configure (default: ./Driftfile)")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Deprecated: does nothing; the configurator confirms")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Deprecated: does nothing; billing is chosen in the configurator")

	return common.DeprecateCommand(cmd, common.Deprecation{
		Old:         "drift slice shrink",
		New:         "drift slice resize",
		RemoveAfter: removeAfterShapeIsConfiguratorOwned,
		Because:     "A reduction is chosen and confirmed in the configurator, which owns a slice's shape — so there is no longer a destructive spelling of resize for this to be.",
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
