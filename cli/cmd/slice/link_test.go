package slice

import (
	"bytes"
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

func runLinkCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := getLinkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// The help points at the declarative way, so a reader of `--help` learns that
// a Driftfile can own the list.
func TestLink_HelpPointsAtEdgeLinks(t *testing.T) {
	if !strings.Contains(getLinkCmd().Long, "edge.links") {
		t.Errorf("`drift slice link --help` does not mention edge.links:\n%s", getLinkCmd().Long)
	}
	if strings.Contains(getLinkCmd().Long, "reachable from your functions; everything") {
		t.Error("the help still carries the old wording")
	}
}

// `add` promises no identity. X-Drift-Slice is asserted by the caller and
// authorises nothing, so telling a user the callee "sees your slice as" it
// invites them to trust it.
func TestLinkAdd_PromisesNoCallerIdentity(t *testing.T) {
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/ops/slice/link" {
			_, _ = w.Write([]byte(`{"target":"billing","target_host":"alice-billing.ondrift.eu"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	out, err := runLinkCmd(t, "add", "billing")
	if err != nil {
		t.Fatalf("link add: %v", err)
	}
	for _, forbidden := range []string{"X-Drift-Slice", "sees your slice"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("`link add` still claims a caller identity (%q):\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, `drift.Edge.Slice("billing")`) {
		t.Errorf("`link add` does not show the SDK call a function makes:\n%s", out)
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

// A refused removal is an error. It used to print nothing and exit 0, so a
// link the platform kept read as removed.
func TestLinkRemove_ReportsARefusal(t *testing.T) {
	stubLinkAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no slice named 'ghost' in your account (links are same-user only)"}`))
	})

	out, err := runLinkCmd(t, "remove", "ghost")
	if err == nil {
		t.Fatalf("a refused removal returned no error; printed:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no slice named 'ghost' in your account") {
		t.Errorf("the platform's reason did not reach the user: %v", err)
	}
	if strings.Contains(out, "Unlinked") {
		t.Errorf("a refused removal printed success:\n%s", out)
	}
}
