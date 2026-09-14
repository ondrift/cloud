// delete_gate_test.go — --yes drops the prompts, never the identity check.
//
// `drift account delete` is the most destructive command the CLI has: it takes
// every slice, every snapshot and the username with it, and there is nothing to
// restore from. The two interactive prompts end in a typed username, which is
// the control — the first `[y/N]` is only friction.
//
// A flag that removed both left nothing at all standing between a copy-pasted
// command and an account. The name now has to appear on the command line
// instead, which is the same shape `slice delete` uses: CI keeps its
// non-interactive path, and the operator still has to get the identity right.
package account

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// tokenFor builds an unsigned JWT carrying just the username claim. The CLI
// reads the username out of the token's payload locally and never verifies the
// signature — the server does that — so this is enough to stand in for a real
// session without a network call or a key.
func tokenFor(username string) string {
	payload, _ := json.Marshal(map[string]any{"username": username})
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf("%s.%s.%s",
		enc([]byte(`{"alg":"none","typ":"JWT"}`)), enc(payload), enc([]byte("sig")))
}

// loggedInAs points HOME at a scratch dir and writes a session for `who`, so
// the command under test resolves a username without a network call.
func loggedInAs(t *testing.T, who string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatalf("seeding home: %v", err)
	}
	if err := common.SaveSession(tokenFor(who), "refresh"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	if got := common.GetUsername(); got != who {
		t.Fatalf("seeded session resolves to %q, want %q", got, who)
	}
}

// runDelete executes the command with args and returns everything it printed.
func runDelete(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := runDeleteE(t, args...)
	return out
}

// runDeleteE is runDelete plus the ERROR the command returned, which is what
// decides the process's exit code.
//
// The text and the exit code are two different claims and only one of them was
// ever asserted here. The handler was a cobra `Run`, which has no error channel:
// it printed a refusal and returned, so the process exited 0 and a script read a
// REFUSED delete as a completed one. On this command that is the worst available
// answer — the caller concludes the account is gone.
func runDeleteE(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := GetDeleteCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	// The root sets both in the real binary; set them here so the captured text
	// is the command's own message rather than cobra's usage dump.
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	execErr := cmd.Execute()
	_ = w.Close()
	os.Stdout = stdout

	var printed bytes.Buffer
	_, _ = printed.ReadFrom(r)
	return printed.String() + out.String() + errString(execErr), execErr
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The defect: --yes with nothing named destroyed the account outright.
func TestYesWithoutTheAccountNameRefuses(t *testing.T) {
	loggedInAs(t, "alice")

	got, err := runDeleteE(t, "--yes")
	if !strings.Contains(got, "Refusing to delete account") {
		t.Errorf("--yes with no name did not refuse; it said: %s", got)
	}
	if !strings.Contains(got, "drift account delete alice --yes") {
		t.Errorf("the refusal does not show what to run instead: %s", got)
	}
	// AND IT EXITS NON-ZERO. A refusal that exits 0 is read by every script as
	// a completed delete.
	if err == nil {
		t.Error("the refusal returned no error, so the process exits 0 and " +
			"`drift account delete --yes && …` carries on as though the account were gone")
	}
}

// Naming the WRONG account must refuse too — otherwise the argument is
// ceremony rather than a check, and a copy-pasted command still lands.
func TestYesWithTheWrongAccountNameRefuses(t *testing.T) {
	loggedInAs(t, "alice")

	got, err := runDeleteE(t, "bob", "--yes")
	if !strings.Contains(got, "Refusing to delete account") {
		t.Errorf("--yes with the wrong name did not refuse; it said: %s", got)
	}
	if !strings.Contains(got, "bob") || !strings.Contains(got, "alice") {
		t.Errorf("the refusal names neither what was asked for nor what is logged in: %s", got)
	}
	if err == nil {
		t.Error("the refusal returned no error, so the process exits 0")
	}
}

// Not being logged in is a refusal too, and it has the same exit-code claim.
func TestDeleteWithNoSessionRefusesAndExitsNonZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := runDeleteE(t, "--yes")
	if err == nil {
		t.Fatalf("deleting with no session returned no error; it said: %s", got)
	}
	if !strings.Contains(got, "drift account login") {
		t.Errorf("the refusal does not name the way forward: %s", got)
	}
}

// The control: the flag must still work for CI when the name is right. This
// one reaches the network call and fails there, which is proof enough that the
// gate let it through — the gate refuses before any request is made.
func TestYesWithTheRightAccountNamePassesTheGate(t *testing.T) {
	loggedInAs(t, "alice")
	t.Setenv("DRIFT_API_BASE", "http://127.0.0.1:1")

	got := runDelete(t, "alice", "--yes")
	if strings.Contains(got, "Refusing to delete account") {
		t.Errorf("the correct name was refused: %s", got)
	}
}

// The interactive path keeps its own typed-username gate, so the argument is
// optional there — demanding both would be friction with no extra proof.
func TestTheNameIsOptionalWithoutYes(t *testing.T) {
	cmd := GetDeleteCmd()
	if err := cmd.Args(cmd, []string{}); err != nil {
		t.Errorf("no-argument form was rejected: %v", err)
	}
	if err := cmd.Args(cmd, []string{"alice"}); err != nil {
		t.Errorf("one-argument form was rejected: %v", err)
	}
	if err := cmd.Args(cmd, []string{"alice", "bob"}); err == nil {
		t.Error("two arguments were accepted")
	}
}

// The flag help has to say what still must be right. "Skip confirmation
// prompts" alone reads as a complete bypass, because it was one.
func TestTheFlagHelpNamesTheSurvivingGate(t *testing.T) {
	cmd := GetDeleteCmd()
	help := cmd.Flags().Lookup("yes").Usage
	if !strings.Contains(help, "must still match") {
		t.Errorf("--yes help does not name the surviving gate: %q", help)
	}
}
