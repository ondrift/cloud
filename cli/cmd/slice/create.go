package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

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
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice, asking for its shape here",
		Example: "  drift slice create                      # asks: name, functions, routes, memory\n" +
			"  drift slice create my-slice             # same, with the name filled in\n" +
			"  drift slice create my-slice --free      # the free slice, nothing to answer",
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

			// Free tier: create directly, no configurator.
			if free {
				if name == "" {
					return fmt.Errorf("--free requires a slice name argument")
				}
				return createHeadless(name)
			}

			// Default: ask here. The platform prices a config over an endpoint,
			// and a slice's shape is a handful of questions with answers only the
			// person typing has.
			return createFromPrompts(name, billingMonths)
		},
	}

	cmd.Flags().BoolVar(&free, "free", false, "Create a free Hacker slice without drawing the form")
	cmd.Flags().BoolVar(&headless, "headless", false, "Alias for --free (CI/scripts)")
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
	if err := common.WaitForSliceReady(name); err != nil {
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
