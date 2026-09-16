package common

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Telling an expired session apart from a refusal the platform MEANT.
//
// Both are 401. The first is fixed by refreshing the token and re-sending; the
// second is the platform's answer to the request, and re-sending it answers a
// question nobody asked — while spending again whatever single-use credential
// the request carried.

// resp401 builds a 401 whose body is what the platform sent.
func resp401(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// THE TWO REFUSALS ARE RECOGNISED, by their machine-readable key rather than by
// the sentence beside it.
//
// This is the case that cost a rehearsal: a snapshot download spends its
// step-up grant on the attempt that carries it, the operator then refuses for a
// wrong passphrase, and a blind retry re-sent the SPENT grant. The user was
// told their proof of identity had already been used, which is true and is not
// the reason, and the real refusal never reached them.
func TestIsApplicationRefusal_RecognisesWhatThePlatformMeant(t *testing.T) {
	cases := map[string]string{
		"a step-up request": `{"error":"this action needs a fresh proof of identity","step_up_required":"snapshot.download"}`,
		"a wrong passphrase": `{"error":"that passphrase does not open this snapshot",` +
			`"passphrase_required":"abc","reason":"wrong_passphrase"}`,
		"a missing passphrase": `{"error":"this snapshot was created with a passphrase",` +
			`"passphrase_required":"abc","reason":"passphrase_required"}`,
	}
	for name, body := range cases {
		if !isApplicationRefusal(resp401(body)) {
			t.Errorf("%s: read as an expired session, so it would be retried and the reason lost", name)
		}
	}
}

// AN ORDINARY 401 IS STILL AN ORDINARY 401. An access token that aged out is
// the common case by a wide margin, and it is fixed invisibly by a refresh — a
// change that stopped refreshing would send every user to `drift account login`
// fifteen minutes into a session.
func TestIsApplicationRefusal_LeavesAnExpiredSessionAlone(t *testing.T) {
	for name, body := range map[string]string{
		"an expired token":   `{"error":"token expired"}`,
		"not JSON at all":    `Unauthorized`,
		"empty":              ``,
		"a JSON array":       `[{"step_up_required":"x"}]`,
		"the key but empty":  `{"step_up_required":""}`,
		"a neighbouring key": `{"step_up":"snapshot.download"}`,
	} {
		if isApplicationRefusal(resp401(body)) {
			t.Errorf("%s: read as a platform refusal, so an expired session would never refresh", name)
		}
	}
}

// THE BODY SURVIVES THE PEEK, on both paths.
//
// The caller reads it on every route out — to find the purpose a step-up names,
// to find which passphrase failure this is, or to print the platform's reason.
// A check that consumed the body would leave every one of those reading an
// empty string, and each would then report the wrong thing confidently.
func TestIsApplicationRefusal_RestoresTheBodyEitherWay(t *testing.T) {
	for name, body := range map[string]string{
		"a refusal":         `{"error":"needs a passphrase","passphrase_required":"abc"}`,
		"an expired 401":    `{"error":"token expired"}`,
		"not JSON":          `Unauthorized`,
		"a very long value": `{"error":"` + strings.Repeat("x", 4096) + `","step_up_required":"p"}`,
	} {
		resp := resp401(body)
		isApplicationRefusal(resp)

		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("%s: reading the restored body failed: %v", name, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s: the body did not survive the peek\n got: %q\nwant: %q", name, got, body)
		}
	}
}

// A body past the peek's limit is still served whole. The limit bounds how much
// is INSPECTED, not how much the caller can read — and a response big enough to
// hit it is one the caller most needs intact.
func TestIsApplicationRefusal_RestoresABodyLargerThanThePeek(t *testing.T) {
	body := strings.Repeat("y", 32<<10)
	resp := resp401(body)
	isApplicationRefusal(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the restored body failed: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("the body was truncated to %d bytes, want %d", len(got), len(body))
	}
	if string(got) != body {
		t.Error("the body past the peek's limit did not come back verbatim")
	}
}
