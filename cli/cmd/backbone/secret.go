package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func secretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "secret",
		Short:   "Manage encrypted secrets in your slice",
		Example: "  drift backbone secret set API_KEY=sk-abc123\n  drift backbone secret get API_KEY\n  drift backbone secret list\n  drift backbone secret previous WEBHOOK_SECRET\n  drift backbone secret delete API_KEY",
	}
	cmd.AddCommand(secretSetCmd(), secretGetCmd(), secretListCmd(), secretPreviousCmd(), secretDeleteCmd())
	return cmd
}

func secretSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set KEY=VALUE",
		Short:   "Store an encrypted secret",
		Example: "  drift backbone secret set API_KEY=sk-abc123\n  drift backbone secret set DB_PASSWORD=hunter2",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			parts := strings.SplitN(args[0], "=", 2)
			if len(parts) != 2 || parts[0] == "" {
				e := fmt.Errorf("Couldn't store secret: argument must be in KEY=VALUE format.")
				return e
			}
			name, value := parts[0], parts[1]

			body, _ := json.Marshal(map[string]string{"name": name, "value": value})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/secret/set",
				bytes.NewBuffer(body),
			)
			if err != nil {
				e := common.TransportError("store secret", err)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "store secret"); err != nil {
				return err
			}

			fmt.Printf("Secret %q stored\n", name)
			// Say so only when a window actually opened. Replacing a value opens
			// one; storing a name for the first time, or rewriting the same
			// value, does not — and a line printed unconditionally would tell a
			// tenant setting their first secret that an old one is still live.
			//
			// Asked rather than assumed, because the CLI cannot tell which of
			// those just happened. The answer is a status code; the replaced
			// value never crosses the wire.
			if secretWindowIsOpen(name) {
				fmt.Printf("  The value it replaced stays readable to your functions for %s.\n", previousWindow)
				fmt.Printf("  Check with: drift backbone secret previous %s\n", name)
			}
			return nil
		},
	}
}

// previousWindow is how long a replaced value keeps verifying, for the message
// above. It mirrors the slice's own constant and is DESCRIPTIVE — the slice
// decides, and nothing here can change it.
const previousWindow = "24 hours"

// secretWindowIsOpen reports whether this secret's replaced value is still
// inside its grace window.
//
// Presence only: 204 means open, 404 means closed, and the value itself is
// never returned by that route. A transport failure reads as closed, because
// the alternative is telling somebody an old credential is still live when we
// could not check.
func secretWindowIsOpen(name string) bool {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/backbone/secret/previous?name="+url.QueryEscape(name),
		nil,
	)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func secretPreviousCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "previous KEY",
		Short: "Say whether this secret's replaced value is still accepted",
		Long: "Replacing a secret keeps the value it replaced readable to your functions\n" +
			"for " + previousWindow + ", so a handler that VERIFIES a credential can accept both\n" +
			"while the other side catches up — a webhook signing secret is the usual\n" +
			"case. Your function reads it with the SDK's Secret.Previous().\n\n" +
			"This reports WHETHER the window is open, never the value. A value you\n" +
			"have already replaced is one you may well be rotating because it leaked;\n" +
			"handing it back through the CLI would be a second way to read it.",
		Example: "  drift backbone secret previous WEBHOOK_SECRET",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			if secretWindowIsOpen(args[0]) {
				fmt.Printf("%q: the value it replaced is still accepted (up to %s after the change).\n",
					args[0], previousWindow)
				return nil
			}
			fmt.Printf("%q: no previous value is accepted — only the current one verifies.\n", args[0])
			return nil
		},
	}
}

func secretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get KEY",
		Short:   "Retrieve the value of a secret",
		Example: "  drift backbone secret get API_KEY",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/secret/get?name="+args[0],
				nil,
			)
			if err != nil {
				e := common.TransportError("get secret", err)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "get secret")
			if err != nil {
				return err
			}

			fmt.Println(string(b))
			return nil
		},
	}
}

func secretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List secret names",
		Example: "  drift backbone secret list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/secret/list",
				nil,
			)
			if err != nil {
				e := common.TransportError("list secrets", err)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list secrets")
			if err != nil {
				return err
			}

			var names []string
			if err := json.Unmarshal(b, &names); err != nil || len(names) == 0 {
				fmt.Println("No secrets stored.")
				return nil
			}
			for _, n := range names {
				fmt.Println(n)
			}
			return nil
		},
	}
}

func secretDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete KEY",
		Short:   "Delete a secret",
		Example: "  drift backbone secret delete API_KEY",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := common.DoJSONRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/backbone/secret/delete",
				bytes.NewBuffer(body),
			)
			if err != nil {
				e := common.TransportError("delete secret", err)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete secret"); err != nil {
				return err
			}

			fmt.Printf("Secret %q deleted\n", args[0])
			return nil
		},
	}
}
