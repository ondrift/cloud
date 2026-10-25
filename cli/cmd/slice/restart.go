package slice

import (
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func getRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "restart",
		Short:   "Restart your slice",
		Example: "  drift slice restart",
		RunE: func(cmd *cobra.Command, args []string) error {
			slice, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			s := common.StartSpinner("", "Restarting slice...")

			resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/restart", nil)
			if err != nil {
				s.Stop()
				return common.TransportError("restart slice", err)
			}
			defer resp.Body.Close()

			_, err = common.CheckResponse(resp, "restart slice")
			s.Stop()
			if err != nil {
				return err
			}

			fmt.Println(restartNotice(common.Highlight(slice)))
			return nil
		},
	}
}

// restartNotice says what a restart costs. A slice is one instance, so a restart
// replaces it: calls in flight drain, new ones are answered 503, and it is back
// after tens of seconds, not a few. The note used to promise "a few seconds".
func restartNotice(name string) string {
	return fmt.Sprintf("Slice %s is restarting. It is unavailable for tens of seconds, "+
		"and requests in that time are answered 503.", name)
}
