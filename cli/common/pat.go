package common

// pat.go — using a personal access token instead of a session.
//
// `drift account login` writes a session file and every command reads it. That
// is right for a person at a terminal and wrong for everything else: a CI job has
// no terminal to log in from, no home directory worth persisting, and no business
// holding the owner's own credentials.
//
// So a token is presented through the ENVIRONMENT and nothing is written to disk:
//
//	export DRIFT_TOKEN=drift_pat_…
//	export DRIFT_SLICE=my-slice      # what `drift slice use` would have set
//	drift file apply
//
// # It is EXCHANGED, not sent
//
// The token itself never reaches a control-plane route. It is swapped at
// /token/exchange for an ordinary short-lived access token carrying the scopes it
// was minted with, and that is what every request afterwards carries. Two things
// follow, and both are the point:
//
//   - Nothing downstream needs to know this feature exists. The api sees the same
//     signed token a login produces, with a narrower `scopes` claim, and the
//     enforcement path is the one that already existed.
//   - Revocation works. A signed token cannot be recalled, so if the credential
//     itself were sent to every route, "revoke" would mean nothing until it
//     expired. Exchanging puts one store lookup in front of it.
//
// # One exchange per invocation
//
// The access token is cached for the life of the PROCESS and never written down.
// A CLI invocation is short, so this is one extra round trip per command rather
// than per request — and a cache that outlived the process would be a session
// file by another name, which is what this avoids.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

const (
	// TokenEnv holds a personal access token. When set, it REPLACES the session
	// file entirely — nothing is read from disk and nothing is written to it.
	TokenEnv = "DRIFT_TOKEN"

	// SliceEnv is what `drift slice use` would have recorded. A token carries an
	// account, not a slice, so a scripted caller has to say which one — and
	// without this the whole feature stops at the first command that needs one.
	SliceEnv = "DRIFT_SLICE"

	// patPrefix marks a Drift personal access token, matching what the platform
	// mints. Used to tell one apart from something pasted into the wrong
	// variable, never as validation: only the platform can say whether a token is
	// real, and this says only that it is the right SHAPE.
	patPrefix = "drift_pat_"
)

// PersonalAccessToken returns the token this invocation was given, or "".
func PersonalAccessToken() string {
	return strings.TrimSpace(os.Getenv(TokenEnv))
}

// exchanged caches the access token this process obtained, for its lifetime.
var exchanged struct {
	sync.Mutex
	token string
}

// exchangePAT swaps the personal access token for a short-lived access token.
//
// The refusals are kept apart for the same reason RefreshAccessToken keeps its
// two apart: a rejected credential and an unreachable platform look identical to
// a caller and mean opposite things. Telling a pipeline its token is dead during
// an outage sends someone to mint a replacement for a credential that was fine.
func exchangePAT(ctx context.Context, raw string) (string, error) {
	body, _ := json.Marshal(map[string]string{"token": raw})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		APIBaseURL+"/token/exchange", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPlatformUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		hint := ""
		if !strings.HasPrefix(raw, patPrefix) {
			// The likeliest cause by a distance, and one the platform cannot
			// diagnose: something else entirely is in the variable.
			hint = "\n  " + TokenEnv + " does not look like a Drift token — they start with " + patPrefix
		}
		return "", errors.New(withCode(
			"the token in "+TokenEnv+" was refused — it may have been revoked, or have expired."+
				"\n  List what this account holds with `drift account token list`."+hint,
			"DRIFT-1011"))
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w (exchange returned %d)", ErrPlatformUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("exchanging %s returned %d", TokenEnv, resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to parse the exchange response: %w", err)
	}
	if out.AccessToken == "" {
		// An empty token would go out as `Authorization: Bearer `, and the 401
		// that follows would read as a revoked credential rather than as the
		// exchange that returned nothing.
		return "", fmt.Errorf("the exchange returned no access token")
	}
	return out.AccessToken, nil
}

// patAccessToken returns this process's access token, exchanging on first use.
func patAccessToken(ctx context.Context, raw string) (string, error) {
	exchanged.Lock()
	defer exchanged.Unlock()
	if exchanged.token != "" {
		return exchanged.token, nil
	}
	tok, err := exchangePAT(ctx, raw)
	if err != nil {
		return "", err
	}
	exchanged.token = tok
	return tok, nil
}

// currentAccessToken returns the token this invocation is acting with, and the
// refresh token beside it when there is one.
//
// It is how anything that wants to READ the current identity — rather than send
// a request with it — gets at the token, without having to know whether this
// invocation is a session or a PAT. The PAT branch reports only what has ALREADY
// been exchanged: this is a read, and a read that silently made a network call
// would turn `drift account whoami` into something that can fail on a plane.
func currentAccessToken() (token string, refresh string, err error) {
	if PersonalAccessToken() != "" {
		exchanged.Lock()
		defer exchanged.Unlock()
		if exchanged.token == "" {
			return "", "", fmt.Errorf("no access token has been exchanged yet")
		}
		return exchanged.token, "", nil
	}
	return GetTokenFromSession()
}

// forgetExchangedToken drops the cached access token so the next request
// exchanges again. Called on a 401, which for a PAT caller means the
// fifteen-minute access token aged out mid-command rather than that the
// credential is bad — the credential is re-presented and the command continues.
func forgetExchangedToken() {
	exchanged.Lock()
	defer exchanged.Unlock()
	exchanged.token = ""
}
