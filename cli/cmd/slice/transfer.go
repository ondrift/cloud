// transfer.go — `drift slice transfer {offer, list, accept, decline, cancel}`.
//
// Handing a running slice to another Drift account. Two-sided: the owner offers
// a slice to a named account, and nothing changes until that account accepts.
// The slice keeps running throughout: its data and its custom domains stay
// exactly where they are, and so does what it costs and how long it is paid for.
// Two things change: the account that controls it, and its address, which is
// named after that account. The old address redirects to the new one for 30 days
// and then stops answering; the offer and the accept both say so, with both
// addresses.
//
// Offering and accepting each ask for the account password, once: the platform
// will not let a session alone give a slice away or take one in.
package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// transferOffer is one offer, as the platform shows it to either party.
//
// Host is the address the slice answers at once the offer is accepted, and
// PreviousHost the one it answers at until then, which redirects to Host for
// RedirectDays after an accept. A platform that does not name PreviousHost does
// not move the address, and the screens then claim no change.
type transferOffer struct {
	ID           string     `json:"id"`
	Slice        string     `json:"slice"`
	From         string     `json:"from"`
	To           string     `json:"to"`
	Status       string     `json:"status"`
	Direction    string     `json:"direction"`
	Host         string     `json:"host"`
	PreviousHost string     `json:"previous_host"`
	RedirectDays int        `json:"redirect_days"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	Terms        struct {
		Tier                string          `json:"tier"`
		MonthlyCostCents    int             `json:"monthly_cost_cents"`
		BillingPeriodMonths int             `json:"billing_period_months"`
		PaidUntil           *time.Time      `json:"paid_until,omitempty"`
		Config              json.RawMessage `json:"config"`
	} `json:"terms"`
}

// transferIDPattern is the shape of an offer id. Anything else given to accept,
// decline or cancel is read as a slice name.
var transferIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

const transferStepUpWhy = "Handing a slice over moves everything in it (every secret, row, file and " +
	"snapshot) and what it costs. Confirm your password to continue."

// transferRedirectDays is how long the address a slice leaves keeps redirecting to
// the one it takes, as the platform documents it. The offer's own redirect_days is
// the figure the platform applies and is used wherever an offer is at hand; this
// one is for the giver's confirmation, which is printed before the platform is
// asked about anything.
const transferRedirectDays = 30

// redirectWindow is a number of days in words: the platform's figure when it sent
// one, and transferRedirectDays when it did not.
func redirectWindow(days int) string {
	if days <= 0 {
		days = transferRedirectDays
	}
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

func getTransferCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "Hand a running slice to another Drift account, or take one in",
		Long: "Hand a running slice to another Drift account. You offer it to an account by\n" +
			"name; nothing changes until that account accepts. The slice keeps running the\n" +
			"whole time: its data and custom domains stay where they are, and its bill and\n" +
			"prepaid period move with it. Its address changes to one named after the new\n" +
			"account; the old address redirects to the new one for " + redirectWindow(transferRedirectDays) + " and then\n" +
			"stops answering. Offers expire after seven days.",
		Example: "  drift slice transfer offer my-shop --to bakkerij\n" +
			"  drift slice transfer list\n" +
			"  drift slice transfer accept my-shop\n" +
			"  drift slice transfer decline my-shop\n" +
			"  drift slice transfer cancel my-shop",
	}
	cmd.AddCommand(
		getTransferOfferCmd(),
		getTransferListCmd(),
		getTransferAcceptCmd(),
		getTransferDeclineCmd(),
		getTransferCancelCmd(),
	)
	return cmd
}

// ─── offer ──────────────────────────────────────────────────────────────────

func getTransferOfferCmd() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "offer <slice> --to <account>",
		Short: "Offer one of your slices to another account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			to = strings.ToLower(strings.TrimSpace(to))
			if to == "" {
				return fmt.Errorf("name the account to offer %s to, with --to", name)
			}

			printOfferWarning(name, to, common.GetUsername())
			first := strings.ToLower(strings.TrimSpace(common.PromptForInput("Offer it? [y/N]")))
			if first != "y" && first != "yes" {
				fmt.Println("Nothing was offered.")
				return nil
			}
			typed := strings.TrimSpace(common.PromptForInput(fmt.Sprintf("Type '%s' to confirm", name)))
			if typed != name {
				fmt.Println("Nothing was offered: the name did not match.")
				return nil
			}

			body, _ := json.Marshal(map[string]string{"slice": name, "to": to})
			respBody, err := transferRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/transfer",
				body, "offer slice", true)
			if err != nil {
				return err
			}
			var o transferOffer
			_ = json.Unmarshal(respBody, &o)
			fmt.Printf("%s Offered %s to %s.\n", common.Check(), name, to)
			fmt.Printf("  They have until %s to accept it with `drift slice transfer accept %s`.\n",
				o.ExpiresAt.Local().Format("2 Jan 2006 15:04"), name)
			fmt.Println(common.Hint("  An offer to a name that is not a Drift account is never accepted; it expires."))
			fmt.Println(common.Hint(fmt.Sprintf("  Change your mind with `drift slice transfer cancel %s`.", name)))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "The account to offer the slice to")
	return cmd
}

// printOfferWarning says exactly what leaves with the slice and what changes
// about it. Giving a slice away cannot be taken back by the giver: only the
// recipient can hand it back.
//
// from is the giver's account name. It is read from the session because the
// platform is not asked until the giver confirms, so both addresses are spelled
// from the slice's name and the two account names.
func printOfferWarning(name, to, from string) {
	fmt.Printf(`
────────────────────────────────────────────────────────────
  You are about to offer slice '%s' to the account '%s'.
────────────────────────────────────────────────────────────

If they accept, the slice becomes theirs, with everything in it:

  • every function, site, collection, bucket, queue and database
  • every secret, including ones you also use elsewhere
  • every snapshot taken of it
  • its custom domains, and its bill and prepaid period

Its address changes, because an address is named after the account that
controls the slice:

%s
The old address redirects to the new one for %s and then stops
answering. A caller that does not follow redirects, such as most webhook
senders, is not served by it even before then, so change anything outside
Drift that names the old address: a webhook or callback URL, an OAuth redirect
URI, a link, a DNS record. Custom domains do not change.

You lose access to it at that moment, and only they can give it back.

Before offering, remove its links to other slices and its alerts.
The platform refuses the offer while either exists.

`, name, to, offerAddresses(name, to, from), redirectWindow(transferRedirectDays))
}

// offerAddresses is the address the slice answers at and the one it takes if the
// offer is accepted. Without the giver's account name only the second can be
// spelled, and it is the one that matters.
func offerAddresses(name, to, from string) string {
	after := fmt.Sprintf("  after accept  %s\n", common.SliceURL(to, name))
	if from == "" {
		return after
	}
	return fmt.Sprintf("  now           %s\n", common.SliceURL(from, name)) + after
}

// ─── list ───────────────────────────────────────────────────────────────────

func getTransferListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the transfer offers you made and the ones made to you",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			offers, err := fetchTransferOffers()
			if err != nil {
				return err
			}
			if len(offers) == 0 {
				fmt.Println("No transfer offers.")
				return nil
			}
			printOffers("Offered to you", offers, "incoming")
			printOffers("Offered by you", offers, "outgoing")
			return nil
		},
	}
}

func printOffers(title string, offers []transferOffer, direction string) {
	var rows []transferOffer
	for _, o := range offers {
		if o.Direction == direction {
			rows = append(rows, o)
		}
	}
	if len(rows) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	fmt.Printf("\n%s\n", title)
	party := "FROM"
	if direction == "outgoing" {
		party = "TO"
	}
	fmt.Printf("  %-24s %-14s %-16s %-10s %-17s %s\n", "SLICE", party, "PRICE", "STATUS", "EXPIRES", "ID")
	for _, o := range rows {
		who := o.From
		if direction == "outgoing" {
			who = o.To
		}
		fmt.Printf("  %-24s %-14s %-16s %-10s %-17s %s\n", o.Slice, who,
			PriceLabel(o.Terms.Tier, o.Terms.MonthlyCostCents), o.Status,
			o.ExpiresAt.Local().Format("2 Jan 2006 15:04"), o.ID)
	}
}

func fetchTransferOffers() ([]transferOffer, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/transfer", nil)
	if err != nil {
		return nil, common.TransportError("list transfer offers", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list transfer offers")
	if err != nil {
		return nil, err
	}
	var out struct {
		Offers []transferOffer `json:"offers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("Couldn't list transfer offers: the API response didn't look right (%w)", err) //nolint:staticcheck // ST1005: user-facing copy.
	}
	return out.Offers, nil
}

// findOffer resolves what the user typed — an offer id, or the name of a slice
// with one open offer in the given direction — to that offer.
func findOffer(arg, direction string) (*transferOffer, error) {
	offers, err := fetchTransferOffers()
	if err != nil {
		return nil, err
	}
	return resolveOffer(offers, arg, direction)
}

// resolveOffer is findOffer's decision, on offers already fetched.
//
// A NAME resolves only among OPEN offers in the caller's direction, and only
// when exactly one matches: two accounts can each offer you a slice called
// `shop`, and guessing between them would accept the wrong one. An id is exact.
func resolveOffer(offers []transferOffer, arg, direction string) (*transferOffer, error) {
	arg = strings.TrimSpace(arg)
	if transferIDPattern.MatchString(arg) {
		for i := range offers {
			if offers[i].ID == arg && offers[i].Direction == direction {
				return &offers[i], nil
			}
		}
		return nil, fmt.Errorf("no transfer offer %s", arg)
	}
	var matches []transferOffer
	for _, o := range offers {
		if o.Slice == arg && o.Direction == direction && o.Status == "pending" {
			matches = append(matches, o)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no open transfer offer for a slice named %s; see `drift slice transfer list`", arg)
	case 1:
		return &matches[0], nil
	}
	return nil, fmt.Errorf("%d open offers for a slice named %s; name one by its id — see `drift slice transfer list`",
		len(matches), arg)
}

// ─── accept ─────────────────────────────────────────────────────────────────

func getTransferAcceptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept <slice|offer-id>",
		Short: "Accept a slice offered to you",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := findOffer(args[0], "incoming")
			if err != nil {
				return err
			}
			printOfferTerms(o)
			answer := strings.ToLower(strings.TrimSpace(common.PromptForInput("Accept it? [y/N]")))
			if answer != "y" && answer != "yes" {
				fmt.Println("Nothing was accepted.")
				return nil
			}

			// The price shown above, sent back: the platform refuses an accept
			// that does not acknowledge what the slice will cost this account.
			body, _ := json.Marshal(map[string]any{
				"id":                        o.ID,
				"acknowledge_monthly_cents": o.Terms.MonthlyCostCents,
			})
			respBody, err := transferRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/transfer/accept",
				body, "accept slice", true)
			if err != nil {
				return err
			}
			var out struct {
				Note string `json:"note"`
			}
			_ = json.Unmarshal(respBody, &out)
			fmt.Printf("%s Slice %s is yours.\n", common.Check(), o.Slice)
			if o.PreviousHost != "" {
				fmt.Printf("  It now answers at https://%s.\n", o.Host)
				fmt.Printf("  The old address, https://%s, redirects there for %s\n", o.PreviousHost, redirectWindow(o.RedirectDays))
				fmt.Println("  and then stops answering.")
			} else {
				fmt.Printf("  It answers at https://%s.\n", o.Host)
			}
			fmt.Printf("  Act on it with DRIFT_SLICE=%s, or from a project whose Driftfile names it.\n", o.Slice)
			if out.Note != "" {
				fmt.Println()
				fmt.Println(common.Highlight("  " + out.Note))
			}
			return nil
		},
	}
}

// printOfferTerms shows the recipient what they are accepting: the slice, who
// it is from, what it is, what it costs, how long it is paid for, and the
// address it takes and the one it leaves.
func printOfferTerms(o *transferOffer) {
	fmt.Printf("\n%s offers you slice '%s'.\n\n", o.From, o.Slice)
	fmt.Printf("  tier          %s\n", TierLabel(o.Terms.Tier))
	fmt.Printf("  price         %s\n", PriceLabel(o.Terms.Tier, o.Terms.MonthlyCostCents))
	if o.Terms.BillingPeriodMonths > 0 && o.Terms.Tier != "hacker" {
		fmt.Printf("  billed every  %d month(s)\n", o.Terms.BillingPeriodMonths)
	}
	if o.Terms.PaidUntil != nil {
		fmt.Printf("  paid until    %s\n", o.Terms.PaidUntil.Local().Format("2 Jan 2006"))
	}
	if shape := shapeSummary(o.Terms.Config); shape != "" {
		fmt.Printf("  shape         %s\n", shape)
	}
	fmt.Printf("  address       https://%s\n", o.Host)
	if o.PreviousHost != "" {
		fmt.Printf("  old address   https://%s\n", o.PreviousHost)
	}
	fmt.Printf("  offer expires %s\n\n", o.ExpiresAt.Local().Format("2 Jan 2006 15:04"))
	fmt.Println("Accepting moves the slice, everything in it and its bill to your account.")
	if o.PreviousHost != "" {
		fmt.Printf("Its address becomes the one above, named after your account. The old address\n"+
			"redirects to it for %s and then stops answering.\n", redirectWindow(o.RedirectDays))
	}
	fmt.Println("The previous owner knows its secrets, its function API keys and its site")
	fmt.Println("password; rotate the ones you rely on once it is yours.")
	fmt.Println()
}

// shapeSummary renders the declared shape in one line — what runs on the slice
// and what it stores — from the config the platform sent. Only the counts and
// the names; the full config is the platform's to state.
func shapeSummary(raw json.RawMessage) string {
	var cfg struct {
		Atomic struct {
			Functions []json.RawMessage `json:"functions"`
		} `json:"atomic"`
		Backbone struct {
			NoSQL struct {
				Collections map[string]int `json:"collections"`
			} `json:"nosql"`
			Blobs struct {
				Buckets map[string]int `json:"buckets"`
			} `json:"blobs"`
			SQL struct {
				Databases map[string]int `json:"databases"`
			} `json:"sql"`
			Queues struct {
				Queues map[string]int `json:"queues"`
			} `json:"queues"`
		} `json:"backbone"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	parts := []string{fmt.Sprintf("%d function(s)", len(cfg.Atomic.Functions))}
	for _, set := range []struct {
		label string
		names map[string]int
	}{
		{"collection", cfg.Backbone.NoSQL.Collections},
		{"bucket", cfg.Backbone.Blobs.Buckets},
		{"database", cfg.Backbone.SQL.Databases},
		{"queue", cfg.Backbone.Queues.Queues},
	} {
		if len(set.names) > 0 {
			parts = append(parts, fmt.Sprintf("%d %s(s)", len(set.names), set.label))
		}
	}
	return strings.Join(parts, " · ")
}

// ─── decline and cancel ─────────────────────────────────────────────────────

func getTransferDeclineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "decline <slice|offer-id>",
		Short: "Decline a slice offered to you",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := findOffer(args[0], "incoming")
			if err != nil {
				return err
			}
			answer := strings.ToLower(strings.TrimSpace(common.PromptForInput(
				fmt.Sprintf("Decline %s's offer of slice %s? [y/N]", o.From, o.Slice))))
			if answer != "y" && answer != "yes" {
				fmt.Println("Nothing was declined.")
				return nil
			}
			body, _ := json.Marshal(map[string]string{"id": o.ID})
			if _, err := transferRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/transfer/decline",
				body, "decline offer", false); err != nil {
				return err
			}
			fmt.Printf("Declined. %s keeps slice %s.\n", o.From, o.Slice)
			return nil
		},
	}
}

func getTransferCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <slice|offer-id>",
		Short: "Withdraw an offer you made",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := findOffer(args[0], "outgoing")
			if err != nil {
				return err
			}
			if _, err := transferRequest(http.MethodDelete,
				common.APIBaseURL+"/ops/slice/transfer?id="+url.QueryEscape(o.ID), nil, "cancel offer", false); err != nil {
				return err
			}
			fmt.Printf("Offer withdrawn. Slice %s stays yours.\n", o.Slice)
			return nil
		},
	}
}

// ─── the request ────────────────────────────────────────────────────────────

// transferRequest sends one transfer request and returns the body of a 2xx.
//
// When stepUp is set and the platform answers that this act needs a fresh proof
// of identity, it asks for the password once, mints a grant and sends the
// request again carrying it. The grant is never minted up front: it is spent by
// the request that carries it, so minting one that was not needed is a password
// prompt for nothing.
func transferRequest(method, target string, body []byte, op string, stepUp bool) ([]byte, error) {
	send := func(headers map[string]string) (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			reader = strings.NewReader(string(body))
			headers["Content-Type"] = "application/json"
		}
		return common.DoRequestWithHeaders(method, target, reader, headers)
	}

	resp, err := send(map[string]string{})
	if err != nil {
		return nil, common.TransportError(op, err)
	}
	if stepUp && resp.StatusCode == http.StatusUnauthorized {
		raw := peek(resp)
		if purpose := needsStepUp(resp.StatusCode, raw); purpose != "" {
			_ = resp.Body.Close()
			grant, gerr := mintStepUpGrant(purpose, transferStepUpWhy)
			if gerr != nil {
				return nil, gerr
			}
			resp, err = send(map[string]string{stepUpHeader: grant})
			if err != nil {
				return nil, common.TransportError(op, err)
			}
		} else {
			// Not a step-up refusal: hand the body back for the error rendering,
			// which reads it to say what was wrong.
			resp.Body = io.NopCloser(bytes.NewReader(raw))
		}
	}
	defer resp.Body.Close()
	return common.CheckResponse(resp, op)
}
