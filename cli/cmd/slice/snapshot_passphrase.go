// snapshot_passphrase.go — the tenant's own key for a snapshot archive.
//
// A snapshot is ordinarily sealed under a key the platform holds, so it can
// serve a download and run a restore without you present. Giving one a
// passphrase replaces that key with one derived from what you type, and the
// replacement is total: the platform cannot open the archive, and a passphrase
// nobody remembers is a snapshot nobody can ever read again.
//
// That is why nothing here accepts a passphrase as an ordinary flag value. A
// password typed as `--passphrase hunter2` lands in shell history and in `ps`,
// and for a password whose loss is unrecoverable and whose leak is a whole
// slice, neither is acceptable. The flags are switches: one says prompt me, one
// says read it from stdin.
package slice

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

// snapshotPassphraseHeader is where the passphrase travels. A header rather
// than the body or the query, because a download's URL lands in access logs.
const snapshotPassphraseHeader = "X-Drift-Snapshot-Passphrase"

// passphraseRefusal is the machine-readable part of the platform's 401.
//
// `Reason` is what separates "you need to give one" from "that one is wrong".
// The sentence in `Error` says the same thing in prose, and matching on prose
// is how a client breaks the first time the wording improves.
type passphraseRefusal struct {
	Error  string `json:"error"`
	ID     string `json:"passphrase_required"`
	Reason string `json:"reason"`
}

const (
	reasonPassphraseRequired = "passphrase_required"
	reasonWrongPassphrase    = "wrong_passphrase"
)

// passphraseRefused reports which passphrase failure a response is, or "" if it
// is not one.
func passphraseRefused(status int, body []byte) string {
	if status != http.StatusUnauthorized {
		return ""
	}
	var r passphraseRefusal
	if err := json.Unmarshal(body, &r); err != nil || r.ID == "" {
		return ""
	}
	return r.Reason
}

// passphraseError turns a refusal into the sentence the user needs.
//
// SEPARATE WORDING for the two cases, because they send somebody to different
// places: one means the archive needs a passphrase and none was given, and the
// other means the one given is not it. Reporting either as a failed download
// would send them looking for a damaged backup that is fine.
func passphraseError(reason, id, verb string) error {
	switch reason {
	case reasonWrongPassphrase:
		return fmt.Errorf("that passphrase does not open snapshot %s. "+
			"It is the passphrase the snapshot was created with, and nothing else can open the archive", id)
	default:
		return fmt.Errorf("snapshot %s was created with a passphrase, and it is needed to %s it. "+
			"Run the command again and enter it when asked", id, verb)
	}
}

// askForPassphrase reads a passphrase for an EXISTING archive: once, because
// getting it wrong is answered by the platform rather than by a second prompt.
func askForPassphrase(stdin bool) (string, error) {
	if stdin {
		pass := common.ReadPasswordFromStdin()
		if pass == "" {
			return "", fmt.Errorf("--passphrase-stdin was given but nothing arrived on stdin")
		}
		return pass, nil
	}
	fmt.Println()
	fmt.Println(common.Hint("  This snapshot was created with a passphrase. Without it the archive cannot be opened, by you or by us."))
	pass := common.PromptForInputHidden("  Passphrase")
	if pass == "" {
		return "", fmt.Errorf("a passphrase is needed to open this snapshot")
	}
	return pass, nil
}

// newPassphrase reads a passphrase for an archive that does not exist yet:
// TWICE, and they must match.
//
// The confirmation is not ceremony. A mistyped password on an account is
// recovered by resetting it; a mistyped passphrase here produces an archive
// that is already sealed, and there is no reset — the snapshot is unreadable
// from the moment it is written, and nothing discovers that until the day
// somebody needs it.
func newPassphrase(stdin bool) (string, error) {
	if stdin {
		pass := common.ReadPasswordFromStdin()
		if pass == "" {
			return "", fmt.Errorf("--passphrase-stdin was given but nothing arrived on stdin")
		}
		return pass, nil
	}

	fmt.Println()
	fmt.Println(common.Hint("  The archive will be encrypted with this passphrase instead of with the platform's key."))
	fmt.Println(common.Hint("  Drift will not be able to open it, and there is no way to recover it if you forget."))
	pass := common.PromptForInputHidden("  Passphrase")
	if pass == "" {
		return "", fmt.Errorf("a passphrase is required when --passphrase is given")
	}
	again := common.PromptForInputHidden("  Passphrase again")
	if pass != again {
		return "", fmt.Errorf("the two passphrases do not match; nothing was created")
	}
	return pass, nil
}

// withJSONContentType is what DoJSONRequest does, for the call sites that also
// need a header of their own and so cannot use it.
func withJSONContentType(headers map[string]string) map[string]string {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/json"
	return headers
}

// snapshotIsPassphraseProtected asks the platform whether opening this archive
// will need a passphrase.
//
// ASKED UP FRONT on download, rather than discovered from a refusal, because
// the download also spends a single-use step-up grant. Finding out afterwards
// would mean a second password prompt to mint a second grant, for one download.
//
// A failure to find out is not an error: the caller proceeds without a
// passphrase and the platform refuses if one was needed, which is the same
// outcome one prompt later.
func snapshotIsPassphraseProtected(id string) bool {
	resp, err := common.DoRequest(http.MethodGet,
		fmt.Sprintf("%s/ops/slice/snapshot/status?id=%s", common.APIBaseURL, id), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	var snap struct {
		PassphraseProtected bool `json:"passphrase_protected"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		return false
	}
	return snap.PassphraseProtected
}
