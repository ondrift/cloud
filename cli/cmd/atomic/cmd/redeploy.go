package atomic_cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Redeploy() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:     "redeploy <name>",
		Short:   "Re-deploy the last known artifact for a pulse or field",
		Example: "  drift atomic redeploy send-email\n  drift atomic redeploy users --method get\n  drift atomic redeploy sequencer --method field",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			payload, _ := json.Marshal(map[string]string{"name": function, "method": method})

			s := common.StartSpinner("", "Redeploying "+function+"...")

			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/atomic/redeploy",
				bytes.NewReader(payload),
			)
			if err != nil {
				s.Stop()
				return common.TransportError("redeploy", err)
			}
			defer resp.Body.Close()

			_, err = common.CheckResponse(resp, "redeploy")
			s.Stop()
			if err != nil {
				return err
			}

			fmt.Printf("%s redeployed from its last known artifact.\n", common.Highlight(function))
			return nil
		},
	}
	cmd.Flags().StringVarP(&method, "method", "m", "", "HTTP method, to disambiguate get:x from post:x at the same path, or `field` for a field")
	return cmd
}
