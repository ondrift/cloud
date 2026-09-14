package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func getDeleteCmd() *cobra.Command {
	var yes bool
	deleteCmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a slice and everything in it (irreversible)",
		Args:    cobra.ExactArgs(1),
		Example: "  drift slice delete my-slice\n  drift slice delete my-slice --yes",
		// RunE, not Run. A `Run` handler has no error channel, so a refused or
		// failed delete could only be printed and returned — which exits 0, and a
		// script then reads "the slice is gone" from a command that deleted
		// nothing.
		//
		// The two CANCELLATIONS stay `nil`: a person answering "no" chose the
		// safe outcome, and exiting non-zero on that would make it read as a
		// failure.
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Pre-check: verify the slice actually exists before showing
			// the scary confirmation prompts.
			if !sliceExists(name) {
				return fmt.Errorf("Couldn't delete slice: no slice named %q was found", name) //nolint:staticcheck // ST1005: user-facing copy.
			}

			// --yes skips both interactive confirmations. The slice name
			// still has to match the argv, which preserves the "you must
			// spell it out" safety of the interactive flow.
			if !yes {
				printDeleteWarning(name)

				// First confirmation: plain yes/no.
				first := strings.ToLower(strings.TrimSpace(
					common.PromptForInput("Proceed with deletion? [y/N]"),
				))
				if first != "y" && first != "yes" {
					fmt.Println("Deletion cancelled.")
					return nil
				}

				// Second confirmation: type the slice name verbatim.
				typed := strings.TrimSpace(
					common.PromptForInput(fmt.Sprintf("Type '%s' to confirm", name)),
				)
				if typed != name {
					fmt.Println("Deletion cancelled — name did not match.")
					return nil
				}
			}

			resp, err := common.DoRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/slice/delete?name="+url.QueryEscape(name),
				nil,
			)
			if err != nil {
				return common.TransportError("delete slice", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete slice"); err != nil {
				return err
			}

			// Clear active slice if it was the deleted one.
			if common.GetActiveSlice() == name {
				_ = common.SaveActiveSlice("")
			}

			fmt.Printf("Slice '%s' deleted.\n", name)
			return nil
		},
	}

	deleteCmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompts (for scripts). The slice name argument must still match exactly.")
	return deleteCmd
}

// sliceExists checks whether a slice with the given name exists by calling
// the list endpoint and looking for a match.
func sliceExists(name string) bool {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/slice/list",
		nil,
	)
	if err != nil {
		// If we can't reach the API, let the delete proceed — the server
		// will reject it with a proper error message.
		return true
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "check slice exists")
	if err != nil {
		return true
	}

	var slices []SliceEntry
	if err := json.Unmarshal(body, &slices); err != nil {
		return true
	}

	for _, s := range slices {
		if s.Name == name {
			return true
		}
	}
	return false
}

// printDeleteWarning spells out exactly what will be destroyed so the user
// can't plausibly claim surprise. Keep this tone honest and direct — this is
// a destructive, irreversible action.
func printDeleteWarning(name string) {
	active := ""
	if common.GetActiveSlice() == name {
		active = "  (this is your currently active slice)"
	}
	fmt.Printf(`
────────────────────────────────────────────────────────────
  You are about to delete slice '%s'%s.
────────────────────────────────────────────────────────────

This will PERMANENTLY destroy, with no recovery:

  • Every atomic function deployed to this slice
  • Every canvas site hosted on this slice
  • The entire backbone: NoSQL collections, queues, blobs,
    secrets, and cached data
  • All logs, metrics, and deployment history
  • The slice's isolated environment and all infrastructure
  • The slice's database

There is NO undo. There is NO backup we can restore from.

`, name, active)
}
