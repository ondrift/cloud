// token.go — `drift account token create|list|revoke`.
//
// A Drift session reaches every control-plane verb, so handing a CI pipeline the
// ability to deploy used to mean handing it the ability to read every secret and
// delete the account — and taking it back meant resetting the owner's password.
// These three mint, enumerate and revoke a credential that is narrower than the
// session and revocable on its own.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// mintableScopes is what `--scope` accepts, and the help text is generated from
// it so the two cannot disagree.
//
// account:write and account:delete are ABSENT, and the platform refuses them
// too. A token that could carry account:write could mint another token — undoing
// every limit set here — and one that could carry account:delete could end the
// account it was given to deploy into. Both are acts for a person in a session.
var mintableScopes = []struct{ name, does string }{
	{"slice:read", "see a slice's shape, status, functions, logs and history"},
	{"slice:write", "create, resize, deploy, roll back, delete — and write to Backbone"},
	{"secret:read", "read secret VALUES in plaintext, and list which exist"},
	{"secret:write", "set and delete secrets, without being able to read them"},
	{"account:read", "read the account's own audit trail and token list"},
}

func tokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint, list and revoke access tokens for CI and scripts",
		Long: `Access tokens for things that are not a person.

A token carries a SUBSET of what your session can do and is revoked on its own,
so a CI pipeline can deploy without also being able to read your secrets or
delete your account.

Use one by putting it in the environment — no login, and nothing written to disk:

    export DRIFT_TOKEN=drift_pat_...
    export DRIFT_SLICE=my-slice
    drift file apply`,
		Example: "  drift account token create ci --scope slice:read --scope slice:write\n" +
			"  drift account token list\n" +
			"  drift account token revoke ci",
	}
	cmd.AddCommand(tokenCreateCmd(), tokenListCmd(), tokenRevokeCmd())
	return cmd
}

// scopeHelp renders --scope's help from mintableScopes, so the list a user is
// offered and the list this command documents cannot disagree.
//
// No leading padding on the continuation lines: cobra already indents wrapped
// flag help to the description column, and adding our own on top of it pushes
// the table off the right of an eighty-column terminal.
func scopeHelp() string {
	var b strings.Builder
	b.WriteString("what the token may do (repeatable, at least one required):\n")
	for _, s := range mintableScopes {
		b.WriteString(fmt.Sprintf("%-13s %s\n", s.name, s.does))
	}
	return strings.TrimRight(b.String(), "\n")
}

func tokenCreateCmd() *cobra.Command {
	var scopes []string
	var ttlDays int
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Mint a token and print it once",
		Long: `Mint a token and print it once.

THE VALUE IS SHOWN ONCE AND IS NOT RECOVERABLE. Drift stores only a hash of it,
so there is no command that can show it again — copy it now, into wherever your
pipeline keeps secrets.

Grant the narrowest set that does the job. A deploy pipeline usually wants
slice:read and slice:write and nothing else: it does not need to read your
secrets to ship a function that uses them.`,
		Example: "  drift account token create ci --scope slice:read --scope slice:write\n" +
			"  drift account token create audit-bot --scope account:read --ttl 90",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(scopes) == 0 {
				e := fmt.Errorf("Couldn't create the token: at least one --scope is required.\n" +
					"Hint: a deploy pipeline usually wants --scope slice:read --scope slice:write.")
				fmt.Println(e)
				return e
			}

			payload, _ := json.Marshal(map[string]any{
				"name":     args[0],
				"scopes":   scopes,
				"ttl_days": ttlDays,
			})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/account/token", bytes.NewBuffer(payload))
			if err != nil {
				e := common.TransportError("create the token", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "create the token")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var out struct {
				Token     string     `json:"token"`
				Name      string     `json:"name"`
				Scopes    []string   `json:"scopes"`
				ExpiresAt *time.Time `json:"expires_at"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				e := fmt.Errorf("couldn't read the response: %w", err)
				fmt.Println(e)
				return e
			}

			fmt.Printf("%s Token %q created\n\n", common.Highlight("✓"), out.Name)
			fmt.Printf("  %s\n\n", out.Token)
			fmt.Printf("  This is the only time it will be shown — Drift stores only a hash.\n")
			fmt.Printf("  It can: %s\n", strings.Join(out.Scopes, ", "))
			if out.ExpiresAt != nil {
				fmt.Printf("  Expires: %s\n", out.ExpiresAt.Format(time.RFC1123))
			} else {
				fmt.Printf("  Expires: never — revoke it with `drift account token revoke %s`\n", out.Name)
			}
			fmt.Printf("\n  Use it:\n")
			fmt.Printf("    export %s=%s\n", common.TokenEnv, out.Token)
			fmt.Printf("    export %s=<your-slice>\n", common.SliceEnv)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&scopes, "scope", nil, scopeHelp())
	cmd.Flags().IntVar(&ttlDays, "ttl", 0, "days until it expires (default: never)")
	return cmd
}

func tokenListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List this account's tokens — never their values",
		Long: `List this account's tokens.

Revoked and expired ones are included. "Was there a credential that could do
this, and when did it stop?" is a question asked after something has gone wrong,
and a listing that hid the answer would be useless exactly when it matters.`,
		Example: "  drift account token list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/account/token", nil)
			if err != nil {
				e := common.TransportError("list tokens", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list tokens")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var rows []struct {
				Name       string     `json:"name"`
				Scopes     []string   `json:"scopes"`
				CreatedAt  time.Time  `json:"created_at"`
				ExpiresAt  *time.Time `json:"expires_at"`
				LastUsedAt *time.Time `json:"last_used_at"`
				Revoked    bool       `json:"revoked"`
				Live       bool       `json:"live"`
			}
			if err := json.Unmarshal(body, &rows); err != nil {
				e := fmt.Errorf("couldn't read the list response: %w", err)
				fmt.Println(e)
				return e
			}
			if len(rows) == 0 {
				fmt.Println("(no tokens)")
				fmt.Println("  Mint one with `drift account token create <name> --scope slice:read`")
				return nil
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })

			fmt.Printf("%s Access tokens\n\n", common.Highlight(""))
			for _, r := range rows {
				// REVOKED and EXPIRED are different words for different facts. One
				// was taken away and the other ran out, and a reader looking at a
				// credential that stopped working needs to know which.
				state := "active"
				switch {
				case r.Revoked:
					state = "REVOKED"
				case !r.Live:
					state = "EXPIRED"
				}
				fmt.Printf("  %-8s %s\n", state, r.Name)
				fmt.Printf("           can: %s\n", strings.Join(r.Scopes, ", "))
				used := "never used"
				if r.LastUsedAt != nil {
					used = "last used " + r.LastUsedAt.Format(time.RFC1123)
				}
				fmt.Printf("           created %s, %s\n", r.CreatedAt.Format(time.RFC1123), used)
				if r.ExpiresAt != nil {
					fmt.Printf("           expires %s\n", r.ExpiresAt.Format(time.RFC1123))
				}
			}
			fmt.Printf("\n%d token(s)\n", len(rows))
			return nil
		},
	}
}

func tokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "Stop a token working",
		Long: `Stop a token working.

Takes effect within about fifteen minutes for a caller mid-run: a token is
exchanged for a short-lived access token, and revoking cannot recall one already
issued. Anything that exchanges after this refuses immediately.

The record is kept rather than deleted, so the token still appears in
'drift account token list' marked REVOKED.`,
		Example: "  drift account token revoke ci",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := common.DoJSONRequest(http.MethodDelete,
				common.APIBaseURL+"/ops/account/token", bytes.NewBuffer(payload))
			if err != nil {
				e := common.TransportError("revoke the token", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "revoke the token"); err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Printf("Token %q revoked\n", args[0])
			fmt.Println("  An access token it already exchanged stays valid for up to 15 minutes.")
			return nil
		},
	}
}
