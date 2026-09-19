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
			// The same rule signup applies, from the same place. "nothing was
			// changed" matters here in a way it does not at signup: the user has
			// already typed a code out of their mailbox, and needs to know the
			// code is still good.
			if err := common.ValidatePassword(newPassword); err != nil {
				return fmt.Errorf("%v Nothing was changed", err)
			}

			verifyFields := map[string]string{
				"username":     username,
				"code":         code,
				"new_password": newPassword,
			}
			body, verr := postResetVerify(client, verifyFields)
			if verr != nil {
				return verr
			}

			// The account has a confirmed second factor: the reset code alone
			// is only the first factor (proof of the inbox), so the platform
			// asks for the phone or a recovery code before it will touch the
			// password — otherwise MFA buys nothing against a reset. This is
			// the SAME round trip, not a new one: the pending reset row is
			// untouched, so retrying with a factor added replays this exact
			// code rather than needing a fresh email.
			if resetNeedsMFA(body) {
				mfaCode, isRecovery, ferr := PromptForFactor()
				if ferr != nil {
					return ferr
				}
				if isRecovery {
					verifyFields["recovery_code"] = mfaCode
				} else {
					verifyFields["mfa_code"] = mfaCode
				}
				if _, verr := postResetVerify(client, verifyFields); verr != nil {
					return verr
				}
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

// postResetVerify sends one /reset/verify attempt and returns the raw
// response body on success, so the caller can look for mfa_required before
// deciding the round trip is actually done.
func postResetVerify(client *http.Client, fields map[string]string) ([]byte, error) {
	payload, _ := json.Marshal(fields)
	resp, err := client.Post(common.APIBaseURL+"/reset/verify", "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return nil, common.TransportError("verify the reset code", err)
	}
	body, err := common.CheckResponse(resp, "verify the reset code")
	resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	return body, err
}

// resetNeedsMFA reports whether a successful /reset/verify reply is actually
// the mfa_required signal rather than a completed reset — the same shape
// /login answers with for a confirmed second factor.
func resetNeedsMFA(body []byte) bool {
	var out struct {
		MFARequired bool `json:"mfa_required"`
	}
	_ = json.Unmarshal(body, &out)
	return out.MFARequired
}
