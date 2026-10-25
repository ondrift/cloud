package slice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// stubLinkAPI points the CLI at a test server and seeds a session whose active
// slice is "app", so no request reaches a real platform or a real ~/.drift.
func stubLinkAPI(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.SliceEnv, "app")
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
}

// The help points at the declarative way, so a reader of `--help` learns that
// a Driftfile owns the list.
func TestLink_HelpPointsAtEdgeLinks(t *testing.T) {
	if !strings.Contains(getLinkCmd().Long, "`links` in its edge block") || !strings.Contains(getLinkCmd().Long, "`drift apply`") {
		t.Errorf("`drift slice link --help` does not point at the Driftfile's links and `drift apply`:\n%s", getLinkCmd().Long)
	}
}

// A Driftfile's `links` is the only way to change a slice's links, so the
// command only reads them.
func TestLink_OnlyListsWhatIsLive(t *testing.T) {
	var names []string
	for _, c := range getLinkCmd().Commands() {
		names = append(names, c.Name())
	}
	if strings.Join(names, ",") != "list" {
		t.Errorf("`drift slice link` has subcommands %v, want only list", names)
	}
}

// The calls name the slice they act on, so a reconcile for one slice cannot
// land on whichever slice this machine last used.
func TestLinkCalls_NameTheirSlice(t *testing.T) {
	var got []string
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.Header.Get("X-Slice"))
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})

	if _, err := AddLink("shop", "billing"); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	if _, err := ListLinks("shop"); err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if err := RemoveLink("shop", "billing"); err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	want := []string{"POST shop", "GET shop", "DELETE shop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", got, want)
	}
}
