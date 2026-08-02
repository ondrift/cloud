// reset.go — `drift account reset-password`. Two-step, guided password
// reset, same shape as `drift account create`'s signup flow:
//
//  1. Initiate — POST /reset/initiate {username}. The server always
//     returns success (anti-enumeration), whether or not the account
//     exists — so the CLI can't distinguish "code sent" from "no such
//     user" here. That's intentional; if nothing arrives, try the
//     username again.
//  2. Verify — user enters the code emailed to them plus a new
//     password; CLI POSTs /reset/verify {username, code, new_password}.
//     A successful reset revokes every existing session for the account
//     server-side and issues no new one, so the CLI immediately logs
//     back in with the new password to leave you signed in.
package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func GetResetPasswordCmd() *cobra.Command {
	var username string

	resetCmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset a forgotten password via an emailed code",
		Example: `  drift account reset-password
  drift account reset-password --username alice`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if username == "" {
				username = common.PromptForInput("Username")
			}

			fmt.Println("\nSending reset code...")
			initiatePayload, _ := json.Marshal(map[string]string{"username": username})

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Post(common.APIBaseURL+"/reset/initiate", "application/json", bytes.NewBuffer(initiatePayload))
			if err != nil {
				return common.TransportError("request a password reset", err)
			}
			_, err = common.CheckResponse(resp, "request a password reset")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				return err
			}

			fmt.Println("If that account exists, a reset code has been emailed to it.")
			code := common.PromptForInput("Reset code")

			newPassword := common.PromptForInputHidden("New password")
			repeatPassword := common.PromptForInputHidden("Repeat new password")
			if newPassword != repeatPassword {
				return errors.New("those passwords don't match — nothing was changed")
			}
			if len(newPassword) < 8 {
				return errors.New("password must be at least 8 characters — nothing was changed")
			}

			verifyPayload, _ := json.Marshal(map[string]string{
				"username":     username,
				"code":         code,
				"new_password": newPassword,
			})
			resp, err = client.Post(common.APIBaseURL+"/reset/verify", "application/json", bytes.NewBuffer(verifyPayload))
			if err != nil {
				return common.TransportError("verify the reset code", err)
			}
			_, err = common.CheckResponse(resp, "verify the reset code")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				return err
			}

			fmt.Println("Password reset. Every existing session for this account has been signed out.")

			// The reset revokes every refresh token server-side and issues no
			// new one — log back in immediately so this doesn't leave you
			// signed out of your own CLI session.
			// The reset itself succeeded; a failure to log back in is still a
			// failure of this command, because it leaves the user signed out
			// (#CLI-STANDARDUSAGE-3F5TDV).
			return DoLogin(username, newPassword)
		},
	}

	resetCmd.Flags().StringVarP(&username, "username", "u", "", "Username (skips interactive prompt)")
	return resetCmd
}
