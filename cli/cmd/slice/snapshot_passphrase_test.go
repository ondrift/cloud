package slice

import (
	"net/http"
	"strings"
	"testing"
)

// Recognising the platform's "this archive needs your passphrase" refusal.
//
// Read from the BODY, like the step-up refusal beside it, and for the same
// reason: a bare 401 is indistinguishable from an expired session, and sending
// somebody to `drift account login` when what they need is to type a passphrase
// is a wrong answer that looks authoritative.

func TestPassphraseRefused_RecognisesBothCases(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"none was given": {
			`{"error":"this snapshot was created with a passphrase","passphrase_required":"abc","reason":"passphrase_required"}`,
			reasonPassphraseRequired,
		},
		"the wrong one": {
			`{"error":"that passphrase does not open this snapshot","passphrase_required":"abc","reason":"wrong_passphrase"}`,
			reasonWrongPassphrase,
		},
	}
	for name, c := range cases {
		if got := passphraseRefused(http.StatusUnauthorized, []byte(c.body)); got != c.want {
			t.Errorf("%s: reason = %q, want %q", name, got, c.want)
		}
	}
}

// AN ORDINARY 401 IS NOT A PASSPHRASE REFUSAL. An expired session answers 401
// too, and a step-up refusal answers 401 with a different key — reading either
// as a passphrase problem would prompt for a passphrase that does not exist.
func TestPassphraseRefused_IgnoresEveryOther401(t *testing.T) {
	for name, body := range map[string]string{
		"an expired session": `{"error":"token expired"}`,
		"a step-up request":  `{"error":"needs a fresh proof of identity","step_up_required":"snapshot.download"}`,
		"not JSON at all":    `Unauthorized`,
		"empty":              ``,
		"the key but empty":  `{"passphrase_required":""}`,
	} {
		if got := passphraseRefused(http.StatusUnauthorized, []byte(body)); got != "" {
			t.Errorf("%s: read as a passphrase refusal (%q)", name, got)
		}
	}
}

// And no other status is one. A 403 or a 500 carrying this key would otherwise
// send the user into a passphrase prompt over a failure a passphrase cannot fix.
func TestPassphraseRefused_OnlyEverOn401(t *testing.T) {
	body := []byte(`{"passphrase_required":"abc","reason":"wrong_passphrase"}`)
	for _, status := range []int{
		http.StatusOK, http.StatusForbidden, http.StatusNotFound,
		http.StatusBadGateway, http.StatusInternalServerError,
	} {
		if got := passphraseRefused(status, body); got != "" {
			t.Errorf("status %d was read as a passphrase refusal", status)
		}
	}
}

// The passphrase goes in a HEADER, and the name must be the one the api reads.
// A mismatch here is silent: the header is simply not forwarded, the operator
// seals under the platform's key, and the tenant is told their archive is
// protected by a passphrase that reached nothing.
func TestSnapshotPassphraseHeader_MatchesTheEdge(t *testing.T) {
	if snapshotPassphraseHeader != "X-Drift-Snapshot-Passphrase" {
		t.Errorf("snapshotPassphraseHeader = %q; it must match what the api reads", snapshotPassphraseHeader)
	}
}

// The two refusals get DIFFERENT sentences, because they send the reader to
// different places: one means type it, the other means the one you typed is not
// it. A single message for both is how somebody concludes their backup is
// corrupt.
func TestPassphraseError_SaysWhichFailureThisIs(t *testing.T) {
	wrong := passphraseError(reasonWrongPassphrase, "abc", "download").Error()
	missing := passphraseError(reasonPassphraseRequired, "abc", "download").Error()

	if wrong == missing {
		t.Fatal("a wrong passphrase and a missing one produced the same message")
	}
	for name, msg := range map[string]string{"wrong": wrong, "missing": missing} {
		if !strings.Contains(msg, "abc") {
			t.Errorf("%s: the message must name the snapshot, got %q", name, msg)
		}
		// Neither may read as a broken archive. A restore is what somebody runs
		// when the snapshot is the only copy left, and telling them it is
		// damaged is the most expensive wrong answer available here.
		for _, forbidden := range []string{"corrupt", "damaged", "invalid archive"} {
			if strings.Contains(strings.ToLower(msg), forbidden) {
				t.Errorf("%s: the message calls the archive %q when it is intact: %q", name, forbidden, msg)
			}
		}
	}
	if !strings.Contains(wrong, "does not open") {
		t.Errorf("the wrong-passphrase message should say the passphrase did not open it, got %q", wrong)
	}
}

// An unknown reason still produces the ASK message rather than an empty one. A
// platform that grows a third reason must not leave the user with a blank
// error, and "you need a passphrase" is right for anything in this family.
func TestPassphraseError_FallsBackToAsking(t *testing.T) {
	msg := passphraseError("something-new", "abc", "restore").Error()
	if !strings.Contains(msg, "passphrase") || !strings.Contains(msg, "abc") {
		t.Errorf("an unrecognised reason must still produce a usable message, got %q", msg)
	}
	if !strings.Contains(msg, "restore") {
		t.Errorf("the message should name the verb the caller was attempting, got %q", msg)
	}
}

// --passphrase-stdin with nothing on stdin is an ERROR, not an empty
// passphrase. Sealing an archive under "" would produce a snapshot every reader
// of this code could open, while the script that took it reported success.
//
// A test binary's stdin is empty, which is exactly the case under test: a
// `--passphrase-stdin` in a pipeline where the upstream command produced
// nothing. Both directions are checked, because the create side seals under
// what it gets and the read side sends it.
func TestPassphraseFromStdin_EmptyIsRefusedInBothDirections(t *testing.T) {
	if _, err := newPassphrase(true); err == nil {
		t.Error("an empty stdin passphrase must be refused, not used to seal an archive")
	}
	if _, err := askForPassphrase(true); err == nil {
		t.Error("an empty stdin passphrase must be refused, not sent as an attempt")
	}
}
