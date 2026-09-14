// link.go — `drift deed link list` and `drift deed link get <identity>`.
//
// The support read for device enrollment. "My device link session never
// completed" and "I revoked that laptop, why does it still work?" are the two
// tickets this answers, and both need the device registry rather than a count.
//
// THE BOOTSTRAP AXIOM IS WHY `get` IS THE INTERESTING HALF. Link only writes a
// registry file the first time an attestation succeeds. Before that an identity
// still has exactly one active device — its own account pubkey — so `list`
// cannot see it and an empty answer from `get` would state the opposite of the
// truth. The slice marks that case `implicit`, and this prints it as the
// implicit device it is rather than as an empty list.
package deed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func linkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "link",
		Short:   "Inspect Deed device enrollment (which devices act for an identity)",
		Example: "  drift deed link list\n  drift deed link get alice",
	}
	cmd.AddCommand(linkListCmd(), linkGetCmd())
	return cmd
}

func linkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the identities that have a device registry",
		Long: `List the identities that have a device registry.

An identity appears here only once an attestation has actually succeeded for it
— that is when Link first writes anything to disk. An identity that has never
enrolled a second device still has one active device (its own account pubkey)
and will NOT be listed; ask for it by name with 'drift deed link get <identity>',
which reports that implicit device honestly.`,
		Example: "  drift deed link list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/deed/admin/link/list", nil)
			if err != nil {
				e := common.TransportError("list link identities", err)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list link identities")
			if err != nil {
				return err
			}

			var identities []string
			if err := json.Unmarshal(body, &identities); err != nil {
				e := fmt.Errorf("couldn't read the list response: %w", err)
				return e
			}
			if len(identities) == 0 {
				fmt.Println("(no identities have enrolled a device)")
				return nil
			}
			for _, id := range identities {
				fmt.Println(id)
			}
			fmt.Printf("\n%d identit(y/ies) with a device registry\n", len(identities))
			return nil
		},
	}
}

// linkDevice is one row of an identity's device registry.
//
// EnrolledAt is a pointer because the implicit genesis device has none: nothing
// ever enrolled it, and printing a zero would claim it was enrolled at the Unix
// epoch. Absent and zero are different facts and the column says so.
type linkDevice struct {
	Pubkey     string `json:"pubkey"`
	EnrolledAt *int64 `json:"enrolled_at"`
	AttestedBy string `json:"attested_by"`
	Revoked    bool   `json:"revoked"`
}

func linkGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <identity>",
		Short: "Show the devices enrolled for one identity",
		Long: `Show the devices enrolled for one identity, revoked ones included.

An identity that has never completed an attestation has no registry on disk and
exactly one implicit active device — its own account pubkey. That is reported as
such rather than as an empty list.

A registry that exists and cannot be decrypted is refused rather than reported
as "no devices": the file may hold revocations, so guessing would describe a
removed device as active.`,
		Example: "  drift deed link get alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identity := args[0]
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			reqURL := fmt.Sprintf("%s/ops/deed/admin/link/get?identity=%s",
				common.APIBaseURL, url.QueryEscape(identity))
			resp, err := common.DoRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				e := common.TransportError("read device registry", err)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "read device registry")
			if err != nil {
				return err
			}

			var reg struct {
				Identity string       `json:"identity"`
				Implicit bool         `json:"implicit"`
				Devices  []linkDevice `json:"devices"`
			}
			if err := json.Unmarshal(body, &reg); err != nil {
				e := fmt.Errorf("couldn't read the registry response: %w", err)
				return e
			}

			fmt.Printf("%s Devices for %s\n\n", common.Highlight(""), reg.Identity)
			if reg.Implicit {
				fmt.Println("  This identity has never completed an attestation, so Link holds no")
				fmt.Println("  registry for it. The device below is the implicit one the account")
				fmt.Println("  pubkey grants — it is active, and nothing enrolled it.")
				fmt.Println()
			}
			for _, d := range reg.Devices {
				state := "active"
				if d.Revoked {
					state = "REVOKED"
				}
				enrolled := "—"
				if d.EnrolledAt != nil {
					enrolled = fmt.Sprintf("%d", *d.EnrolledAt)
				}
				fmt.Printf("  %-8s %s\n", state, d.Pubkey)
				fmt.Printf("           attested by %s, enrolled at %s\n", d.AttestedBy, enrolled)
			}
			fmt.Printf("\n%d device(s)\n", len(reg.Devices))
			return nil
		},
	}
}
