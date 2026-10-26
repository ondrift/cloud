package atomic_cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Trigger() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "List a slice's triggers, or remove one",
		Long: "A function's triggers are declared in the Driftfile: a function labelled QUEUE and the queue it " +
			"drains (with `poll` and `max_retries` to tune it), and a `cron` for a schedule. Nothing here " +
			"registers one. `list` shows what the slice is running, and `unregister` removes a trigger the " +
			"Driftfile no longer declares.",
		Example: "  drift atomic trigger list\n  drift atomic trigger unregister order-processor",
		GroupID: "operations",
	}

	cmd.AddCommand(triggerListCmd(), triggerUnregisterCmd())
	return cmd
}

func triggerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all registered triggers",
		Example: "  drift atomic trigger list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/trigger/list",
				nil,
			)
			if err != nil {
				e := common.TransportError("list triggers", err)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list triggers")
			if err != nil {
				return err
			}

			var triggers []map[string]any
			if err := json.Unmarshal(b, &triggers); err != nil || len(triggers) == 0 {
				fmt.Println("No triggers registered.")
				return nil
			}

			fmt.Printf("%-32s  %-10s  %s\n", "NAME", "TYPE", "SOURCE / SCHEDULE")
			fmt.Printf("%-32s  %-10s  %s\n", "--------------------------------", "----------", "------------------")
			for _, t := range triggers {
				name, _ := t["name"].(string)
				typ, _ := t["type"].(string)
				detail := ""
				switch typ {
				case "queue":
					detail, _ = t["source"].(string)
				case "webhook":
					detail, _ = t["path"].(string)
				case "schedule":
					detail, _ = t["schedule"].(string)
				}
				fmt.Printf("%-32s  %-10s  %s\n", name, typ, detail)
			}
			return nil
		},
	}
}

func triggerUnregisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unregister <name>",
		Short:   "Unregister a trigger by name",
		Example: "  drift atomic trigger unregister order-processor",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			payload, _ := json.Marshal(map[string]string{"name": name})
			resp, err := common.DoJSONRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/trigger/unregister",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("unregister the trigger", err)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "unregister the trigger"); err != nil {
				return err
			}

			fmt.Printf("Trigger %q unregistered\n", name)
			return nil
		},
	}
}
