// auth.go — `drift slice auth {list, disable}`, the Basic-auth gate in front of
// a slice's Canvas site.
//
// The gate is switched ON by declaring `site_auth` in the Driftfile's edge block
// and running `drift apply`, which sets exactly the declared users and realm.
// There is no command that sets credentials. `list` reads the gate and `disable`
// removes it, because no Driftfile declaration turns the gate off: absent, the
// key leaves the gate as it is.
//
// The gate covers the SITE, not the API: it is the "this staging site is not
// for the public yet" wall, not an authorization model for Atomic functions.
// `drift atomic auth` is the separate thing that guards those.
package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// siteAuthUser is one credential as the platform reports it. The response never
// carries a digest back, so only the name is read.
type siteAuthUser struct {
	Name string `json:"name"`
}

type siteAuthConfig struct {
	Enabled bool           `json:"enabled"`
	Realm   string         `json:"realm,omitempty"`
	Paths   []string       `json:"paths,omitempty"`
	Users   []siteAuthUser `json:"users,omitempty"`
}

func getAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Show or remove the username and password in front of the slice's site",
		Long: "Inspect or remove the HTTP Basic auth gate on the slice's Canvas site.\n\n" +
			"The gate is declared as `site_auth` in the Driftfile's edge block and set by\n" +
			"`drift apply`. Visitors get a browser password box; anyone without the\n" +
			"credential gets a 401 and never reaches the site. This gates the SITE —\n" +
			"Atomic functions are guarded separately with `drift atomic auth`.",
		Example: "  drift slice auth list\n" +
			"  drift slice auth disable",
	}
	cmd.AddCommand(getAuthListCmd(), getAuthDisableCmd())
	return cmd
}

func getAuthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "Show whether the gate is on, and who can sign in",
		Example: "  drift slice auth list",
		Args:    cobra.NoArgs,
		// RunE, not Run with os.Exit: os.Exit inside a handler skips every
		// deferred call, including the response body's Close. The handler
		// returns, main() prints once to stderr and exits 1.
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
			fmt.Printf("  Covers: %s\n", describeGatePaths(out.Paths))
			for _, u := range out.Users {
				fmt.Printf("  %s\n", u.Name)
			}
			return nil
		},
	}
}

// describeGatePaths says what a gate covers. A gate that reports no paths was set
// before paths existed and covers the whole site.
func describeGatePaths(paths []string) string {
	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "*") {
		return "the whole site"
	}
	return strings.Join(paths, ", ")
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
		// RunE — see the note on `list` above. The cancellation stays `nil`: a
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
