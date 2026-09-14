// auth.go — the Basic-auth gate in front of a slice's Canvas site.
//
// The gate has worked end to end for a while — the api stores it, the operator
// owns the record, the edge carries it on every host lookup and the router
// enforces it — and there was no way to switch it on short of hand-rolling a
// curl against /ops/slice/auth. A feature nobody can find is indistinguishable
// from one that does not work, and this one was very nearly deleted on exactly
// that reading (#CLI-STANDARDUSAGE-P545K1).
//
// The gate covers the SITE, not the API: it is the "this staging site is not
// for the public yet" wall, not an authorization model for Atomic functions.
// `drift atomic auth` is the separate thing that guards those.
package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// siteAuthUser is one credential on the wire. The response never carries a
// digest back — the api marks it `json:"-"` — so Password is write-only here.
type siteAuthUser struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

type siteAuthConfig struct {
	Enabled bool           `json:"enabled"`
	Realm   string         `json:"realm,omitempty"`
	Users   []siteAuthUser `json:"users,omitempty"`
}

func getAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Put a username and password in front of the slice's site",
		Long: "Guard the slice's Canvas site with HTTP Basic auth.\n\n" +
			"Visitors get a browser password box; anyone without the credential gets a\n" +
			"401 and never reaches the site. This gates the SITE — Atomic functions are\n" +
			"guarded separately with `drift atomic auth`.",
		Example: "  drift slice auth set --user visitor\n" +
			"  drift slice auth list\n" +
			"  drift slice auth disable",
	}
	cmd.AddCommand(getAuthSetCmd(), getAuthListCmd(), getAuthDisableCmd())
	return cmd
}

func getAuthSetCmd() *cobra.Command {
	var (
		users         []string
		realm         string
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Enable the gate, replacing any credentials already set",
		Long: "Enable the Basic-auth gate on this slice's site.\n\n" +
			"This REPLACES the whole credential set rather than adding to it, so a\n" +
			"`set` naming one user leaves exactly that user able to get in. That is\n" +
			"what makes removing someone a single predictable command.\n\n" +
			"Passwords are read from the terminal, or one per --user in order from\n" +
			"stdin with --password-stdin. There is deliberately no --password flag:\n" +
			"it would put the credential in `ps` output and shell history.",
		Example: "  drift slice auth set --user visitor\n" +
			"  drift slice auth set --user alice --user bob --realm \"Staging\"\n" +
			"  printf 'hunter2\\n' | drift slice auth set --user visitor --password-stdin",
		Args: cobra.NoArgs,
		// RunE, not Run with os.Exit. The exit CODE was right here — unlike the
		// `Run` handlers elsewhere that printed and returned 0 — but two other
		// things were not: the message went to stdout rather than stderr, and
		// `os.Exit` inside the handler skips every deferred call, including the
		// `resp.Body.Close()` below it.
		//
		// Returning gives one convention across the whole CLI: the handler
		// returns, main() prints once to stderr and exits 1.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(users) == 0 {
				return fmt.Errorf("name at least one user with --user; the gate needs someone who can get in")
			}

			cfg := siteAuthConfig{Enabled: true, Realm: realm}
			for _, name := range users {
				var pw string
				if passwordStdin {
					pw = common.ReadPasswordFromStdin()
				} else {
					pw = common.PromptForInputHidden(fmt.Sprintf("Password for %s", name))
				}
				if pw == "" {
					return fmt.Errorf("no password given for %s", name)
				}
				cfg.Users = append(cfg.Users, siteAuthUser{Name: name, Password: pw})
			}

			body, err := json.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("failed to prepare the request: %w", err)
			}
			resp, err := common.DoRequestWithContentType(http.MethodPost,
				common.APIBaseURL+"/ops/slice/auth", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("Couldn't enable the site gate: %w", err) //nolint:staticcheck // ST1005: user-facing copy.
			}
			defer resp.Body.Close()

			raw, err := common.CheckResponse(resp, "enable the site gate")
			if err != nil {
				return err
			}
			var out siteAuthConfig
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("Couldn't enable the site gate: the platform sent a reply we couldn't read") //nolint:staticcheck // ST1005: user-facing copy.
			}

			names := make([]string, 0, len(out.Users))
			for _, u := range out.Users {
				names = append(names, u.Name)
			}
			fmt.Printf("Site gate enabled. %s can sign in.\n", strings.Join(names, ", "))
			if out.Realm != "" {
				fmt.Printf("  Realm: %s\n", out.Realm)
			}
			fmt.Println("  Visitors without the password now get a 401 instead of the site.")
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&users, "user", "u", nil,
		"Username allowed through the gate (repeat for more than one)")
	cmd.Flags().StringVar(&realm, "realm", "",
		"Realm shown in the browser's password box")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false,
		"Read passwords from stdin, one line per --user, in order (for CI)")
	return cmd
}

func getAuthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "Show whether the gate is on, and who can sign in",
		Example: "  drift slice auth list",
		Args:    cobra.NoArgs,
		// RunE — see the note on `set` above for why os.Exit is the wrong tool
		// inside a handler that has deferred work.
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				return fmt.Errorf("Couldn't read the site gate: %w", err) //nolint:staticcheck // ST1005: user-facing copy.
			}
			defer resp.Body.Close()

			raw, err := common.CheckResponse(resp, "read the site gate")
			if err != nil {
				return err
			}
			var out siteAuthConfig
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("Couldn't read the site gate: the platform sent a reply we couldn't read") //nolint:staticcheck // ST1005: user-facing copy.
			}

			if !out.Enabled {
				fmt.Println("No site gate. The site is public.")
				return nil
			}
			fmt.Println("Site gate is ON.")
			if out.Realm != "" {
				fmt.Printf("  Realm: %s\n", out.Realm)
			}
			for _, u := range out.Users {
				fmt.Printf("  %s\n", u.Name)
			}
			return nil
		},
	}
}

func getAuthDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove the gate and make the site public again",
		Long: "Remove the Basic-auth gate. The site becomes publicly reachable.\n\n" +
			"Confirmed, because it is the one direction that EXPOSES something: the\n" +
			"whole point of the gate is that the site is not ready to be seen.",
		Example: "  drift slice auth disable",
		Args:    cobra.NoArgs,
		// RunE — see the note on `set` above. The cancellation stays `nil`: a
		// person answering "no" chose the safe outcome, and exiting non-zero on it
		// would make that read as a failure.
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("This makes the site publicly reachable by anyone with the URL.")
			answer := strings.ToLower(strings.TrimSpace(
				common.PromptForInput("Remove the site gate? [y/N]"),
			))
			if answer != "y" && answer != "yes" {
				fmt.Println("Left as it was.")
				return nil
			}

			resp, err := common.DoRequest(http.MethodDelete,
				common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				return fmt.Errorf("Couldn't remove the site gate: %w", err) //nolint:staticcheck // ST1005: user-facing copy.
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "remove the site gate"); err != nil {
				return err
			}
			fmt.Println("Site gate removed. The site is public again.")
			return nil
		},
	}
}
