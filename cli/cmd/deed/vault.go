// vault.go — `drift deed vault list` and `drift deed vault get <uid>`.
//
// The support read for Deed's keyring. Deed was the only one of the four
// pillars with no per-record visibility from the CLI at all: its whole operator
// surface was `drift deed status`, three integers, so "I think my Vault backup
// is corrupted" could only be answered with filesystem access to the box.
//
// WHY `list` AND `get` ARE SEPARATE ACTS. The slice also serves
// `/admin/vault/dump`, which returns every uid's decrypted blob in one
// response. Pointing a CLI at that and filtering here would have been the short
// way to both commands — and it would mean answering a question about one
// customer put every other customer's plaintext on the wire, and into whatever
// shell history and log the answer reached. So `list` names uids and carries no
// values at all, and seeing an actual blob is the deliberate, single-record act
// of naming one.
package deed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func vaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vault",
		Short:   "Inspect the Deed vault (the account-key-wrapped keyring)",
		Example: "  drift deed vault list\n  drift deed vault get alice",
	}
	cmd.AddCommand(vaultListCmd(), vaultGetCmd())
	return cmd
}

func vaultListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the uids that have written a vault entry",
		Long: `List the uids that have written a vault entry.

Names only — never the stored values. To see one uid's blob, ask for it by name
with 'drift deed vault get <uid>'.`,
		Example: "  drift deed vault list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/deed/admin/vault/list", nil)
			if err != nil {
				e := common.TransportError("list vault entries", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list vault entries")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var uids []string
			if err := json.Unmarshal(body, &uids); err != nil {
				e := fmt.Errorf("couldn't read the list response: %w", err)
				fmt.Println(e)
				return e
			}
			if len(uids) == 0 {
				fmt.Println("(no vault entries)")
				return nil
			}
			for _, uid := range uids {
				fmt.Println(uid)
			}
			fmt.Printf("\n%d uid(s)\n", len(uids))
			return nil
		},
	}
}

func vaultGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <uid>",
		Short: "Show one uid's current vault blob",
		Long: `Show one uid's current vault blob.

Vault is append-only, so this is the NEWEST entry that uid has written, not its
whole history. The blob is client-encrypted before it ever reaches Drift — what
prints here is the ciphertext the customer's own application stored, not
anything this platform can read.`,
		Example: "  drift deed vault get alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid := args[0]
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			reqURL := fmt.Sprintf("%s/ops/deed/admin/vault/get?uid=%s",
				common.APIBaseURL, url.QueryEscape(uid))
			resp, err := common.DoRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				e := common.TransportError("read vault entry", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "read vault entry")
			if err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Println(string(body))
			return nil
		},
	}
}
