package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// warmupPoll and warmupLimit bound the wait for a cooled slice to come back.
var (
	warmupPoll  = 5 * time.Second
	warmupLimit = 3 * time.Minute
)

func getWarmupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "warmup",
		Short: "Bring a cooled-down slice back",
		Long: "A free slice that no request has reached for 30 days is scaled to zero to free its memory; " +
			"its data stays where it was. A request to its site, or any command that needs it, wakes it by " +
			"itself and waits. This wakes it now, for the moment before traffic arrives, and returns when it is " +
			"running. On a slice that is already running it does nothing.",
		Example: "  drift slice warmup",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}
			started := time.Now()
			told := false
			for {
				state, err := askWarmup()
				if err != nil {
					return err
				}
				if state == "running" {
					if told {
						fmt.Printf("Slice %s is warm.\n", common.Highlight(name))
					} else {
						fmt.Printf("Slice %s is already running.\n", common.Highlight(name))
					}
					return nil
				}
				if !told {
					fmt.Printf("Slice %s was cooled down. Warming it up, about 20 seconds...\n", common.Highlight(name))
					told = true
				}
				if time.Since(started) > warmupLimit {
					return fmt.Errorf("slice %s is still warming after %s; run `drift slice warmup` again, or `drift slice info` to see what it is doing",
						name, warmupLimit)
				}
				time.Sleep(warmupPoll)
			}
		},
	}
}

// askWarmup posts the warm-up and reports the state the platform answered:
// "running" when the slice was not cooled or has finished coming back, "warming"
// while it is on its way.
func askWarmup() (string, error) {
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/warmup", nil)
	if err != nil {
		return "", common.TransportError("warm up slice", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "warm up slice")
	if err != nil {
		return "", err
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.State == "" {
		return "", fmt.Errorf("Couldn't warm up the slice: the API response didn't look right")
	}
	return out.State, nil
}
