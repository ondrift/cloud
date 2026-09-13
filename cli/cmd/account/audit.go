package account

// audit.go — `drift account audit`: what the platform recorded about your
// account.
//
// Drift keeps a hash-chained, anchored, two-year audit trail of logins, deploys,
// secret reads, snapshot downloads and domain changes — and the security page
// tells customers so, including that deleting the account does not remove it.
// Until this command there was no way for the person it is about to read a
// single line of it.
//
// It shows events you PERFORMED. Actions the platform took on its own — an
// operator tearing a slice down, say — carry that operator as their actor and do
// not appear; the server scopes every query to the authenticated username and
// ignores any actor the client asks for, which is what stops one tenant reading
// another's trail.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// auditEvent is the subset of a record worth showing. The store returns more —
// request id, user agent, details — and `--json` is how anyone who wants those
// gets them, unshaped by this binary's idea of what matters.
type auditEvent struct {
	TS time.Time `json:"ts"`
	// `action`, not `event`. The record has been stored under that name since it
	// was written — `bson:"action" json:"action"` — and reading `event` here
	// produced a table with an empty EVENT column: every row present, correctly
	// timestamped, and silent about what had happened. A field name that does not
	// match decodes to the zero value rather than failing, which is why this
	// survived a passing unit test and was found by running it.
	Event    string `json:"action"`
	Outcome  string `json:"outcome"`
	Target   string `json:"target"`
	Reason   string `json:"reason"`
	SourceIP string `json:"source_ip"`
}

func GetAuditCmd() *cobra.Command {
	var (
		event  string
		since  string
		until  string
		limit  int
		cursor string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show what the platform recorded about your account",
		Long: "Reads your own audit trail: logins, password resets, deploys, secret reads,\n" +
			"snapshot downloads and domain changes, newest first.\n\n" +
			"The trail is hash-chained and kept for two years, and it survives account\n" +
			"deletion by design — the record that an account existed is exactly what must\n" +
			"outlive it.\n\n" +
			"Only YOUR events: the server scopes every query to you and ignores any actor\n" +
			"a client asks for.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: "  drift account audit\n" +
			"  drift account audit --event login.failed\n" +
			"  drift account audit --limit 100 --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if event != "" {
				q.Set("event", event)
			}
			for _, b := range []struct{ name, raw string }{{"since", since}, {"until", until}} {
				if b.raw == "" {
					continue
				}
				ts, terr := parseWhen(b.raw)
				if terr != nil {
					return fmt.Errorf("--%s: %w", b.name, terr)
				}
				q.Set(b.name, ts.UTC().Format(time.RFC3339))
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}

			target := common.APIBaseURL + "/ops/account/audit"
			if encoded := q.Encode(); encoded != "" {
				target += "?" + encoded
			}

			resp, err := common.DoRequest("GET", target, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "read your audit trail")
			if err != nil {
				return err
			}

			if asJSON {
				// The server's bytes, unshaped. Anything this command chooses not
				// to render is still in here, and a caller piping to jq should not
				// be reading a re-encoded subset.
				fmt.Println(strings.TrimSpace(string(body)))
				return nil
			}

			var page struct {
				Events []auditEvent `json:"events"`
				Cursor string       `json:"cursor"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return fmt.Errorf("the platform returned an audit page this version cannot read: %w", err)
			}

			if len(page.Events) == 0 {
				// NOT "nothing happened". A filter that matches nothing and an
				// empty trail are different facts, and only one of them is about
				// the account.
				if event != "" {
					fmt.Printf("No %s events recorded for your account.\n", event)
				} else {
					fmt.Println("No events recorded for your account yet.")
				}
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WHEN\tEVENT\tOUTCOME\tTARGET\tFROM")
			for _, e := range page.Events {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					e.TS.Local().Format("2006-01-02 15:04:05"),
					e.Event,
					outcomeLabel(e.Outcome, e.Reason),
					dashIfEmpty(e.Target),
					dashIfEmpty(e.SourceIP),
				)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			// The cursor is the only way to the next page, so it is printed as the
			// command that uses it rather than as an opaque token to copy.
			if page.Cursor != "" {
				fmt.Printf("\n%s\n", common.Hint("more: drift account audit --cursor "+page.Cursor))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "only this event type (e.g. login, secret.read, atomic.deploy)")
	cmd.Flags().StringVar(&since, "since", "", "only events at or after this point: 7d, 24h, or YYYY-MM-DD")
	cmd.Flags().StringVar(&until, "until", "", "only events at or before this point: same forms as --since")
	cmd.Flags().IntVar(&limit, "limit", 0, "events per page (default 50, max 200)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue from a previous page")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the platform's reply verbatim")
	return cmd
}

// parseWhen accepts the three forms a person actually types for a point in time.
//
// A DURATION IS AN AGO, not a length: `--since 7d` means "seven days back from
// now", which is what someone asking about last week means. Go's own
// ParseDuration has no day unit — `7d` is an error there — and days are the unit
// this question is nearly always asked in, so that form is handled before the
// rest is handed over.
//
// A BARE DATE MEANS THE START OF THAT DAY IN THE LOCAL ZONE, because someone
// typing a calendar date means their own day rather than UTC's. The wire format
// is UTC and the conversion happens at the call site.
func parseWhen(raw string) (time.Time, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-d), nil
	}
	if strings.HasSuffix(raw, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(raw, "d")); err == nil {
			return time.Now().AddDate(0, 0, -days), nil
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if ts, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time I can read — try 7d, 24h, a YYYY-MM-DD date, or a full RFC 3339 timestamp", raw)
}

// outcomeLabel keeps the reason attached to the outcome it explains. A bare
// "failure" tells the reader that something did not work and not what, and the
// reason is usually one word — unknown_user, bad_password — that answers it.
func outcomeLabel(outcome, reason string) string {
	if reason == "" {
		return dashIfEmpty(outcome)
	}
	return outcome + " (" + reason + ")"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
