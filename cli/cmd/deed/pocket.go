// pocket.go — `drift deed pocket list <identity>`.
//
// The support read for an identity's E2EE app data. "A Pocket key isn't showing
// up on my second device" is answerable with this and was not answerable at all
// before.
//
// KEY NAMES ONLY, AND THAT IS STRUCTURAL RATHER THAN CAREFUL. Pocket payloads
// are ciphertext before they reach Drift and the slice holds no key for them, so
// there is no version of this command that could over-expose a customer's data
// even by mistake. That makes it the safest of Deed's three support reads and
// worth saying out loud: the restraint Vault and Link need is enforced here by
// the design rather than by the caller.
//
// There is no `pocket get`. A value would print as ciphertext nobody can read,
// so it would answer no question a support ticket asks while moving a
// customer's data somewhere it need not go.
package deed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func pocketCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pocket",
		Short:   "Inspect Deed pocket (an identity's end-to-end encrypted app data)",
		Example: "  drift deed pocket list alice",
	}
	cmd.AddCommand(pocketListCmd())
	return cmd
}

func pocketListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <identity>",
		Short: "List the pocket key names stored for one identity",
		Long: `List the pocket key names stored for one identity.

Names only, and never values — Pocket payloads are encrypted client-side and the
slice holds no key for them, so a value could not be shown even if it were
wanted.

An identity that has stored nothing and one that does not exist are the same
answer here. Pocket creates an identity's storage on its first write, so the two
states are genuinely identical and there is nothing to tell apart.`,
		Example: "  drift deed pocket list alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identity := args[0]
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			reqURL := fmt.Sprintf("%s/ops/deed/admin/pocket/list?identity=%s",
				common.APIBaseURL, url.QueryEscape(identity))
			resp, err := common.DoRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				e := common.TransportError("list pocket keys", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list pocket keys")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var keys []string
			if err := json.Unmarshal(body, &keys); err != nil {
				e := fmt.Errorf("couldn't read the list response: %w", err)
				fmt.Println(e)
				return e
			}
			if len(keys) == 0 {
				fmt.Printf("(no pocket items for %s)\n", identity)
				return nil
			}
			for _, k := range keys {
				fmt.Println(k)
			}
			fmt.Printf("\n%d key(s)\n", len(keys))
			return nil
		},
	}
}
