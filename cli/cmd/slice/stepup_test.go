package slice

import (
	"net/http"
	"testing"
)

// Recognising the platform's "prove who you are" refusal.
//
// This has to be read from the BODY rather than inferred from the status. A
// bare 401 is indistinguishable from an expired session, and a client that
// guessed wrong would send somebody to `drift account login` when what they
// need is to type their password once.

func TestNeedsStepUp_RecognisesTheRefusal(t *testing.T) {
	body := []byte(`{"error":"this action needs a fresh proof of identity","step_up_required":"snapshot.download"}`)

	if got := needsStepUp(http.StatusUnauthorized, body); got != "snapshot.download" {
		t.Errorf("purpose = %q, want the one the platform named", got)
	}
}

// AN ORDINARY 401 IS NOT A STEP-UP REQUEST. An expired session answers 401 too,
// and prompting for a password there would ask somebody to authenticate a
// session that is already gone — the password would be checked against a token
// the platform has stopped honouring.
func TestNeedsStepUp_IgnoresAnOrdinary401(t *testing.T) {
	for name, body := range map[string]string{
		"an expired session": `{"error":"token expired"}`,
		"not JSON at all":    `Unauthorized`,
		"empty":              ``,
		"a null purpose":     `{"step_up_required":null}`,
	} {
		if got := needsStepUp(http.StatusUnauthorized, []byte(body)); got != "" {
			t.Errorf("%s: read as a step-up request (%q)", name, got)
		}
	}
}

// And no other status is one, however the body reads. A 403 or a 500 carrying
// this key would otherwise send the client into a password prompt on a failure
// a password cannot fix.
func TestNeedsStepUp_OnlyEverOn401(t *testing.T) {
	body := []byte(`{"step_up_required":"snapshot.download"}`)
	for _, status := range []int{
		http.StatusOK, http.StatusForbidden, http.StatusNotFound,
		http.StatusBadGateway, http.StatusInternalServerError,
	} {
		if got := needsStepUp(status, body); got != "" {
			t.Errorf("status %d was read as a step-up request", status)
		}
	}
}

// The grant goes in a HEADER. This is a GET whose URL lands in shell history, a
// proxy log and a `ps` listing, and a single-use credential in any of those is
// one somebody else can read before it is spent.
func TestStepUpHeader_IsAHeaderName(t *testing.T) {
	if stepUpHeader != "X-Drift-Step-Up" {
		t.Errorf("stepUpHeader = %q; it must match what the api reads", stepUpHeader)
	}
}
