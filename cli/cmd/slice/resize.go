package slice

import (
	"fmt"
	"os"

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
//
// # And both can be answered WITHOUT one
//
// The form needs a terminal, so for a long time there was no resize at all in a
// script, a CI job or an SDK consumer: every automated path onto Drift began
// with a human at a form. `--dump` and `--config` are the other way round the
// same two questions — see resizefile.go, which explains why a flag is as safe
// as a prompt here and the form is not what makes either safe.
func getResizeCmd() *cobra.Command {
	var (
		allowDestructive bool
		autoYes          bool
		billingMonths    int
		dump             bool
		configPath       string
		ackCents         int
		confirm          string
	)

	cmd := &cobra.Command{
		Use:   "resize [name]",
		Short: "Resize a slice — defaults to the active slice",
		Long: "Resize a slice.\n\n" +
			"With no flags this draws a form, opened on what the slice already is.\n\n" +
			"For a script or a CI job, the same change is three commands:\n\n" +
			"    drift slice resize my-slice --dump > shape.json\n" +
			"    $EDITOR shape.json\n" +
			"    drift slice resize my-slice --config shape.json\n\n" +
			"The platform asks two questions of every caller, form or not: it refuses a\n" +
			"resize that books memory per function for the FIRST time -- the one repricing\n" +
			"that can look like bookkeeping -- until the new figure is sent back, and one\n" +
			"that takes something away until the slice is named. Answer them with\n" +
			"--acknowledge-monthly-cents and --confirm. Each refusal says which is needed\n" +
			"and what the figure or the loss actually is. An ordinary price increase --\n" +
			"more storage, scheduled jobs, realtime connections -- is not asked to confirm:\n" +
			"it is exactly what was requested.",
		Example: `  drift slice resize
  drift slice resize my-slice
  drift slice resize my-slice --dump > shape.json
  drift slice resize my-slice --config shape.json --acknowledge-monthly-cents 1200`,
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

			if dump && configPath != "" {
				return fmt.Errorf("--dump writes the current shape and --config applies one; " +
					"they are the two halves of the loop, not one command")
			}
			if dump {
				return dumpSliceConfig(name, os.Stdout)
			}
			if configPath != "" {
				// -1 means "not given", which is not 0: a slice can cost nothing,
				// so the absence of a figure and the figure zero must differ.
				ack := -1
				if cmd.Flags().Changed("acknowledge-monthly-cents") {
					ack = ackCents
				}
				return resizeFromFile(name, configPath, billingMonths, ack, confirm)
			}
			return resizeFromPrompts(name, billingMonths)
		},
	}

	// Both are inert: the form asks each question where the answer belongs. A
	// removal is authorised by typing the slice's name on the form, and the
	// price is confirmed on the row that shows it. They keep parsing so a
	// script that passes them is not stopped by an unknown flag.
	//
	// `--confirm` below is NOT a replacement for either: it is the non-interactive
	// answer to the destructive question, and it takes the slice's NAME for the
	// same reason the form does — a bare boolean agrees to whatever the loss turns
	// out to be, and the refusal lists it first.
	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "Deprecated: does nothing; the form confirms a removal by naming the slice")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Deprecated: does nothing; the form confirms")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months for the resize")
	cmd.Flags().BoolVar(&dump, "dump", false, "Write the slice's current shape as JSON to stdout, for editing")
	cmd.Flags().StringVar(&configPath, "config", "", "Apply the shape in this JSON file instead of drawing the form")
	cmd.Flags().IntVar(&ackCents, "acknowledge-monthly-cents", 0,
		"Agree to the new monthly price, in cents, when the resize changes it")
	cmd.Flags().StringVar(&confirm, "confirm", "",
		"Name the slice to confirm a resize that takes something away")
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
