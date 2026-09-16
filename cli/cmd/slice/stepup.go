package slice

// stepup.go — proving, freshly, that the person downloading a slice is the
// person who owns it.
//
// `snapshot download` streams EVERYTHING: every Backbone secret, every
// document, every blob, every row. A live session used to be enough, and the
// refresh token behind a session sits in `~/.drift/session.json` for thirty
// days — so a copied session file was a month of unlimited exfiltration.
//
// The platform now wants a single-use grant, minted by re-entering the account
// password. This is the client half: notice the refusal, ask, retry once.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// stepUpHeader is where the grant travels.
//
// A HEADER, never a query parameter: this is a GET whose URL lands in shell
// history, in a proxy log and in a `ps` listing, and a single-use credential in
// any of those is one somebody else can read before it is spent.
const stepUpHeader = "X-Drift-Step-Up"

// stepUpRefusal is the machine-readable part of the platform's 401.
//
// Read from the body rather than inferred from the status, because a bare 401
// is indistinguishable from an expired session — and a client that guessed
// wrong would send somebody to `drift account login` when what they need is to
// type their password once.
type stepUpRefusal struct {
	Error   string `json:"error"`
	Purpose string `json:"step_up_required"`
}

// peek reads a refusal's body so it can be inspected, leaving the response
// readable by nobody else — a refusal is small and the caller is about to
// replace this response anyway.
func peek(resp *http.Response) []byte {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return nil
	}
	return body
}

// needsStepUp reports the purpose a refusal names, or "" if this is not one.
func needsStepUp(status int, body []byte) string {
	if status != http.StatusUnauthorized {
		return ""
	}
	var r stepUpRefusal
	if err := json.Unmarshal(body, &r); err != nil {
		return ""
	}
	return r.Purpose
}

// mintStepUpGrant asks for the password and exchanges it for a single-use
// grant.
//
// The password is read with echo off and never reaches a flag, an argument or
// an environment variable — the same rule `drift account login` follows, and
// for a stronger reason here: this one is typed in the middle of another
// command, so a shell's history is the likeliest place for it to end up.
func mintStepUpGrant(purpose, why string) (string, error) {
	fmt.Println()
	fmt.Println(common.Hint("  " + why))
	password := common.PromptForInputHidden("  Password")
	if password == "" {
		return "", fmt.Errorf("a password is needed to download a snapshot")
	}

	body, err := json.Marshal(map[string]string{"password": password, "purpose": purpose})
	if err != nil {
		return "", err
	}
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/account/stepup", strings.NewReader(string(body)))
	if err != nil {
		return "", common.TransportError("verify your password", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// The platform's own wording, which distinguishes a wrong password from
		// an account it could not read. Restating it here would be a second
		// author of a refusal this side cannot see the reason for.
		return "", fmt.Errorf("could not verify your password: %s", strings.TrimSpace(string(raw)))
	}
	var out struct {
		Grant string `json:"grant"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Grant == "" {
		return "", fmt.Errorf("the platform accepted the password but returned no grant")
	}
	return out.Grant, nil
}
