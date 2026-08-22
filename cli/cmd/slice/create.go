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
// Default flow: draws the slice's shape in the terminal and creates it from
// what was drawn — see interactive.go. A positional name pre-fills the form;
// without one the form collects it. It needs a terminal, and says so when there
// is none.
//
// Free flow (--free / --headless): skips the form and creates a free Hacker
// slice directly. The name is required, because there is no form to collect it
// from. This is the path CI, scripts and non-interactive SSH sessions take.
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

			// Free tier: create directly, with no form to draw.
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

// createHeadless posts directly to api/ops/slice/create with the free tier. It
// is the non-interactive path (CI, scripts, SSH sessions). A configured (paid)
// slice is drawn on the form instead — the default, with no --free/--headless.
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

	// Wait for the slice to answer, exactly as the form's own create does.
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
