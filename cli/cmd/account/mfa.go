package account

// mfa.go — `drift account mfa`: the second factor on your own account.
//
// Three verbs and a status. Everything the platform decides is decided in the
// auth service; what lives here is the sequence a person walks through, and one
// judgement that is genuinely the CLI's:
//
//	RECOVERY CODES ARE SHOWN EXACTLY ONCE AND NEVER WRITTEN TO A FILE.
//
// Offering to save them would put ten working second factors in a file beside
// the session, on the same disk, unencrypted — which is most of the way to not
// having a second factor at all. They are printed, with what they are for, and
// the user puts them somewhere this program cannot see.

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

const mfaBase = "/ops/account/mfa"

// mfaStatus is what the platform reports about an account's factor.
type mfaStatus struct {
	Enabled          bool `json:"enabled"`
	EnrolmentPending bool `json:"enrolment_pending"`
	RecoveryCodes    int  `json:"recovery_codes"`
}

func GetMFACmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mfa",
		Short: "Manage the second factor on your account",
		Long: "A second factor means a stolen password is not a stolen account.\n\n" +
			"Drift uses TOTP — the six-digit codes any authenticator app produces.\n" +
			"Enrolling gives you ten single-use recovery codes for the day the phone\n" +
			"is gone; they are shown once and never stored by this program.",
		Example: "  drift account mfa\n" +
			"  drift account mfa enrol\n" +
			"  drift account mfa disable",
	}
	cmd.AddCommand(getMFAStatusCmd(), getMFAEnrolCmd(), getMFADisableCmd())

	// `drift account mfa` with no verb reports the state rather than printing
	// usage. It is the question people actually arrive with — "is this on?" —
	// and a help screen is a worse answer to it than the answer.
	cmd.RunE = func(c *cobra.Command, _ []string) error { return printMFAStatus() }
	cmd.Args = cobra.NoArgs

	return cmd
}

func getMFAStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show whether a second factor is enabled",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         func(*cobra.Command, []string) error { return printMFAStatus() },
	}
}

func printMFAStatus() error {
	st, err := fetchMFAStatus()
	if err != nil {
		return err
	}

	switch {
	case st.Enabled:
		fmt.Println("Two-factor authentication is ON for your account.")
		switch {
		case st.RecoveryCodes == 0:
			fmt.Println(common.Hint("no recovery codes left — if you lose the device you cannot get back in. " +
				"Run `drift account mfa disable` and enrol again to get a fresh set."))
		case st.RecoveryCodes <= 3:
			fmt.Println(common.Hint(fmt.Sprintf("%d recovery codes left.", st.RecoveryCodes)))
		default:
			fmt.Printf("%d recovery codes remaining.\n", st.RecoveryCodes)
		}
	case st.EnrolmentPending:
		fmt.Println("Two-factor authentication is OFF — an enrolment was started and never confirmed.")
		fmt.Println(common.Hint("run `drift account mfa enrol` to start again."))
	default:
		fmt.Println("Two-factor authentication is OFF for your account.")
		fmt.Println(common.Hint("run `drift account mfa enrol` to turn it on."))
	}
	return nil
}

func fetchMFAStatus() (mfaStatus, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+mfaBase, nil)
	if err != nil {
		return mfaStatus{}, err
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read your second-factor settings")
	if err != nil {
		return mfaStatus{}, err
	}
	var st mfaStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return mfaStatus{}, fmt.Errorf("the platform returned a reply this version cannot read: %w", err)
	}
	return st, nil
}

func getMFAEnrolCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enrol",
		Short: "Turn on two-factor authentication",
		Long: "Starts an enrolment, shows the secret for your authenticator app, and\n" +
			"waits for the first code it produces. The factor is only turned on once\n" +
			"that code verifies — so an app that never received the secret cannot lock\n" +
			"you out of your own account.",
		Aliases:      []string{"enroll"},
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         func(*cobra.Command, []string) error { return runMFAEnrol() },
	}
}

func runMFAEnrol() error {
	// THE PASSWORD, and not because the platform is being fussy. A session on its
	// own is exactly what a stolen one is: without this, whoever holds it can
	// enrol THEIR authenticator and lock the owner out of their own account with
	// the control that was meant to protect them. Turning the factor on costs
	// what turning it off costs.
	password := common.PromptForInputHidden("Password")
	if password == "" {
		return fmt.Errorf("no password entered — nothing was changed")
	}
	reqBody, _ := json.Marshal(map[string]string{"password": password})

	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+mfaBase+"/enrol", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "start the enrolment")
	if err != nil {
		return err
	}
	var started struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(body, &started); err != nil {
		return fmt.Errorf("the platform returned a reply this version cannot read: %w", err)
	}

	fmt.Println("Scan this with your authenticator app.")
	fmt.Println()

	// THE QR IS THE POINT, and the secret beneath it is the fallback rather than
	// the other way round. Typing 32 base32 characters into a phone by hand is
	// where people abandon turning a second factor on.
	//
	// A failure to draw it is not a failure to enrol: the secret and the URI are
	// printed either way, and every authenticator app accepts a typed secret. So
	// the error is reported and the flow continues.
	if err := common.QRCode(os.Stdout, started.URI); err != nil {
		fmt.Println(common.Hint("could not draw the QR code here — enter the secret below by hand instead."))
		fmt.Println()
	}

	fmt.Printf("  Secret:  %s\n", started.Secret)
	fmt.Printf("  URI:     %s\n", started.URI)
	fmt.Println()
	fmt.Println(common.Hint("no camera to hand? every authenticator app also accepts the secret typed in."))
	fmt.Println()

	// The confirm step, in the same command. Two commands would leave a user
	// holding a secret their account does not yet require, which is the state
	// that looks like it worked and is not.
	code := strings.TrimSpace(common.PromptForInput("Enter the code your app shows now"))
	if code == "" {
		return fmt.Errorf("no code entered — two-factor authentication is NOT on. " +
			"Nothing was changed; run `drift account mfa enrol` again when the app is ready")
	}

	confirmBody, _ := json.Marshal(map[string]string{"code": code})
	confirmResp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+mfaBase+"/confirm", bytes.NewReader(confirmBody))
	if err != nil {
		return err
	}
	defer confirmResp.Body.Close()

	confirmed, err := common.CheckResponse(confirmResp, "confirm the enrolment")
	if err != nil {
		return err
	}
	var out struct {
		Codes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(confirmed, &out); err != nil {
		return fmt.Errorf("the platform returned a reply this version cannot read: %w", err)
	}

	fmt.Println()
	fmt.Println("Two-factor authentication is ON.")
	fmt.Println()
	fmt.Println("RECOVERY CODES — these are shown once and cannot be shown again.")
	fmt.Println("Each one works exactly once, in place of a code from your app.")
	fmt.Println("Keep them somewhere that is not this machine.")
	fmt.Println()
	for _, c := range out.Codes {
		fmt.Printf("    %s\n", c)
	}
	fmt.Println()
	fmt.Println(common.Hint("use one with: drift account login --recovery-code <code>"))
	return nil
}

func getMFADisableCmd() *cobra.Command {
	var code, recoveryCode string

	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Turn off two-factor authentication",
		Long: "Turns the second factor off. This asks for your password AND a current\n" +
			"code, because a session on its own is exactly what a theft has — and\n" +
			"surviving that is what the factor is for.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			st, err := fetchMFAStatus()
			if err != nil {
				return err
			}
			if !st.Enabled && !st.EnrolmentPending {
				fmt.Println("Two-factor authentication is already off.")
				return nil
			}

			password := common.PromptForInputHidden("Password")
			if password == "" {
				return fmt.Errorf("no password entered — nothing was changed")
			}

			isRecovery := recoveryCode != ""
			supplied := code
			if isRecovery {
				supplied = recoveryCode
			}
			if supplied == "" {
				supplied, isRecovery, err = promptForFactor()
				if err != nil {
					return err
				}
			}
			if supplied == "" {
				return fmt.Errorf("no code entered — two-factor authentication is still ON, nothing was changed")
			}

			payload := map[string]string{"password": password}
			if isRecovery {
				payload["recovery_code"] = supplied
			} else {
				payload["code"] = supplied
			}
			reqBody, _ := json.Marshal(payload)

			resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+mfaBase+"/disable", bytes.NewReader(reqBody))
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "turn off two-factor authentication"); err != nil {
				return err
			}

			fmt.Println("Two-factor authentication is OFF.")
			fmt.Println(common.Hint("your password is now the only thing protecting this account."))
			return nil
		},
	}

	cmd.Flags().StringVar(&code, "code", "", "Six-digit authentication code, for non-interactive use")
	cmd.Flags().StringVar(&recoveryCode, "recovery-code", "", "Use a recovery code instead — for a device you no longer have")
	cmd.MarkFlagsMutuallyExclusive("code", "recovery-code")

	return cmd
}
