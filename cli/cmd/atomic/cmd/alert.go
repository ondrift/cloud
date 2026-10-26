// alert.go — `drift atomic alert list`. Alerts are declared on a function in the
// Driftfile and `drift apply` sets them; this only shows what the slice holds.
package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

type alertSpec struct {
	Name         string `json:"name"`
	Function     string `json:"function"`
	Trigger      string `json:"trigger"`
	Threshold    int    `json:"threshold"`
	WindowSec    int    `json:"window_seconds"`
	NotifyType   string `json:"notify_type"`
	NotifyTarget string `json:"notify_target"`
}

func Alert() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alert",
		Short: "List the alerts a slice holds",
		Long: "A function's alerts are declared in the Driftfile, in an `alert` block inside the function, and " +
			"`drift apply` sets them and deletes the ones the file does not name. Nothing here adds or " +
			"removes one; `list` shows what the slice holds.",
		GroupID: "operations",
		Example: "  drift atomic alert list",
	}
	cmd.AddCommand(getAlertListCmd())
	return cmd
}

func getAlertListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List alerts registered on the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/alert", nil)
			if err != nil {
				return common.TransportError("list alerts", err)
			}
			defer resp.Body.Close()
			body, err := common.CheckResponse(resp, "list alerts")
			if err != nil {
				return err
			}
			var specs []alertSpec
			_ = json.Unmarshal(body, &specs)
			if len(specs) == 0 {
				fmt.Println("No alerts registered.")
				return nil
			}
			fmt.Printf("%-24s  %-24s  %-8s  %-9s  %-7s  %s\n",
				"NAME", "FUNCTION", "TRIGGER", "THRESHOLD", "WINDOW", "NOTIFY")
			for _, s := range specs {
				// The webhook is a credential: its type is shown, never its URL.
				fmt.Printf("%-24s  %-24s  %-8s  %-9d  %-7s  %s\n",
					s.Name, s.Function, s.Trigger, s.Threshold, alertWindow(s.WindowSec), s.NotifyType)
			}
			return nil
		},
	}
}

func alertWindow(sec int) string {
	switch {
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}
