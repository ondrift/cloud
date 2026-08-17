package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	project "github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// getCreateCmd builds `drift slice create [name]`.
//
// Default flow: hands off to the configurator in the browser. The CLI mints a
// single-use session, opens the URL, and polls until the user submits — the
// configurator forwards the create to the api under the user's own token, so
// the slice is created by the same authenticated call the CLI would have made.
// A positional name pre-fills the form; without one the form collects it.
//
// Headless flow (--free / --headless): skips the browser entirely
// and creates a free Hacker slice directly. The name is required
// in headless mode because there is no form to collect it from.
// This path is the only one that works in CI, scripts, and SSH
// sessions.
//
// Driftfile flow (--from <path>): the slice is born at the shape the
// manifest declares, which is what `drift file apply` would have
// provisioned had the slice not existed. Without this, the two-command
// path (`slice create` then `project deploy`) starts from a shape nobody
// declared — the fixed free preset, which is larger than any honest first
// Driftfile — while the one-command path starts from the manifest. Same
// project, two different slices, depending only on which command was run
// first. See HQ CLI-STANDARDUSAGE-T17KKN.
func getCreateCmd() *cobra.Command {
	var (
		headless      bool
		free          bool
		fromPath      string
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice (opens the configurator in your browser, or --from a Driftfile)",
		Example: "  drift slice create my-slice            # opens the configurator\n" +
			"  drift slice create my-slice --free      # free Hacker slice, no browser\n" +
			"  drift slice create --from Driftfile     # born at the manifest's shape\n" +
			"  drift slice create my-slice --headless  # alias for --free (CI/scripts)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// --headless is an alias for --free (backward compat).
			if headless {
				free = true
			}

			if fromPath != "" {
				if name != "" {
					return fmt.Errorf("cannot pass <name> when --from is set; the slice name comes from the Driftfile")
				}
				if free {
					return fmt.Errorf("cannot combine --from with --free; use --free alone, or --from to configure the slice in the browser")
				}
				common.DeprecateFlag(cmd, "from", common.Deprecation{
					Old:         "drift slice create --from",
					New:         "drift slice create",
					RemoveAfter: removeAfterShapeIsConfiguratorOwned,
					Because:     "A Driftfile no longer declares a slice's shape, so there is nothing here to create one from. This opens the configurator on the slice the file names.",
				})()
				return handoffFromDriftfile("create slice", fromPath, common.ModeCreate)
			}

			// Free tier: create directly, no configurator.
			if free {
				if name == "" {
					return fmt.Errorf("--free requires a slice name argument")
				}
				return createHeadless(name)
			}

			// Default: the browser configurator, the same handoff `resize`
			// uses — one session flow, one place it can break.
			result, err := common.RunBrowserHandoff("create slice", name, common.ModeCreate, nil)
			if err != nil {
				return err
			}
			printSliceSummary("created", result)
			// The configurator returns the api's Slice document, so the name
			// is known even when the form collected it rather than the CLI.
			if created := sliceNameFrom(result); created != "" {
				if serr := common.SaveActiveSlice(created); serr != nil {
					fmt.Println("Warning: couldn't mark the new slice as active —", serr)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&free, "free", false, "Create a free Hacker slice without opening the browser")
	cmd.Flags().BoolVar(&headless, "headless", false, "Alias for --free (CI/scripts)")
	cmd.Flags().StringVar(&fromPath, "from", "", "Create the slice at the shape a Driftfile declares (name comes from the Driftfile)")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months, for a configured (non-free) slice")
	_ = cmd.Flags().MarkHidden("headless")
	return cmd
}

// createHeadless posts directly to api/ops/slice/create with the free
// tier. Kept for non-interactive use (CI, scripts, SSH sessions). For
// configured (paid) slices, use the browser flow (the default, no
// --free/--headless).
func createHeadless(name string) error {
	body, _ := json.Marshal(map[string]string{
		"name": name,
		"tier": "hacker",
	})

	resp, err := common.DoJSONRequest(
		http.MethodPost,
		common.APIBaseURL+"/ops/slice/create",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()

	if _, err := common.CheckResponse(resp, "create slice"); err != nil {
		return err
	}

	// Wait for the slice to answer, exactly as the --from path above does.
	// The record exists the moment create returns, but the components behind
	// it do not, and this command's whole purpose is that a deploy follows it
	// — the documented opening of every demo is create, use, deploy, run back
	// to back. A deploy issued into that gap is refused by a component that is
	// not listening yet, and the CLI renders any 5xx as the maintenance
	// message, so the reader is told to wait for Drift when what they are
	// waiting for is their own slice.
	//
	// The slice is created either way, so a timeout is reported as "not ready
	// yet" rather than as a failure: a create retried against an existing name
	// gets a 409, which is a worse place to leave someone.
	fmt.Printf("  Waiting for slice %q to come up...\n", name)
	if err := project.WaitForSliceReady(name); err != nil {
		if serr := common.SaveActiveSlice(name); serr != nil {
			fmt.Println("Warning: couldn't mark the new slice as active —", serr)
		}
		return fmt.Errorf("slice %q was created and is still starting: %w", name, err)
	}

	if err := common.SaveActiveSlice(name); err != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", err)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", name)
	return nil
}
