// logout_test.go — `drift account logout` ends the session on this machine and
// says which account it was.
//
// Every test runs the command through the account group, the way
// `drift account logout` reaches it, so a command that exists but is not
// registered fails here too. HOME is a scratch directory in every test: nothing
// here can read or remove the real ~/.drift.
package account

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// logoutSession points HOME at a scratch directory and logs in each account in
// turn, so the last one is current. The environment overrides that would change
// which account, or which credential, a command uses are cleared. It returns the
// scratch HOME.
func logoutSession(t *testing.T, accounts ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(common.AccountEnv, "")
	t.Setenv(common.TokenEnv, "")
	for _, a := range accounts {
		if err := common.SaveSession(tokenFor(a), "refresh-"+a); err != nil {
			t.Fatalf("seeding the session for %s: %v", a, err)
		}
	}
	return home
}

// memberTokenFor is the token of somebody working on another account: `username`
// names the owner and `actor` names them, and the profile is filed under them.
func memberTokenFor(owner, actor string) string {
	payload, _ := json.Marshal(map[string]any{"username": owner, "actor": actor})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "." + enc([]byte("sig"))
}

func sessionPath(home string) string {
	return filepath.Join(home, ".drift", "session.json")
}

// writeSessionFile puts raw bytes where the session file lives.
func writeSessionFile(t *testing.T, home string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sessionPath(home)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sessionPath(home), body, 0o600); err != nil {
		t.Fatalf("writing the session file: %v", err)
	}
}

// runLogout runs `account logout` with args and returns everything it printed
// and the error it returned, which decides the process's exit status.
func runLogout(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := GetAccountCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"logout"}, args...))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// requireLogout fails the test unless `logout` is a registered subcommand of the
// account group. The tests that pin what a command REFUSES would otherwise pass
// against a group that has no such command, because an unknown verb is refused
// too.
func requireLogout(t *testing.T) {
	t.Helper()
	found, _, err := GetAccountCmd().Find([]string{"logout"})
	if err != nil || found.Name() != "logout" {
		t.Fatalf("`drift account logout` is not registered on the account group (Find: %v)", err)
	}
}

// filesHolding lists the files under dir whose bytes contain needle.
func filesHolding(t *testing.T, dir, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, []byte(needle)) {
			rel, _ := filepath.Rel(dir, path)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return hits
}

// --- signed in ---------------------------------------------------------------

// THE CONTRACT: the stored session is gone, and the output says whose it was.
func TestLogout_EndsTheSessionAndNamesTheAccount(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t, "alice")

	// The control. If the seeded session were unreadable, "gone afterwards" would
	// hold for a reason that has nothing to do with the command.
	if who, err := runWhoami(t); err != nil || strings.TrimSpace(who) != "alice" {
		t.Fatalf("before logout whoami answers %q (%v); the seeded session is not a working one, "+
			"so nothing below proves anything", who, err)
	}

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("the output does not name the account that was logged out: %q", out)
	}
	if _, serr := os.Stat(sessionPath(home)); !os.IsNotExist(serr) {
		t.Errorf("the session file survived logout (stat: %v)", serr)
	}
	for _, secret := range []string{tokenFor("alice"), "refresh-alice"} {
		if strings.Contains(out, secret) {
			t.Errorf("logout printed a credential: %q", out)
		}
	}
}

// The refresh token goes with the session. It is the credential that outlives
// the access token, so a logout that left it behind would leave the account
// reachable from this machine.
//
// The first assertion is the control: it shows the walk below finds the token
// when it is there, so finding nothing afterwards means something.
func TestLogout_TakesTheRefreshTokenWithIt(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t, "alice")
	dot := filepath.Join(home, ".drift")

	if hits := filesHolding(t, dot, "refresh-alice"); len(hits) != 1 || hits[0] != "session.json" {
		t.Fatalf("before logout the refresh token is held by %v, want session.json alone", hits)
	}

	if out, err := runLogout(t); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}

	for _, secret := range []string{"refresh-alice", tokenFor("alice")} {
		if hits := filesHolding(t, home, secret); len(hits) != 0 {
			t.Errorf("after logout %q is still on disk in %v", secret, hits)
		}
	}
}

// After logout, whoami reports that nobody is logged in: an error and an empty
// stdout, never an empty line a script would read as a username.
func TestLogout_ThenWhoamiReportsNobody(t *testing.T) {
	t.Skip("fails until the logout command exists")

	logoutSession(t, "alice")
	if who, err := runWhoami(t); err != nil || strings.TrimSpace(who) != "alice" {
		t.Fatalf("before logout whoami answers %q (%v), want alice", who, err)
	}

	if out, err := runLogout(t); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}

	who, err := runWhoami(t)
	if err == nil {
		t.Fatalf("whoami still names %q after logout", who)
	}
	if who != "" {
		t.Errorf("whoami printed %q on stdout with nobody logged in", who)
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("whoami says %q, want it to say nobody is logged in", err)
	}
}

// After logout, a command that needs a session says to log in, with the remedy
// the error registry holds for it: `drift account login`, three words.
func TestLogout_ThenACommandThatNeedsASessionSaysToLogIn(t *testing.T) {
	t.Skip("fails until the logout command exists")

	logoutSession(t, "alice")
	const target = "http://127.0.0.1:1/never-sent"

	// The control: with the session in place the request is built.
	if _, err := common.NewAuthenticatedRequest("GET", target, nil); err != nil {
		t.Fatalf("before logout an authenticated request cannot be built: %v", err)
	}

	if out, err := runLogout(t); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}

	_, err := common.NewAuthenticatedRequest("GET", target, nil)
	if err == nil {
		t.Fatal("an authenticated request is still built after logout")
	}
	if !strings.Contains(err.Error(), "drift account login") {
		t.Fatalf("the refusal does not say to log in: %q", err)
	}

	// The refusal is the registered failure for "no session on this machine",
	// and the three-word rule is held on that registry: its remedy is the
	// command the refusal tells the user to type.
	code, ok := common.LookupErrorCode("DRIFT-1011")
	if !ok {
		t.Fatal("DRIFT-1011 is not registered")
	}
	if code.Command != "drift account login" {
		t.Errorf("DRIFT-1011's remedy is %q, want `drift account login`", code.Command)
	}
	if n := len(strings.Fields(code.Command)); n > 3 {
		t.Errorf("DRIFT-1011's remedy %q is %d words; the rule is three", code.Command, n)
	}
	if !strings.Contains(err.Error(), "DRIFT-1011") {
		t.Errorf("the refusal does not carry its code: %q", err)
	}
}

// --- several accounts --------------------------------------------------------

// Logging out ends the CURRENT account and nobody else. The other accounts keep
// their sessions, and the output says who is still logged in, because whoami
// answers with one of them afterwards.
func TestLogout_EndsTheCurrentAccountAndLeavesTheOthers(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t, "alice", "bob") // bob is current

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Logged out of bob") {
		t.Errorf("the output does not say bob was logged out: %q", out)
	}
	if !strings.Contains(out, "Still logged in: alice") {
		t.Errorf("the output does not say alice is still logged in: %q", out)
	}

	raw, rerr := os.ReadFile(sessionPath(home))
	if rerr != nil {
		t.Fatalf("alice's session went with bob's: %v", rerr)
	}
	if bytes.Contains(raw, []byte("refresh-bob")) || bytes.Contains(raw, []byte(tokenFor("bob"))) {
		t.Errorf("bob's credentials are still in the session file:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("refresh-alice")) {
		t.Errorf("alice's refresh token is gone from the session file:\n%s", raw)
	}
	if who, werr := runWhoami(t); werr != nil || strings.TrimSpace(who) != "alice" {
		t.Errorf("whoami after bob logged out says %q (%v), want alice", who, werr)
	}
}

// A profile is filed under the PERSON, so a member is told their own name and
// not the owner's account they act on.
func TestLogout_NamesAMemberByTheirOwnName(t *testing.T) {
	t.Skip("fails until the logout command exists")

	logoutSession(t)
	if err := common.SaveSession(memberTokenFor("isrand", "erica"), "refresh-erica"); err != nil {
		t.Fatalf("seeding the member's session: %v", err)
	}

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Logged out of erica") {
		t.Errorf("the output does not name erica: %q", out)
	}
	if strings.Contains(out, "isrand") {
		t.Errorf("the output names the owner, who is not the one logged out: %q", out)
	}
}

// DRIFT_ACCOUNT names a profile that is not logged in: there is nothing to end,
// and the account that IS logged in is left alone.
func TestLogout_AnUnknownDriftAccountEndsNothing(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t, "alice")
	t.Setenv(common.AccountEnv, "ghost")

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("the output does not say alice is the one logged in: %q", out)
	}
	if hits := filesHolding(t, home, "refresh-alice"); len(hits) != 1 {
		t.Errorf("alice's session did not survive a logout aimed at ghost: held by %v", hits)
	}
}

// --- nobody logged in --------------------------------------------------------

// With no session, logout says so and exits 0. It must not create a file to
// have something to remove.
func TestLogout_WithNobodyLoggedInSaysSoAndSucceeds(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t)

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout with nobody logged in is not an error, got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nobody is logged in") {
		t.Errorf("the output does not say nobody was logged in: %q", out)
	}
	if _, serr := os.Stat(sessionPath(home)); !os.IsNotExist(serr) {
		t.Errorf("logout left a session file behind (stat: %v)", serr)
	}
}

// Running it twice is harmless: the first names the account, the second says
// nobody is left. Both exit 0.
func TestLogout_TwiceIsHarmless(t *testing.T) {
	t.Skip("fails until the logout command exists")

	logoutSession(t, "alice")

	first, err := runLogout(t)
	if err != nil || !strings.Contains(first, "Logged out of alice") {
		t.Fatalf("first logout: %q (%v), want it to log alice out", first, err)
	}
	second, err := runLogout(t)
	if err != nil {
		t.Fatalf("second logout: %v\n%s", err, second)
	}
	if !strings.Contains(second, "Nobody is logged in") || strings.Contains(second, "alice") {
		t.Errorf("second logout: %q, want it to say nobody is logged in", second)
	}
}

// A file with the right shape and no accounts in it is a session in name only.
// It is removed, so "no session" stays a missing file.
func TestLogout_AFileHoldingNoAccountsIsRemoved(t *testing.T) {
	t.Skip("fails until the logout command exists")

	home := logoutSession(t)
	writeSessionFile(t, home, []byte(`{"current":"","accounts":{}}`))

	out, err := runLogout(t)
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nobody is logged in") {
		t.Errorf("the output does not say nobody was logged in: %q", out)
	}
	if _, serr := os.Stat(sessionPath(home)); !os.IsNotExist(serr) {
		t.Errorf("the empty session file survived (stat: %v)", serr)
	}
}

// --- a corrupt session file --------------------------------------------------

// A session file the CLI cannot read is removed, and logout succeeds saying so.
//
// The alternative, refusing and leaving the file, strands the user: every other
// command already treats an unreadable file as "not logged in", and the one
// command meant to clear the session would then be the one that cannot. Nothing
// is lost by removing it, because credentials the CLI cannot read are
// credentials it cannot use.
func TestLogout_ACorruptSessionFileIsRemovedNotAnError(t *testing.T) {
	t.Skip("fails until the logout command exists")

	cases := []struct {
		name string
		body []byte
	}{
		{"truncated JSON", []byte(`{"current":"alice","accounts":{"alice":{"token":`)},
		{"an empty file", nil},
		{"not JSON at all", []byte("\x00\x01 this is not a session")},
		{"JSON of the wrong shape", []byte(`["alice"]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := logoutSession(t)
			writeSessionFile(t, home, tc.body)

			out, err := runLogout(t)
			if err != nil {
				t.Fatalf("a corrupt session file is cleared, not an error: %v\n%s", err, out)
			}
			if !strings.Contains(out, "could not be read") || !strings.Contains(out, "removed") {
				t.Errorf("the output does not say the file was unreadable and removed: %q", out)
			}
			if !strings.Contains(out, "Nobody is logged in") {
				t.Errorf("the output does not say nobody is logged in: %q", out)
			}
			if _, serr := os.Stat(sessionPath(home)); !os.IsNotExist(serr) {
				t.Errorf("the corrupt session file survived (stat: %v)", serr)
			}
			if _, werr := runWhoami(t); werr == nil {
				t.Error("whoami names somebody after the corrupt file was cleared")
			}
		})
	}
}

// --- what it leaves alone ----------------------------------------------------

// The device id names this machine, not an account. Logging out and back in has
// to bind the next refresh token to the same machine, so logout never touches it.
func TestLogout_LeavesTheDeviceIdAlone(t *testing.T) {
	t.Skip("fails until the logout command exists")

	cases := []struct {
		name     string
		accounts []string
	}{
		{"with an account to log out of", []string{"alice"}},
		{"with nobody logged in", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := logoutSession(t, tc.accounts...)
			id := common.GetOrCreateDeviceID()
			if id == "" {
				t.Fatal("no device id was created to protect")
			}
			path := filepath.Join(home, ".drift", "device_id")

			if out, err := runLogout(t); err != nil {
				t.Fatalf("logout: %v\n%s", err, out)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the device id file is gone after logout: %v", err)
			}
			if strings.TrimSpace(string(got)) != id {
				t.Errorf("the device id changed from %q to %q", id, got)
			}
		})
	}
}

// DRIFT_TOKEN is a credential in the environment, not a session, and logout
// cannot unset a variable in the caller's shell. It says so rather than
// reporting a clean logout while commands keep acting as that token, and it never
// prints the token.
func TestLogout_SaysWhenAPersonalAccessTokenStillApplies(t *testing.T) {
	t.Skip("fails until the logout command exists")

	const pat = "drift_pat_0123456789abcdef"

	for _, tc := range []struct {
		name     string
		accounts []string
	}{
		{"after logging an account out", []string{"alice"}},
		{"with nobody logged in", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logoutSession(t, tc.accounts...)
			t.Setenv(common.TokenEnv, pat)

			out, err := runLogout(t)
			if err != nil {
				t.Fatalf("logout: %v\n%s", err, out)
			}
			if !strings.Contains(out, common.TokenEnv) {
				t.Errorf("the output does not mention %s: %q", common.TokenEnv, out)
			}
			if strings.Contains(out, pat) {
				t.Errorf("logout printed the personal access token: %q", out)
			}
		})
	}
}

// And without one, it says nothing about tokens.
func TestLogout_SaysNothingOfTokensWhenNoneIsSet(t *testing.T) {
	t.Skip("fails until the logout command exists")

	logoutSession(t, "alice")

	out, err := runLogout(t)
	if err != nil || !strings.Contains(out, "Logged out of alice") {
		t.Fatalf("logout: %q (%v), want it to log alice out", out, err)
	}
	if strings.Contains(out, common.TokenEnv) {
		t.Errorf("the output mentions %s though none is set: %q", common.TokenEnv, out)
	}
}

// --- what it does not take ---------------------------------------------------

// There is no --all, and an argument is refused rather than ignored: ignoring
// `drift account logout bob` would log out whichever account is current.
func TestLogout_TakesNoFlagsAndNoArguments(t *testing.T) {
	t.Skip("fails until the logout command exists")

	requireLogout(t)

	found, _, _ := GetAccountCmd().Find([]string{"logout"})
	if found.Flags().Lookup("all") != nil {
		t.Error("logout has an --all flag")
	}

	for _, args := range [][]string{{"--all"}, {"alice"}} {
		home := logoutSession(t, "alice", "bob")

		out, err := runLogout(t, args...)
		if err == nil {
			t.Errorf("logout %v was accepted:\n%s", args, out)
		}
		raw, rerr := os.ReadFile(sessionPath(home))
		if rerr != nil {
			t.Fatalf("a refused logout %v removed the session file: %v", args, rerr)
		}
		for _, want := range []string{"refresh-alice", "refresh-bob"} {
			if !bytes.Contains(raw, []byte(want)) {
				t.Errorf("a refused logout %v removed %s", args, want)
			}
		}
	}
}
