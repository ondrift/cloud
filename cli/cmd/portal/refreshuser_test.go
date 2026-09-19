package portal

import (
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The header is a CACHE of who the portal is running as, and every fetch
// already re-reads the session file's current account on each call
// (newAuthenticatedRequestCtx). refreshUser is what keeps the cache from
// lagging behind that: a `drift account login` as a different account in
// another terminal on the same machine must be reflected in the header
// (CNV-65), not just in which slices/functions the next fetch happens to
// return.
func TestRefreshUser_PicksUpAnAccountChangedInAnotherTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession(fakeAccessToken(t), "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	m := &model{user: "someone-else"} // the account this portal launched under

	m.refreshUser()

	if m.user != "testuser" {
		t.Errorf("refreshUser() left m.user = %q, want the session's current account %q", m.user, "testuser")
	}
}

// A momentary empty read (no session, no token — GetUsername's only empty
// case) must not blank a header that has a perfectly good last-known value.
// The portal cannot actually reach this state while running (ensureLoggedIn
// gates launch), but refreshUser's own contract should not depend on that.
func TestRefreshUser_LeavesTheLastKnownUserOnAnEmptyRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no session file at all

	m := &model{user: "alice"}

	m.refreshUser()

	if m.user != "alice" {
		t.Errorf("refreshUser() blanked m.user on an empty read, got %q", m.user)
	}
}
