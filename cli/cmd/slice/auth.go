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
	"os"
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
		Run: func(cmd *cobra.Command, args []string) {
			if len(users) == 0 {
				fmt.Println("Name at least one user with --user; the gate needs someone who can get in.")
				os.Exit(1)
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
					fmt.Printf("No password given for %s.\n", name)
					os.Exit(1)
				}
				cfg.Users = append(cfg.Users, siteAuthUser{Name: name, Password: pw})
			}

			body, err := json.Marshal(cfg)
			if err != nil {
				fmt.Println("Failed to prepare the request:", err)
				os.Exit(1)
			}
			resp, err := common.DoRequestWithContentType(http.MethodPost,
				common.APIBaseURL+"/ops/slice/auth", "application/json", bytes.NewReader(body))
			if err != nil {
				fmt.Printf("Couldn't enable the site gate: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			raw, err := common.CheckResponse(resp, "enable the site gate")
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			var out siteAuthConfig
			if err := json.Unmarshal(raw, &out); err != nil {
				fmt.Println("Couldn't enable the site gate: the platform sent a reply we couldn't read.")
				os.Exit(1)
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
		Run: func(cmd *cobra.Command, args []string) {
			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				fmt.Printf("Couldn't read the site gate: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			raw, err := common.CheckResponse(resp, "read the site gate")
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			var out siteAuthConfig
			if err := json.Unmarshal(raw, &out); err != nil {
				fmt.Println("Couldn't read the site gate: the platform sent a reply we couldn't read.")
				os.Exit(1)
			}

			if !out.Enabled {
				fmt.Println("No site gate. The site is public.")
				return
			}
			fmt.Println("Site gate is ON.")
			if out.Realm != "" {
				fmt.Printf("  Realm: %s\n", out.Realm)
			}
			for _, u := range out.Users {
				fmt.Printf("  %s\n", u.Name)
			}
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
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("This makes the site publicly reachable by anyone with the URL.")
			answer := strings.ToLower(strings.TrimSpace(
				common.PromptForInput("Remove the site gate? [y/N]"),
			))
			if answer != "y" && answer != "yes" {
				fmt.Println("Left as it was.")
				return
			}

			resp, err := common.DoRequest(http.MethodDelete,
				common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				fmt.Printf("Couldn't remove the site gate: %v\n", err)
				os.Exit(1)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "remove the site gate"); err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			fmt.Println("Site gate removed. The site is public again.")
		},
	}
}
