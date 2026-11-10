// Package wallet is `drift wallet`: the euro balance on your account, what has
// moved through it, and a top-up.
//
// The platform's payments service owns the ledger and every decision about money
// (the smallest top-up, what is credited, and when). These commands only ask the
// api's /ops/wallet routes and print the answer; the CLI holds no copy of a
// minimum or a price. The account is the signed-in one: no command here names an
// account, and the api would ignore one if it were sent.
package wallet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// GetCmd returns the `drift wallet` group.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wallet",
		Short: "Your euro balance: see it, see its history, add to it",
		Long: "Your account holds a balance in euros. It is money you have paid in and not\n" +
			"yet spent; nothing is ever taken from it except for a price shown to you\n" +
			"first.",
		Example: "  drift wallet balance\n" +
			"  drift wallet history\n" +
			"  drift wallet topup 10",
		GroupID: "account",
	}
	cmd.AddCommand(getBalanceCmd(), getHistoryCmd(), getTopUpCmd())
	return cmd
}

type balanceReply struct {
	Account      string `json:"account"`
	BalanceCents int64  `json:"balance_cents"`
}

func getBalanceCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "balance",
		Short:        "Print your balance in euros",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example:      "  drift wallet balance",
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := call("GET", "/ops/wallet/balance", nil, nil, "read your balance")
			if err != nil {
				return err
			}
			if asJSON {
				fmt.Println(strings.TrimSpace(string(body)))
				return nil
			}
			var b balanceReply
			if err := json.Unmarshal(body, &b); err != nil {
				return fmt.Errorf("the platform returned a balance this version cannot read: %w", err)
			}
			fmt.Println(formatEuros(b.BalanceCents))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the platform's reply verbatim")
	return cmd
}

// historyEntry is one line of the ledger. AmountCents is signed: money in is
// positive, money out is negative.
type historyEntry struct {
	Seq          int64     `json:"seq"`
	Kind         string    `json:"kind"`
	AmountCents  int64     `json:"amount_cents"`
	BalanceCents int64     `json:"balance_cents"`
	Ref          string    `json:"ref"`
	At           time.Time `json:"at"`
}

// defaultHistoryLimit is the page this command asks for, so it knows when a
// page was full and there may be more.
const defaultHistoryLimit = 20

func getHistoryCmd() *cobra.Command {
	var (
		limit  int
		before int64
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:          "history",
		Short:        "List what moved your balance, newest first",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: "  drift wallet history\n" +
			"  drift wallet history --limit 100\n" +
			"  drift wallet history --before 41",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 200 {
				return fmt.Errorf("--limit must be between 1 and 200")
			}
			if before < 0 {
				return fmt.Errorf("--before must be an entry number from a previous page")
			}
			q := url.Values{"limit": {strconv.Itoa(limit)}}
			if before > 0 {
				q.Set("before", strconv.FormatInt(before, 10))
			}
			body, err := call("GET", "/ops/wallet/history", q, nil, "read your wallet history")
			if err != nil {
				return err
			}
			if asJSON {
				fmt.Println(strings.TrimSpace(string(body)))
				return nil
			}
			var page struct {
				Entries []historyEntry `json:"entries"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return fmt.Errorf("the platform returned a history this version cannot read: %w", err)
			}
			if len(page.Entries) == 0 {
				if before > 0 {
					fmt.Println("No older entries.")
				} else {
					fmt.Println("Nothing has moved your balance yet.")
				}
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WHEN\tWHAT\tAMOUNT\tBALANCE\tFOR")
			for _, e := range page.Entries {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					e.At.Local().Format("2006-01-02 15:04:05"),
					kindLabel(e.Kind),
					formatSigned(e.AmountCents),
					formatEuros(e.BalanceCents),
					dashIfEmpty(e.Ref),
				)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(page.Entries) >= limit {
				oldest := page.Entries[len(page.Entries)-1].Seq
				fmt.Printf("\n%s\n", common.Hint(fmt.Sprintf("older: drift wallet history --before %d", oldest)))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", defaultHistoryLimit, "entries per page (1 to 200)")
	cmd.Flags().Int64Var(&before, "before", 0, "only entries older than this entry number, from a previous page")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the platform's reply verbatim")
	return cmd
}

type topUpReply struct {
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
	PayURL    string `json:"pay_url"`
}

func getTopUpCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "topup <euros>",
		Short: "Add euros to your balance",
		Long: "Starts a top-up of the amount you give, in euros, and prints where to pay.\n" +
			"The balance moves when the payment is confirmed, not when you run this.\n\n" +
			"The smallest top-up is the platform's to say; if the amount is too small the\n" +
			"refusal tells you what it is.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: "  drift wallet topup 10\n" +
			"  drift wallet topup 12.50",
		RunE: func(cmd *cobra.Command, args []string) error {
			cents, err := parseEuros(args[0])
			if err != nil {
				return err
			}
			req, err := json.Marshal(map[string]int64{"amount_cents": cents})
			if err != nil {
				return err
			}
			body, err := call("POST", "/ops/wallet/topup", nil, req, "start a top-up")
			if err != nil {
				return err
			}
			if asJSON {
				fmt.Println(strings.TrimSpace(string(body)))
				return nil
			}
			var t topUpReply
			if err := json.Unmarshal(body, &t); err != nil {
				return fmt.Errorf("the platform returned a top-up this version cannot read: %w", err)
			}
			printTopUp(cents, t)
			if t.Status == "paid" {
				// The payment is already confirmed, so the balance read now
				// includes it. A failed read here changes nothing about the
				// top-up and is not reported as one.
				if b, err := call("GET", "/ops/wallet/balance", nil, nil, "read your balance"); err == nil {
					var br balanceReply
					if json.Unmarshal(b, &br) == nil {
						fmt.Printf("Your balance is %s.\n", formatEuros(br.BalanceCents))
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the platform's reply verbatim")
	return cmd
}

// printTopUp says what a started top-up is, by the status the platform gave.
// Only "paid" says the money arrived.
func printTopUp(cents int64, t topUpReply) {
	amount := formatEuros(cents)
	switch t.Status {
	case "paid":
		fmt.Printf("Topped up %s (payment %s).\n", amount, t.PaymentID)
	case "pending":
		fmt.Printf("Started a top-up of %s (payment %s). Your balance moves when it is paid.\n", amount, t.PaymentID)
		if t.PayURL != "" {
			fmt.Printf("Pay at: %s\n", t.PayURL)
		}
	default:
		fmt.Printf("The top-up of %s (payment %s) is %s. Nothing was added to your balance.\n", amount, t.PaymentID, dashIfEmpty(t.Status))
	}
}

// call makes one request to the api and returns the body of a 2xx.
func call(method, path string, query url.Values, payload []byte, op string) ([]byte, error) {
	target := common.APIBaseURL + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	var (
		resp *http.Response
		err  error
	)
	if payload != nil {
		resp, err = common.DoJSONRequest(method, target, bytes.NewReader(payload))
	} else {
		resp, err = common.DoRequest(method, target, nil)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return common.CheckResponse(resp, op)
}

// kindLabel names a ledger entry kind the way a person says it. A kind this
// version does not know is printed as the platform sent it.
func kindLabel(kind string) string {
	switch kind {
	case "topup":
		return "top-up"
	case "grant":
		return "credit granted"
	case "booking":
		return "slice period"
	case "renewal":
		return "renewal"
	case "resize":
		return "resize"
	case "refund":
		return "refund"
	}
	return dashIfEmpty(kind)
}

// formatSigned prints a movement with an explicit sign: money in is +€10.00,
// money out is -€2.40.
func formatSigned(cents int64) string {
	if cents >= 0 {
		return "+" + formatEuros(cents)
	}
	return formatEuros(cents)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
