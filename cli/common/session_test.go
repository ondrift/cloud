package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// PLM-81: writeSession used to open the target with O_TRUNC and write into
// it in place, so two `drift` commands saving at nearly the same instant —
// a login completing in one shell while `slice use` saves in another —
// could corrupt the file: both truncate before either writes, and if the
// SHORTER payload's write lands after the LONGER one's, nothing truncates
// away the longer write's trailing bytes, leaving a dangling, invalid-JSON
// suffix. Nothing distinguishes that from a session nobody ever created
// (PLM-82), so every later command would silently read "not logged in".
//
// THE OLD CODE DOES NOT RELIABLY FAIL THIS TEST, and that is worth stating
// rather than hiding: the corruption needs BOTH writers to have already
// truncated the shared target before EITHER writes, which — called through
// the ordinary two-goroutine `go writeSession(...)` shape below — usually
// does not happen on a fast local filesystem, where one call's open, write
// and close complete before the other's open is even scheduled. Forcing the
// interleaving deterministically was confirmed separately, against a
// hand-instrumented replica of the old open-then-write shape with an
// explicit barrier between the two steps (200/200 runs corrupted) — proving
// the vulnerability is real — but that harness does not exercise
// writeSession itself, so it is not what is pinned here.
//
// What THIS test verifies, and can prove reliably: writeSession's own
// atomicity guarantee holds under real concurrent load — many writers of
// deliberately different sizes, racing with no artificial synchronization —
// which is the shape every future regression will actually be exercised
// against. Run with -race, which would also catch a Go-level data race in
// the implementation itself, though the corruption this exists for is an
// OS/filesystem-level property no Go-level race exists to catch.
func TestWriteSession_ConcurrentWritesNeverCorruptTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("writer-%d", i)
			accounts := map[string]AccountProfile{}
			// Deliberately varying size per writer — a uniform payload never
			// exercises the shape that actually corrupted the old code.
			for j := 0; j < i; j++ {
				accounts[fmt.Sprintf("%s-%d", name, j)] = AccountProfile{
					Token: strings.Repeat("x", 100), RefreshToken: strings.Repeat("y", 100),
				}
			}
			_ = writeSession(sessionFile{Current: name, Accounts: accounts})
		}(i)
	}
	wg.Wait()

	if _, err := readSession(); err != nil {
		t.Fatalf("the file on disk did not parse as valid JSON after %d concurrent writers of "+
			"different sizes: %v", n, err)
	}
}

// The write-then-rename mechanism's own hygiene: a successful write leaves
// no ".session-*.json.tmp" behind. Deterministic, unlike the concurrency
// test above — this needs no race to check, only that the implementation
// actually does what its own comment claims.
func TestWriteSession_LeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveSession("tok", "ref"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, ".drift"))
	if err != nil {
		t.Fatalf("reading ~/.drift: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

// TestWriteSession_ReplacesTheInode is the deterministic pin for PLM-81 — no
// timing or race needed, unlike the concurrency test above.
//
// The old implementation opened the EXISTING path with O_TRUNC and wrote
// into it, keeping the same inode throughout — which is exactly what let two
// such writers corrupt each other: both were free to truncate and write the
// SAME inode in any order. write-then-rename never touches that inode again;
// it builds the new content under a different name and swaps the directory
// entry atomically, so a second write is always a full replacement, never an
// in-place mutation racing another one.
//
// This is the one property here that genuinely fails against the pre-fix
// code and passes against the fix, with no timing luck involved either way.
func TestWriteSession_ReplacesTheInode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveSession("tok1", "ref1"); err != nil {
		t.Fatalf("first SaveSession: %v", err)
	}
	path := filepath.Join(dir, ".drift", "session.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeIno := before.Sys().(*syscall.Stat_t).Ino

	if err := SaveSession("tok2", "ref2"); err != nil {
		t.Fatalf("second SaveSession: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	afterIno := after.Sys().(*syscall.Stat_t).Ino

	if beforeIno == afterIno {
		t.Errorf("session.json kept inode %d across a second write — an in-place truncate+write, "+
			"not a rename, which is exactly the shape that let two concurrent writers corrupt "+
			"each other (PLM-81)", beforeIno)
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
	if err := SaveActiveSlice("erica-slice"); err != nil {
		t.Fatalf("slice erica: %v", err)
	}
	if err := SaveSession(memberToken("isrand", "sam"), "ref-s"); err != nil {
		t.Fatalf("login sam: %v", err)
	}
	if err := SaveActiveSlice("sam-slice"); err != nil {
		t.Fatalf("slice sam: %v", err)
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
	if got := GetActiveSlice(); got != "erica-slice" {
		t.Errorf("erica's active slice is %q — the two members share one", got)
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
