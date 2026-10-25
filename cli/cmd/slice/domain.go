// domain.go — `drift slice domain {verify, list, remove}`.
// Thin wrappers around the API gateway's /ops/slice/domain* endpoints. A host is
// attached by declaring a `domain` block in the Driftfile's edge block and
// running `drift apply`; there is no command that adds one.
package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

type domainResponse struct {
	Host         string   `json:"host"`
	Status       string   `json:"status"`
	StatusDetail string   `json:"status_detail,omitempty"`
	TXTToken     string   `json:"txt_token,omitempty"`
	Verify       string   `json:"verify,omitempty"`
	Instructions []string `json:"instructions,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	Wildcard     bool     `json:"wildcard,omitempty"`
}

func getDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Verify, list, or remove custom hostnames for your slice (declare them in the Driftfile)",
		Example: "  drift slice domain verify forms.gemeente.example\n" +
			"  drift slice domain list\n" +
			"  drift slice domain remove forms.gemeente.example",
	}
	cmd.AddCommand(
		getDomainVerifyCmd(),
		getDomainListCmd(),
		getDomainRemoveCmd(),
	)
	return cmd
}

func getDomainVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <host>",
		Short: "Re-check the TXT record and (on success) issue the TLS certificate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			host := strings.ToLower(strings.TrimSpace(args[0]))
			u := common.APIBaseURL + "/ops/slice/domain/verify?host=" + url.QueryEscape(host)
			resp, err := common.DoJSONRequest(http.MethodPost, u, strings.NewReader("{}"))
			if err != nil {
				return common.TransportError("verify domain", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "verify domain")
			if err != nil {
				return err
			}
			var d domainResponse
			_ = json.Unmarshal(respBody, &d)
			switch d.Status {
			case "live":
				fmt.Printf("✔ %s is live.\n", d.Host)
			case "verified":
				fmt.Printf("✔ %s verified — issuing the TLS certificate now. Re-run `drift slice domain list` to check progress.\n", d.Host)
				if d.Detail != "" {
					fmt.Printf("  detail: %s\n", d.Detail)
				}
			default:
				fmt.Printf("Status: %s\n", d.Status)
				if d.StatusDetail != "" {
					fmt.Printf("Detail: %s\n", d.StatusDetail)
				}
			}
			return nil
		},
	}
}

func getDomainListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List custom hostnames for the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/domain", nil)
			if err != nil {
				return common.TransportError("list domains", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "list domains")
			if err != nil {
				return err
			}
			var domains []domainResponse
			_ = json.Unmarshal(respBody, &domains)
			if len(domains) == 0 {
				fmt.Println("No custom domains registered for this slice.")
				return nil
			}
			fmt.Printf("%-40s  %-10s  %-9s  %s\n", "HOST", "STATUS", "WILDCARD", "DETAIL")
			for _, d := range domains {
				detail := d.StatusDetail
				if d.TXTToken != "" && d.Status == "pending" {
					detail = "TXT token: " + d.TXTToken
				}
				wildcard := ""
				if d.Wildcard {
					wildcard = "yes"
				}
				fmt.Printf("%-40s  %-10s  %-9s  %s\n", d.Host, d.Status, wildcard, detail)
			}
			return nil
		},
	}
}

func getDomainRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <host>",
		Short: "Remove a custom hostname (also removes its TLS certificate and routing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			host := strings.ToLower(strings.TrimSpace(args[0]))
			u := common.APIBaseURL + "/ops/slice/domain?host=" + url.QueryEscape(host)
			resp, err := common.DoRequest(http.MethodDelete, u, nil)
			if err != nil {
				return common.TransportError("remove domain", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, _ = common.CheckResponse(resp, "remove domain")
				return nil
			}
			fmt.Printf("Domain %s removed.\n", host)
			return nil
		},
	}
}
