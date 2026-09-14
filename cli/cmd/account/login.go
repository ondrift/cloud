// login.go — `drift account login`. POSTs `{username, password,
// device_id}` to /login; on success, persists the JWT pair to
// `~/.drift/session.json` (mode 0600). The device_id is a
// stable per-workstation random ID (see common/session.go ::
// GetOrCreateDeviceID) — refresh tokens are bound to it, so a
// stolen session.json without the matching device_id can't refresh.
//
// Three ways to supply the password:
//
//   - Interactive (default) — `drift account login` prompts for it
//     with terminal echo disabled.
//   - --password-stdin       — `echo $PASS | drift account login -u alice
//     --password-stdin`. The password never appears as a process
//     argument; doesn't show up in `ps`, shell history, or
//     process-listing logs. The pattern gh / docker login / kubectl /
//     doctl / op all use. Recommended for CI.
//   - --password (-p)        — kept for backward compatibility but
//     prints a deprecation warning. The password ends up in shell
//     history and `ps` output. Migrate to --password-stdin.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// DoLoginErr performs the login POST and persists the session, returning an
// error instead of printing one. Lets callers outside the CLI-command UX
// (the portal's pre-launch login window) react to a failed attempt
// themselves — DoLogin below is just this plus the command's print-and-return
// behavior.
//
// An account with a second factor does not get a session from this call alone:
// the platform answers `mfa_required` with a short-lived handle, and the code is
// supplied separately. See DoLoginWithFactor, which is what the command uses.
func DoLoginErr(username, password string) error {
	return DoLoginWithFactor(username, password, nil)
}

// loginReply is what /login answers with. The two shapes are exclusive: either
// the token pair, or a challenge and no tokens at all.
type loginReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`

	MFARequired bool   `json:"mfa_required"`
	MFAToken    string `json:"mfa_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// factorSupplier is asked for a second factor when the platform demands one. It
// returns the code and whether it is a recovery code.
//
// A FUNCTION RATHER THAN A STRING, because the caller decides where a code comes
// from and the two answers are genuinely different: an interactive session
// prompts a person, and CI passes `--mfa-code` or has nothing to give. A nil
// supplier means nothing to give — the login then fails with a message naming
// the flag, rather than hanging on a prompt no one is watching.
type factorSupplier func() (code string, isRecovery bool, err error)

// DoLoginWithFactor logs in, completing the second leg when one is demanded.
func DoLoginWithFactor(username, password string, factor factorSupplier) error {
	jsonData, _ := json.Marshal(map[string]string{
		"username":  username,
		"password":  password,
		"device_id": common.GetOrCreateDeviceID(),
	})

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Post(common.APIBaseURL+"/login", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return common.TransportError("log in", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "log in")
	if err != nil {
		return err
	}

	var reply loginReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("couldn't log in: the API response didn't look right — %w", err)
	}

	if reply.MFARequired {
		if factor == nil {
			return fmt.Errorf("this account has a second factor. Pass a code with --mfa-code, " +
				"or run `drift account login` without --password-stdin to be prompted for one")
		}
		return completeLoginWithFactor(client, reply.MFAToken, factor)
	}

	if reply.AccessToken == "" || reply.RefreshToken == "" {
		return fmt.Errorf("couldn't log in: the API didn't return a full set of tokens. That's on us; please try again")
	}

	if err := common.SaveSession(reply.AccessToken, reply.RefreshToken); err != nil {
		return fmt.Errorf("logged in, but couldn't save your session to disk: %w", err)
	}
	return nil
}

// completeLoginWithFactor exchanges the challenge handle and a code for the
// session.
//
// The handle is NOT saved anywhere. It is a credential for five minutes, and
// writing it to disk beside the session would leave a file that completes a
// login without the password — the thing the second factor exists to prevent.
func completeLoginWithFactor(client *http.Client, mfaToken string, factor factorSupplier) error {
	code, isRecovery, err := factor()
	if err != nil {
		return err
	}
	if code == "" {
		return fmt.Errorf("no code supplied — this account needs a second factor to log in")
	}

	payload := map[string]string{"mfa_token": mfaToken}
	if isRecovery {
		payload["recovery_code"] = code
	} else {
		payload["code"] = code
	}
	jsonData, _ := json.Marshal(payload)

	resp, err := client.Post(common.APIBaseURL+"/login/mfa", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return common.TransportError("complete the login", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "complete the login")
	if err != nil {
		return err
	}

	var reply loginReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return fmt.Errorf("couldn't log in: the API response didn't look right — %w", err)
	}
	if reply.AccessToken == "" || reply.RefreshToken == "" {
		return fmt.Errorf("couldn't log in: the API didn't return a full set of tokens. That's on us; please try again")
	}

	if err := common.SaveSession(reply.AccessToken, reply.RefreshToken); err != nil {
		return fmt.Errorf("logged in, but couldn't save your session to disk: %w", err)
	}
	return nil
}

// DoLogin logs in and reports the outcome, returning the error so the command
// EXITS NON-ZERO on failure.
//
// It used to print the error and return nothing, on a cobra `Run:` (which has no
// error channel at all), so a rejected login printed "Couldn't log in: invalid
// username or password." and exited 0 (#CLI-STANDARDUSAGE-3F5TDV). Every script
// doing `drift account login ... && drift atomic deploy ...` therefore deployed
// after a failed login, and `set -e` could not help, because nothing failed as far
// as the shell could see.
//
// Nothing is printed here on failure: main() prints the returned error to stderr
// and exits 1 (SilenceErrors is set), so printing it too would emit the message
// twice, once per stream.
func DoLogin(username, password string) error {
	return DoLoginFactor(username, password, nil)
}

// DoLoginFactor is DoLogin with a way to answer a second-factor challenge.
func DoLoginFactor(username, password string, factor factorSupplier) error {
	if err := DoLoginWithFactor(username, password, factor); err != nil {
		return err
	}
	fmt.Printf("Logged in as %s.\n", username)
	return nil
}

// promptForFactor asks a person for their code.
//
// A code left EMPTY is taken as "I do not have my phone" and the prompt switches
// to a recovery code, rather than failing and making them start the whole login
// again — which is a bad moment to be sent back to the beginning, because it is
// exactly the moment the device is missing.
func promptForFactor() (string, bool, error) {
	code := strings.TrimSpace(common.PromptForInput("Authentication code (or press enter to use a recovery code)"))
	if code != "" {
		return code, false, nil
	}
	recovery := strings.TrimSpace(common.PromptForInput("Recovery code"))
	return recovery, true, nil
}

// fixedFactor answers with a code supplied up front, for CI.
func fixedFactor(code string, isRecovery bool) factorSupplier {
	return func() (string, bool, error) { return code, isRecovery, nil }
}

func GetLoginCmd() *cobra.Command {
	var username, password string
	var passwordStdin bool
	var mfaCode, recoveryCode string

	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Login to Drift and get a JWT token",
		Example: `  drift account login
  drift account login --username alice
  echo $PASS | drift account login -u alice --password-stdin
  echo $PASS | drift account login -u alice --password-stdin --mfa-code 123456`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if username == "" {
				username = common.PromptForInput("Username")
			}

			// Source resolution order: --password-stdin wins, then
			// --password, then interactive prompt. The two flags
			// are mutually exclusive at the cobra level below;
			// even so, this branch keeps the precedence clear.
			switch {
			case passwordStdin:
				password = common.ReadPasswordFromStdin()
			case password != "":
				fmt.Fprintln(os.Stderr,
					"warning: --password leaks the password to shell history and `ps`. "+
						"Prefer `--password-stdin` (echo $PASS | drift account login --password-stdin).")
			default:
				password = common.PromptForInputHidden("Password")
			}

			// How a second factor gets answered, if the platform asks for one.
			//
			// A code passed up front wins. Otherwise a person is prompted — but
			// ONLY when there is a person: with --password-stdin the input is a
			// pipe, and prompting there reads the next line of a script as if it
			// were a code, or blocks a CI job forever on a prompt nothing will
			// answer. A nil supplier makes that case an error naming the flag.
			var factor factorSupplier
			switch {
			case recoveryCode != "":
				factor = fixedFactor(recoveryCode, true)
			case mfaCode != "":
				factor = fixedFactor(mfaCode, false)
			case !passwordStdin:
				factor = promptForFactor
			}

			return DoLoginFactor(username, password, factor)
		},
	}

	loginCmd.Flags().StringVarP(&username, "username", "u", "", "Username (skips interactive prompt)")
	loginCmd.Flags().StringVarP(&password, "password", "p", "", "Password (DEPRECATED: leaks to ps + shell history; use --password-stdin)")
	loginCmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin (recommended for CI)")
	loginCmd.Flags().StringVar(&mfaCode, "mfa-code", "", "Six-digit authentication code, for non-interactive logins")
	loginCmd.Flags().StringVar(&recoveryCode, "recovery-code", "", "Use a single-use recovery code instead of an authentication code")
	loginCmd.MarkFlagsMutuallyExclusive("password", "password-stdin")
	loginCmd.MarkFlagsMutuallyExclusive("mfa-code", "recovery-code")

	return loginCmd
}
