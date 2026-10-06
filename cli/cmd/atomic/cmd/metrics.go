package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Metrics() *cobra.Command {
	return &cobra.Command{
		Use:     "metrics <pulse-name>",
		Short:   "Show request metrics for a deployed pulse",
		Example: "  drift atomic metrics send-email",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/atomic/metrics?function="+function,
				nil,
			)
			if err != nil {
				return common.TransportError("fetch pulse metrics", err)
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "fetch pulse metrics")
			if err != nil {
				return err
			}

			var m struct {
				TotalRequests int64   `json:"total_requests"`
				ErrorRequests int64   `json:"error_requests"`
				ErrorRate     float64 `json:"error_rate"`
				AvgDurationMs float64 `json:"avg_duration_ms"`
			}
			if err := json.Unmarshal(b, &m); err != nil {
				return fmt.Errorf("Couldn't fetch pulse metrics: the API response didn't look right (%w)", err)
			}

			fmt.Printf("Pulse:           %s\n", function)
			fmt.Printf("Total requests:  %d\n", m.TotalRequests)
			fmt.Printf("Error requests:  %d\n", m.ErrorRequests)
			fmt.Printf("Error rate:      %.1f%%\n", m.ErrorRate*100)
			fmt.Printf("Avg duration:    %.1fms\n", m.AvgDurationMs)
			return nil
		},
	}
}
