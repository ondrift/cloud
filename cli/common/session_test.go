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

func TestSessionPreservesActiveSlice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok1", "ref1")
	SaveActiveSlice("myapp")

	// Re-saving session should preserve active_slice.
	SaveSession("tok2", "ref2")

	got := GetActiveSlice()
	if got != "myapp" {
		t.Fatalf("active slice lost after re-save: got %q", got)
	}
}

func TestActiveSliceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")
	SaveActiveSlice("staging")

	got := GetActiveSlice()
	if got != "staging" {
		t.Fatalf("active slice: got %q, want staging", got)
	}
}

func TestGetActiveSlice_NoSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	got := GetActiveSlice()
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRequireActiveSlice_Set(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")
	SaveActiveSlice("prod")

	s, err := RequireActiveSlice()
	if err != nil {
		t.Fatalf("RequireActiveSlice: %v", err)
	}
	if s != "prod" {
		t.Fatalf("got %q", s)
	}
}

func TestRequireActiveSlice_NotSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")

	_, err := RequireActiveSlice()
	if err == nil {
		t.Fatal("expected error when no active slice")
	}
}

// The file holds ACCOUNTS now, each with its own active slice.
//
// It used to be flat — one token, one refresh token, one active slice — and this
// test asserted that shape. The change is deliberate and the migration below is
// what makes it safe.
func TestSessionFileFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(AccountEnv, "")

	SaveSession("tok", "ref")
	if err := SaveActiveSlice("myslice"); err != nil {
		t.Fatalf("SaveActiveSlice: %v", err)
	}

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
	if p.Token != "tok" || p.RefreshToken != "ref" || p.ActiveSlice != "myslice" {
		t.Fatalf("profile round-tripped wrong: %+v", p)
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
	if got := GetActiveSlice(); got != "oldslice" {
		t.Errorf("the active slice did not survive migration: %q", got)
	}
	if got := CurrentAccount(); got != "alice" {
		t.Errorf("the migrated profile is named %q, want the token's own username", got)
	}
}

// Logging in as a second account ADDS rather than replaces, and each keeps its
// own slice. This is the bug the flat file had: one stored slice, shared by
// every account, pointing at a slice the current one may not own.
func TestASecondLoginAddsAnAccountAndKeepsEachSliceSeparate(t *testing.T) {
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
	if err := SaveActiveSlice("alice-slice"); err != nil {
		t.Fatalf("slice alice: %v", err)
	}
	if err := SaveSession(tokenFor("bob"), "ref-b"); err != nil {
		t.Fatalf("login bob: %v", err)
	}
	if err := SaveActiveSlice("bob-slice"); err != nil {
		t.Fatalf("slice bob: %v", err)
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
	if got := GetActiveSlice(); got != "bob-slice" {
		t.Errorf("bob's active slice is %q", got)
	}

	// THE assertion: switching back restores alice's OWN slice, rather than
	// leaving bob's selected against alice's account.
	if err := UseAccount("alice"); err != nil {
		t.Fatalf("UseAccount: %v", err)
	}
	if got := GetActiveSlice(); got != "alice-slice" {
		t.Errorf("after switching to alice the active slice is %q — that is the "+
			"cross-account leak profiles exist to fix", got)
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
	_ = SaveActiveSlice("alice-slice")
	_ = SaveSession(tokenFor("bob"), "ref-b")
	_ = SaveActiveSlice("bob-slice")

	t.Setenv(AccountEnv, "alice")
	if got := CurrentAccount(); got != "alice" {
		t.Errorf("%s did not take effect: %q", AccountEnv, got)
	}
	if got := GetActiveSlice(); got != "alice-slice" {
		t.Errorf("the override did not carry the account's own slice: %q", got)
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
