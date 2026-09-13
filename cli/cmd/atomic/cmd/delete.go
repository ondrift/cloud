package atomic_cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// splitBookingKey accepts the `method:route` form every other surface prints and
// returns the two halves.
//
// # Why this is not just a nicety
//
// `method:route` is the ONE name a function has everywhere else: it is what the
// Driftfile declares, what `functionBudgets` renders, and what the
// undeclared-deploy refusal prints back at the user. This command took the route
// and a `--method` flag, so pasting the name shown by every other surface failed
// with "function not found" — which reads as "that function is already gone",
// not "your name is in the wrong shape". A user then believes the delete
// succeeded, or that something else removed it.
//
// ONLY A KNOWN METHOD SPLITS. A route may legitimately contain a colon — a path
// parameter is spelled `users/:id` in some grammars, and a blob key can carry
// one — so splitting on any colon would mangle a name that was correct. An
// unrecognised prefix is left alone and travels as the route it is.
func splitBookingKey(name string) (route, method string, split bool) {
	prefix, rest, found := strings.Cut(name, ":")
	if !found || rest == "" {
		return name, "", false
	}
	switch strings.ToLower(prefix) {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return rest, strings.ToLower(prefix), true
	}
	return name, "", false
}

func Delete() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a deployed atomic function by name",
		Long: "Takes either form of the name:\n\n" +
			"  drift atomic delete get:users     the booking key, as the Driftfile declares it\n" +
			"  drift atomic delete users --method get\n\n" +
			"A route containing a colon that is not an HTTP method is left alone, so a\n" +
			"path like `users/:id` still means itself.",
		Example: "  drift atomic delete send-email\n" +
			"  drift atomic delete get:groups/id\n" +
			"  drift atomic delete users --method get",
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

			// The booking key is split into the two halves the route wants. An
			// explicit --method WINS, so a user who passes both is not silently
			// overruled by the prefix — and the two disagreeing is worth saying
			// rather than resolving quietly.
			route, fromKey, split := splitBookingKey(name)
			if split {
				if method != "" && !strings.EqualFold(method, fromKey) {
					return fmt.Errorf("the name says %s and --method says %s — pass one or the other", fromKey, method)
				}
				name, method = route, fromKey
			}

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
