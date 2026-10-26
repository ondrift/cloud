package common

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath_Tilde(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	got, err := expandPath("~/test/file")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	want := filepath.Join(home, "test/file")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandPath_Absolute(t *testing.T) {
	got, err := expandPath("/etc/hosts")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if got != "/etc/hosts" {
		t.Fatalf("got %q, want /etc/hosts", got)
	}
}

func TestExpandPath_Relative(t *testing.T) {
	got, err := expandPath("relative/path")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if got != "relative/path" {
		t.Fatalf("got %q", got)
	}
}

// TestExpandPath_BareTilde regression-tests an index-out-of-range
// panic that the previous implementation hit on the literal string
// "~" — it sliced [:2] after only checking len(path) > 0. A
// public-source CLI shouldn't panic on any input shape.
func TestExpandPath_BareTilde(t *testing.T) {
	got, err := expandPath("~")
	if err != nil {
		t.Fatalf("expandPath(~): unexpected error: %v", err)
	}
	if got != "~" {
		t.Fatalf("expandPath(~) = %q, want %q", got, "~")
	}
}

// TestExpandPath_Empty exercises the same length-check edge.
func TestExpandPath_Empty(t *testing.T) {
	got, err := expandPath("")
	if err != nil {
		t.Fatalf("expandPath(\"\"): unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expandPath(\"\") = %q, want empty", got)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	// Override HOME to use a temp directory.
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveSession("tok123", "ref456"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	tok, ref, err := GetTokenFromSession()
	if err != nil {
		t.Fatalf("GetTokenFromSession: %v", err)
	}
	if tok != "tok123" {
		t.Fatalf("token: got %q", tok)
	}
	if ref != "ref456" {
		t.Fatalf("refresh: got %q", ref)
	}
}

// The file holds ACCOUNTS now, each with its credentials and nothing about a
// slice.
//
// It used to be flat — one token, one refresh token — and this test asserted
// that shape. The change is deliberate and the migration below is what makes it
// safe.
func TestSessionFileFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")

	SaveSession("tok", "ref")

	path := filepath.Join(dir, ".drift", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}

	var f struct {
		Current  string `json:"current"`
		Accounts map[string]struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
			ActiveSlice  string `json:"active_slice"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// "tok" carries no readable username claim, so it lands under "default" —
	// which is the documented fallback rather than an accident.
	p, ok := f.Accounts[f.Current]
	if !ok {
		t.Fatalf("the current account %q is not in the file: %s", f.Current, data)
	}
	if p.Token != "tok" || p.RefreshToken != "ref" {
		t.Fatalf("profile round-tripped wrong: %+v", p)
	}
	if p.ActiveSlice != "" {
		t.Fatalf("a slice was written to the session file: %q", p.ActiveSlice)
	}
}

// THE MIGRATION, and the reason the format change is safe: a flat file written
// by an older CLI keeps working, and nobody is logged out.
//
// The profile takes its name from the token's own `username` claim, so the
// account keeps the name its user knows it by rather than appearing as
// "default".
func TestAFlatSessionFileStillWorksAndKeepsItsName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")
	t.Setenv(SliceEnv, "")

	// A real-shaped JWT: header.payload.signature, payload naming alice.
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"alice","exp":9999999999}`))
	token := "h." + payload + ".s"

	if err := os.MkdirAll(filepath.Join(dir, ".drift"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	flat := `{"token":"` + token + `","refresh_token":"ref","active_slice":"oldslice"}`
	if err := os.WriteFile(filepath.Join(dir, ".drift", "session.json"), []byte(flat), 0o600); err != nil {
		t.Fatalf("seed flat file: %v", err)
	}

	gotTok, gotRef, err := GetTokenFromSession()
	if err != nil {
		t.Fatalf("a flat session file stopped working: %v", err)
	}
	if gotTok != token || gotRef != "ref" {
		t.Errorf("credentials did not survive migration: %q / %q", gotTok, gotRef)
	}
	if got := GetActiveSlice(); got != "" {
		t.Errorf("an old stored slice still decides the target after migration: %q", got)
	}
	if got := CurrentAccount(); got != "alice" {
		t.Errorf("the migrated profile is named %q, want the token's own username", got)
	}
}

// Logging in as a second account ADDS rather than replaces.
func TestASecondLoginAddsAnAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")
	t.Setenv(SliceEnv, "")

	tokenFor := func(user string) string {
		p := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"` + user + `","exp":9999999999}`))
		return "h." + p + ".s"
	}

	if err := SaveSession(tokenFor("alice"), "ref-a"); err != nil {
		t.Fatalf("login alice: %v", err)
	}
	if err := SaveSession(tokenFor("bob"), "ref-b"); err != nil {
		t.Fatalf("login bob: %v", err)
	}

	names, current, err := Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("got %v, want both accounts — the second login replaced the first", names)
	}
	if current != "bob" {
		t.Errorf("current is %q, want the account just logged in", current)
	}
	if err := UseAccount("alice"); err != nil {
		t.Fatalf("UseAccount: %v", err)
	}
	if _, ref, _ := GetTokenFromSession(); ref != "ref-a" {
		t.Errorf("switching account did not switch credentials: %q", ref)
	}
}

// DRIFT_ACCOUNT acts as another account for ONE command and writes nothing.
func TestTheAccountEnvironmentOverridesWithoutPersisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")
	t.Setenv(SliceEnv, "")

	tokenFor := func(user string) string {
		p := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"` + user + `","exp":9999999999}`))
		return "h." + p + ".s"
	}
	_ = SaveSession(tokenFor("alice"), "ref-a")
	_ = SaveSession(tokenFor("bob"), "ref-b")

	t.Setenv(AccountEnv, "alice")
	if got := CurrentAccount(); got != "alice" {
		t.Errorf("%s did not take effect: %q", AccountEnv, got)
	}

	// Nothing was written: with the variable gone, bob is still current.
	t.Setenv(AccountEnv, "")
	if got := CurrentAccount(); got != "bob" {
		t.Errorf("%s persisted a switch it must not have: current is %q", AccountEnv, got)
	}
}

// Deleting one account must not log the user out of the others.
func TestClearSessionRemovesOnlyTheCurrentAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")

	tokenFor := func(user string) string {
		p := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"` + user + `","exp":9999999999}`))
		return "h." + p + ".s"
	}
	_ = SaveSession(tokenFor("alice"), "ref-a")
	_ = SaveSession(tokenFor("bob"), "ref-b") // bob is current

	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	names, current, err := Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(names) != 1 || names[0] != "alice" {
		t.Fatalf("got %v, want alice alone — deleting one account logged out the others", names)
	}
	if current != "alice" {
		t.Errorf("current is %q, want the surviving account", current)
	}

	// And the last one out removes the file, so "no session" stays a missing
	// file rather than an empty object.
	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".drift", "session.json")); !os.IsNotExist(err) {
		t.Error("the session file survived the last account being cleared")
	}
}

// --- a member's profile ------------------------------------------------------

// memberToken is the shape a MEMBER's session has: `username` names the OWNER —
// which is what makes their commands reach the owner's slices — and `actor`
// names them.
func memberToken(owner, actor string) string {
	p := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"username":"` + owner + `","actor":"` + actor + `","exp":9999999999}`))
	return "h." + p + ".s"
}

// A PROFILE IS NAMED AFTER THE PERSON. Naming it from `username` would file
// Erica's credentials under "isrand" — the account she works on, and a name
// belonging to somebody who is not logged in on this machine.
func TestAMembersProfileIsNamedAfterThemAndNotTheOwner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")
	t.Setenv(SliceEnv, "")

	if err := SaveSession(memberToken("isrand", "erica"), "ref-e"); err != nil {
		t.Fatalf("login erica: %v", err)
	}

	if got := CurrentAccount(); got != "erica" {
		t.Errorf("the profile is named %q, want \"erica\" — `drift account use isrand` "+
			"would name an account that is not logged in here", got)
	}
	// And the ACCOUNT is still the owner: that is what the commands act on.
	if got := GetUsername(); got != "isrand" {
		t.Errorf("GetUsername() = %q, want \"isrand\" — a member acts on the owner's account", got)
	}
	if got := ActorFromToken(); got != "erica" {
		t.Errorf("ActorFromToken() = %q, want \"erica\"", got)
	}
}

// THE COLLISION. Two members of the same account, on one machine. Named from
// `username` they land in the SAME profile and silently overwrite each other —
// each login reporting success, and the second one taking the first's slice.
func TestTwoMembersOfOneAccountDoNotOverwriteEachOther(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")
	t.Setenv(SliceEnv, "")

	if err := SaveSession(memberToken("isrand", "erica"), "ref-e"); err != nil {
		t.Fatalf("login erica: %v", err)
	}
	if err := SaveSession(memberToken("isrand", "sam"), "ref-s"); err != nil {
		t.Fatalf("login sam: %v", err)
	}

	names, current, err := Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("got %v, want both members — the second login overwrote the first, and "+
			"said nothing", names)
	}
	if current != "sam" {
		t.Errorf("current is %q, want the member who just logged in", current)
	}

	if err := UseAccount("erica"); err != nil {
		t.Fatalf("UseAccount(erica): %v", err)
	}
	if _, ref, _ := GetTokenFromSession(); ref != "ref-e" {
		t.Errorf("switching to erica produced %q — the two members share credentials", ref)
	}
}

// An OWNER's profile is unaffected: with no actor claim the two names are the
// same string, which is every session that exists today.
func TestAnOwnersProfileIsStillNamedFromTheUsernameClaim(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")

	p := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"alice","exp":9999999999}`))
	if err := SaveSession("h."+p+".s", "ref-a"); err != nil {
		t.Fatalf("login alice: %v", err)
	}
	if got := CurrentAccount(); got != "alice" {
		t.Errorf("an owner's profile is named %q, want \"alice\"", got)
	}
	if got := ActorFromToken(); got != "" {
		t.Errorf("an owner's session reports an actor: %q", got)
	}
}
