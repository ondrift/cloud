// member.go — `drift account member invite|list|remove`.
//
// The one non-owner role. A member is a second PERSON on your account: their own
// login, their own password, their own second factor, working on your slices.
//
// # What a member can and cannot do, in one place
//
// Can: create, resize, restart and delete slices; deploy, roll back and delete
// functions; read and write Backbone.
//
// Cannot: read or change your secrets, read your audit trail, change how your
// account is protected, mint an access token, invite anybody else, or delete the
// account.
//
// That list is not enforced here — it is the scope set their session carries,
// checked by the platform on every request — but it is what the commands below
// have to be honest about, because it is the only place a user reads it.
//
// # This is NOT `drift account token`
//
// A token is a credential for a THING: a CI job, a script, something that is not
// a person. It has no login, no password and no identity of its own, and it is
// revoked by name.
//
// A member is a PERSON. They log in as themselves, the audit trail records them
// by name rather than recording you, and removing them leaves their account
// intact and theirs. Reach for a token for a pipeline and a member for a
// colleague — using one for the other works and makes the trail useless.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

const memberEndpoint = "/ops/account/member"

func memberCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Invite a colleague onto this account, see who is on it, remove them",
		Long: `A member is a second person on your account.

They log in as themselves and work on your slices: deploy, roll back, create and
delete slices, read and write Backbone.

They cannot read or change your secrets, read your audit trail, change your
second factor, mint an access token, invite anyone else, or delete the account.

For a CI pipeline or a script, use ` + "`drift account token`" + ` instead — a member is a
person, and the audit trail is written on that assumption.`,
		Example: "  drift account member invite erica@example.com\n" +
			"  drift account member list\n" +
			"  drift account member remove erica",
	}
	cmd.AddCommand(memberInviteCmd(), memberListCmd(), memberRemoveCmd())
	return cmd
}

func memberInviteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "invite <email>",
		Short: "Invite someone onto this account",
		Long: `Invite someone onto this account.

Drift emails them an invite code. They create their own account against it —
their own username, their own password — and it lands attached to yours.

The code is tied to that email address and works once. It is printed here as
well as emailed, so you can pass it on yourself if the mail does not arrive.

You do not create their credentials and you never see them. That is deliberate:
it is what lets the audit trail say THEY deployed something rather than saying
you did.`,
		Example: "  drift account member invite erica@example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, _ := json.Marshal(map[string]string{"email": args[0]})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+memberEndpoint, bytes.NewBuffer(payload))
			if err != nil {
				e := common.TransportError("send the invite", err)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "send the invite")
			if err != nil {
				return err
			}

			// ExpiresIn is flexSeconds for the reason login.go gives at length:
			// the platform sends `expires_in` as seconds in a string, and a
			// client that falls over on the shape of a field it only prints is
			// a client that refuses a working reply.
			var out struct {
				Code      string      `json:"code"`
				Email     string      `json:"email"`
				Emailed   bool        `json:"emailed"`
				ExpiresIn flexSeconds `json:"expires_in"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				e := fmt.Errorf("couldn't read the response: %w", err)
				return e
			}

			fmt.Printf("%s Invited %s\n\n", common.Highlight("✓"), out.Email)
			fmt.Printf("  %s\n\n", out.Code)
			if out.Emailed {
				fmt.Printf("  Emailed to %s. The code above is the same one, in case it does not arrive.\n", out.Email)
			} else {
				// SAYING SO IS THE POINT. The invite is stored either way, so a
				// silent mail failure would leave a live code nobody can see.
				fmt.Printf("  The email could NOT be sent — pass the code above on yourself.\n")
			}
			if out.ExpiresIn > 0 {
				fmt.Printf("  Valid for %d days, for that address, once.\n", out.ExpiresIn/86400)
			}
			fmt.Printf("\n  They sign up with:\n")
			fmt.Printf("    drift account create -e %s --invite-code %s\n", out.Email, out.Code)
			fmt.Printf("\n  Withdraw it with `drift account member remove --email %s`\n", out.Email)
			return nil
		},
	}
}

func memberListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Who is on this account, and who has been asked",
		Long: `Who is on this account.

Pending invites are listed too. An unspent code is a person who can still join,
so leaving it out would make this list answer the wrong question.

Invite codes are NOT shown. You saw each one when you minted it; re-printing
them here would mean a read-only listing hands out live credentials.`,
		Example: "  drift account member list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+memberEndpoint, nil)
			if err != nil {
				e := common.TransportError("list members", err)
				return e
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list members")
			if err != nil {
				return err
			}

			var out struct {
				Members []struct {
					Username  string    `json:"username"`
					Email     string    `json:"email"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"members"`
				Pending []struct {
					Email     string    `json:"email"`
					CreatedAt time.Time `json:"created_at"`
					ExpiresAt time.Time `json:"expires_at"`
				} `json:"pending"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				e := fmt.Errorf("couldn't read the list response: %w", err)
				return e
			}

			if len(out.Members) == 0 && len(out.Pending) == 0 {
				fmt.Println("(no members)")
				fmt.Println("  Invite someone with `drift account member invite <email>`")
				return nil
			}

			sort.Slice(out.Members, func(i, j int) bool {
				return out.Members[i].CreatedAt.Before(out.Members[j].CreatedAt)
			})
			sort.Slice(out.Pending, func(i, j int) bool {
				return out.Pending[i].CreatedAt.Before(out.Pending[j].CreatedAt)
			})

			fmt.Printf("%s Members\n\n", common.Highlight(""))
			for _, m := range out.Members {
				fmt.Printf("  %-10s %s\n", "member", m.Username)
				fmt.Printf("             %s, joined %s\n", m.Email, m.CreatedAt.Format(time.RFC1123))
			}
			for _, p := range out.Pending {
				fmt.Printf("  %-10s %s\n", "INVITED", p.Email)
				fmt.Printf("             invited %s, expires %s\n",
					p.CreatedAt.Format(time.RFC1123), p.ExpiresAt.Format(time.RFC1123))
			}

			fmt.Printf("\n%d member(s), %d pending invite(s)\n", len(out.Members), len(out.Pending))
			fmt.Println("  Members can deploy and use Backbone. They cannot read your secrets,")
			fmt.Println("  read your audit trail, invite anyone, or delete the account.")
			return nil
		},
	}
}

func memberRemoveCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "remove [username]",
		Short: "Take someone off this account, or withdraw an invite",
		Long: `Take someone off this account.

Give a username to remove a member, or --email to withdraw an invite nobody has
redeemed yet.

REMOVING A MEMBER DOES NOT DELETE THEIR ACCOUNT. Their login, their password and
their second factor are theirs and stay theirs — they simply stop having access
to yours. Everything they deployed belongs to your account and is untouched.

Access ends within about fifteen minutes: their current session runs out and the
next renewal is refused.`,
		Example: "  drift account member remove erica\n" +
			"  drift account member remove --email erica@example.com",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// EXACTLY ONE identifier, refused here as well as by the platform. The
			// two name different things, and a command that quietly preferred one
			// would remove the wrong one the first time somebody's username looked
			// like an email address.
			if (len(args) == 0) == (email == "") {
				e := fmt.Errorf("Couldn't remove: give either a username or --email, not both.\n" +
					"Hint: `drift account member remove erica` removes a member;\n" +
					"      `drift account member remove --email erica@example.com` withdraws an invite.")
				return e
			}

			q := url.Values{}
			what := ""
			if email != "" {
				q.Set("email", email)
				what = "invite for " + email
			} else {
				q.Set("username", args[0])
				what = args[0]
			}

			resp, err := common.DoRequest(http.MethodDelete,
				common.APIBaseURL+memberEndpoint+"?"+q.Encode(), nil)
			if err != nil {
				e := common.TransportError("remove the member", err)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "remove the member"); err != nil {
				return err
			}

			if email != "" {
				fmt.Printf("Withdrew the %s\n", what)
				fmt.Println("  The code stops working immediately.")
				return nil
			}
			fmt.Printf("Removed %s from this account\n", what)
			fmt.Println("  Their own account is untouched — only their access to yours ended.")
			fmt.Println("  A session they already hold runs out within about 15 minutes.")
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "",
		"withdraw a pending invite for this address, instead of removing a member")
	return cmd
}
