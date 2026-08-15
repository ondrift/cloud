package atomic_cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Delete() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a deployed atomic function by name",
		Example: "  drift atomic delete send-email\n  drift atomic delete users --method get",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		// SilenceUsage: a refused delete is the platform answering, not the command
		// being held wrong, and printing the usage block after it buries the reason.
		SilenceUsage: true,
		// RunE, and the error is RETURNED rather than printed: main() renders it to
		// stderr and exits non-zero. Under Run the command could only print and fall
		// off the end, so a delete the platform refused still exited 0 — which a CI
		// gate reads as "the function is gone" and a shell `&&` chain carries on from.
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			target := common.APIBaseURL + "/ops/atomic/delete?name=" + url.QueryEscape(name)
			if method != "" {
				target += "&method=" + url.QueryEscape(method)
			}
			resp, err := common.DoRequest(http.MethodDelete, target, nil)
			if err != nil {
				return common.TransportError("delete atomic function", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete atomic function"); err != nil {
				return err
			}

			fmt.Printf("Function %q deleted.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&method, "method", "m", "", "HTTP method, to disambiguate get:x from post:x at the same path")
	return cmd
}
