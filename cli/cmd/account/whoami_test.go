package account

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSession puts a session file carrying a token with the given username
// claim into a temporary HOME.
//
// The token is unsigned — the CLI reads its own claims for display and never
// verifies them, because it has no key to verify with and the server is the
// authority on every call that matters.
func writeSession(t *testing.T, username string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	claims, _ := json.Marshal(map[string]any{"username": username, "exp": 1})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	token := "header." + payload + ".signature"

	dir := filepath.Join(home, ".drift")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "refresh_token": "r"})
	if err := os.WriteFile(filepath.Join(dir, "session.json"), body, 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
}

// runWhoami executes the command and captures stdout.
func runWhoami(t *testing.T) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	cmdErr := GetWhoamiCmd().RunE(nil, nil)

	if cerr := w.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	os.Stdout = original

	var buf bytes.Buffer
	if _, rerr := buf.ReadFrom(r); rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return buf.String(), cmdErr
}

// The whole contract: the username, alone, on one line. A label or a
// surrounding sentence would make `$(drift account whoami)` unusable, which is
// the case the command exists for.
func TestWhoami_PrintsTheUsernameAndNothingElse(t *testing.T) {
	writeSession(t, "alice")

	out, err := runWhoami(t)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if out != "alice\n" {
		t.Errorf("got %q, want %q — anything else breaks $(drift account whoami)", out, "alice\n")
	}
}

// An expired token still answers. "Who am I logged in as" has a true answer
// after a token ages out, and refusing would make the command useless at the
// moment someone is working out why a call 401'd.
//
// The fixture's exp is 1 — January 1970 — so this is the expired case already.
func TestWhoami_AnExpiredTokenStillAnswers(t *testing.T) {
	writeSession(t, "alice")

	out, err := runWhoami(t)
	if err != nil {
		t.Fatalf("an expired session must still report who it belongs to: %v", err)
	}
	if strings.TrimSpace(out) != "alice" {
		t.Errorf("got %q, want alice", out)
	}
}

// No session is an ERROR, not an empty line. A command substitution that
// silently yields "" is how a script writes an empty owner, tags an empty
// account, or compares against nothing and takes the wrong branch.
func TestWhoami_WithNoSessionItFailsRatherThanPrintingNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	out, err := runWhoami(t)
	if err == nil {
		t.Fatal("with no session this must fail — an empty line silently poisons every $(…) that uses it")
	}
	if out != "" {
		t.Errorf("nothing may be printed on stdout when there is no answer, got %q", out)
	}
	if !strings.Contains(err.Error(), "drift account login") {
		t.Errorf("the error must say how to fix it, got %q", err)
	}
}

// A session file whose token is not a readable JWT is the same refusal. It is a
// broken session rather than a missing one, but the caller can do exactly one
// thing about either.
func TestWhoami_AnUnreadableTokenIsAlsoARefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".drift")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"token": "not-a-jwt", "refresh_token": "r"})
	if err := os.WriteFile(filepath.Join(dir, "session.json"), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := runWhoami(t); err == nil {
		t.Fatal("an unreadable token must not print an empty username")
	}
}
