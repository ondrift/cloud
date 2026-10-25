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
		Use:     "trigger",
		Short:   "Manage event triggers for pulses",
		Example: "  drift atomic trigger list\n  drift atomic trigger register queue order-processor --queue orders --target http://localhost:8080\n  drift atomic trigger unregister order-processor",
		GroupID: "operations",
	}

	register := &cobra.Command{
		Use:     "register",
		Short:   "Register a trigger",
		Example: "  drift atomic trigger register queue order-processor --queue orders --target http://localhost:8080",
	}
	register.AddCommand(triggerRegisterQueueCmd())

	cmd.AddCommand(triggerListCmd(), register, triggerUnregisterCmd())
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

func triggerRegisterQueueCmd() *cobra.Command {
	var queue, target string
	var pollMS, maxRetry int
	cmd := &cobra.Command{
		Use:     "queue <name>",
		Short:   "Register a queue trigger: polls a Backbone queue and invokes a pulse on each message",
		Example: "  drift atomic trigger register queue order-processor --queue orders --target http://localhost:8080\n  drift atomic trigger register queue email-sender --queue emails --target http://localhost:8080 --poll-ms 250 --max-retry 5",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			payload, _ := json.Marshal(map[string]any{
				"name":       name,
				"type":       "queue",
				"source":     queue,
				"target_url": target,
				"poll_ms":    pollMS,
				"max_retry":  maxRetry,
			})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/trigger/register",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("register the queue trigger", err)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "register the queue trigger"); err != nil {
				return err
			}

			fmt.Printf("Queue trigger %q registered (queue: %s, poll: %dms)\n", name, queue, pollMS)
			return nil
		},
	}
	cmd.Flags().StringVar(&queue, "queue", "", "Backbone queue name to watch (required)")
	cmd.Flags().StringVar(&target, "target", "", "Target URL to invoke when a message arrives (required)")
	cmd.Flags().IntVar(&pollMS, "poll-ms", 500, "Queue polling interval in milliseconds")
	cmd.Flags().IntVar(&maxRetry, "max-retry", 3, "Maximum retries before moving to dead-letter queue")
	_ = cmd.MarkFlagRequired("queue")
	_ = cmd.MarkFlagRequired("target")
	return cmd
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
