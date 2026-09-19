package atomic_cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Auth() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "auth",
		Short:   "Manage API key authentication for deployed functions",
		Example: "  drift atomic auth set send-email my-secret-key\n  drift atomic auth list send-email\n  drift atomic auth revoke send-email --method get",
		GroupID: "operations",
	}
	cmd.AddCommand(authSet(), authList(), authRevoke())
	return cmd
}

// resolveAuthMethod decides which method `auth set`/`auth revoke` acts
// against. An explicit --method is always honoured, unchanged.
//
// Otherwise the flag's own "post" default is silently wrong the moment the
// function is not deployed as POST: it gates a method the function does not
// answer on while reporting success, and the route it actually serves stays
// permanently 503 with nothing pointing at the mismatch (ATM-47). So when the
// function resolves to EXACTLY one deployed method, that method is used
// instead of the default.
//
// Falls back to the flag's default in the two cases where a real answer
// is not available: the function is not found (not deployed yet, or a typo
// the platform will refuse on its own — not this command's job to diagnose),
// or the platform could not be reached (best-effort; an outage must not block
// the command). Refuses outright only when the name resolves to MORE than one
// method — get:x and post:x are different functions, and guessing between
// them would be exactly as wrong as the bug this fixes.
func resolveAuthMethod(cmd *cobra.Command, function, flagMethod string) (string, error) {
	if cmd.Flags().Changed("method") {
		return flagMethod, nil
	}
	slots, err := fetchSlots()
	if err != nil {
		return flagMethod, nil
	}
	var matches []atomicRecord
	for _, s := range slots {
		if s.FunctionName == function {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].Method, nil
	case 0:
		return flagMethod, nil
	default:
		methods := make([]string, len(matches))
		for i, m := range matches {
			methods[i] = strings.ToLower(m.Method)
		}
		return "", fmt.Errorf(
			"%q is deployed under more than one method (%s) -- pass --method to say which one",
			function, strings.Join(methods, ", "))
	}
}

// authSet sets or rotates the API key for a function+method.
func authSet() *cobra.Command {
	var method string

	cmd := &cobra.Command{
		Use:     "set <function-name> <api-key>",
		Short:   "Set or rotate the API key for a deployed function",
		Example: "  drift atomic auth set send-email my-secret-key\n  drift atomic auth set send-email my-secret-key --method get",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]
			key := args[1]

			resolved, rerr := resolveAuthMethod(cmd, function, method)
			if rerr != nil {
				return rerr
			}

			body, _ := json.Marshal(map[string]string{
				"function": function,
				"method":   strings.ToUpper(resolved),
				"key":      key,
			})

			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/atomic/auth",
				bytes.NewReader(body),
			)
			if err != nil {
				return common.TransportError("set the API key", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "set the API key"); err != nil {
				return err
			}

			fmt.Printf("API key set for %s %s\n", strings.ToUpper(resolved), function)
			return nil
		},
	}

	cmd.Flags().StringVarP(&method, "method", "m", "post", "HTTP method of the function (get, post, put, delete)")
	return cmd
}

// authList shows key fingerprints configured for a function.
func authList() *cobra.Command {
	return &cobra.Command{
		Use:     "list <function-name>",
		Short:   "List API keys configured for a deployed function",
		Example: "  drift atomic auth list send-email",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/atomic/auth?function="+function,
				nil,
			)
			if err != nil {
				return common.TransportError("list API keys", err)
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list API keys")
			if err != nil {
				return err
			}

			var keys []struct {
				Method      string `json:"method"`
				Path        string `json:"path"`
				Fingerprint string `json:"fingerprint"`
			}
			if err := json.Unmarshal(b, &keys); err != nil || len(keys) == 0 {
				fmt.Printf("No API keys configured for %s\n", function)
				return nil
			}

			fmt.Printf("%-8s  %-24s  %s\n", "METHOD", "FUNCTION", "KEY")
			fmt.Printf("%-8s  %-24s  %s\n", "--------", "------------------------", "-------")
			for _, k := range keys {
				fmt.Printf("%-8s  %-24s  %s\n", k.Method, k.Path, k.Fingerprint)
			}
			return nil
		},
	}
}

// authRevoke removes the API key for a function+method.
func authRevoke() *cobra.Command {
	var method string

	cmd := &cobra.Command{
		Use:     "revoke <function-name>",
		Short:   "Revoke the API key for a deployed function",
		Example: "  drift atomic auth revoke send-email\n  drift atomic auth revoke send-email --method delete",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			resolved, rerr := resolveAuthMethod(cmd, function, method)
			if rerr != nil {
				return rerr
			}

			body, _ := json.Marshal(map[string]string{
				"function": function,
				"method":   strings.ToUpper(resolved),
			})

			resp, err := common.DoJSONRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/atomic/auth",
				bytes.NewReader(body),
			)
			if err != nil {
				return common.TransportError("revoke the API key", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "revoke the API key"); err != nil {
				return err
			}

			fmt.Printf("API key revoked for %s %s\n", strings.ToUpper(resolved), function)
			return nil
		},
	}

	cmd.Flags().StringVarP(&method, "method", "m", "post", "HTTP method of the function (get, post, put, delete)")
	return cmd
}
