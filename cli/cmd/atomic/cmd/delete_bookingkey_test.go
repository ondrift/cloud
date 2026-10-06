package atomic_cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	deploy "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// `method:route` is the ONE name a function has everywhere else — it is what the
// Driftfile declares, what functionBudgets renders, and what the
// undeclared-deploy refusal prints back at you. This command took the route and
// a --method flag, so pasting the name every other surface shows failed with
// "function not found": which reads as "that function is already gone", not
// "your name is in the wrong shape". A user then believes the delete worked.
func TestSplitBookingKey_AcceptsTheFormEveryOtherSurfacePrints(t *testing.T) {
	for _, c := range []struct{ in, route, method string }{
		{"get:groups/id", "groups/id", "get"},
		{"post:send-email", "send-email", "post"},
		{"DELETE:users", "users", "delete"},
		{"Put:things/1", "things/1", "put"},
		{"patch:a/b/c", "a/b/c", "patch"},
		{"head:health", "health", "head"},
		{"options:cors", "cors", "options"},
		// The two methods no HTTP request carries, and the help promises both.
		// `queue:txs` used to travel whole as a route and answer "function not
		// found", while `delete txs --method queue` worked.
		{"queue:txs", "txs", "queue"},
		{"QUEUE:orders.v2", "orders.v2", "queue"},
		{"field:sequencer", "sequencer", "field"},
	} {
		route, method, split := splitBookingKey(c.in)
		if !split {
			t.Errorf("%q was not recognised as a booking key", c.in)
			continue
		}
		if route != c.route || method != c.method {
			t.Errorf("%q split to (%q, %q), want (%q, %q)", c.in, route, method, c.route, c.method)
		}
	}
}

// EVERY method a slot can be booked under splits, so the list the parser reads
// and the list the platform books cannot drift apart one method at a time.
func TestSplitBookingKey_SplitsEverySlotMethod(t *testing.T) {
	for _, m := range deploy.SlotMethods {
		route, method, split := splitBookingKey(m + ":x")
		if !split || route != "x" || method != m {
			t.Errorf("%s:x split to (%q, %q, %v), want (\"x\", %q, true)", m, route, method, split, m)
		}
	}
}

// The command itself, end to end: `delete queue:txs` reaches the operator as
// name=txs&method=queue, which is what `delete txs --method queue` sends.
func TestDelete_TheBookingKeyFormReachesTheOperatorSplit(t *testing.T) {
	for _, c := range []struct {
		args       []string
		name, meth string
	}{
		{[]string{"queue:txs"}, "txs", "queue"},
		{[]string{"txs", "--method", "queue"}, "txs", "queue"},
		{[]string{"field:sequencer"}, "sequencer", "field"},
		{[]string{"post:send-email"}, "send-email", "post"},
	} {
		var got []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ops/atomic/delete" {
				got = append(got, r.URL.Query().Get("name")+" "+r.URL.Query().Get("method"))
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Setenv("HOME", t.TempDir())
		if err := common.SaveSession("test-token", "test-refresh"); err != nil {
			t.Fatal(err)
		}
		prev := common.APIBaseURL
		common.APIBaseURL = srv.URL

		cmd := Delete()
		cmd.SetArgs(c.args)
		cmd.SetOut(new(strings.Builder))
		cmd.SetErr(new(strings.Builder))
		err := cmd.Execute()
		common.APIBaseURL = prev
		srv.Close()

		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if want := c.name + " " + c.meth; len(got) != 1 || got[0] != want {
			t.Errorf("%v sent %q, want one call with name and method %q", c.args, got, want)
		}
	}
}

// A ROUTE MAY CONTAIN A COLON. `users/:id` is a path parameter in several
// grammars and a blob key can carry one, so splitting on any colon would mangle
// a name that was already correct — turning a working delete into a 404, which
// is the same failure this change exists to remove, pointed the other way.
//
// Only a KNOWN HTTP method splits. Everything else travels as the route it is.
func TestSplitBookingKey_LeavesAColonThatIsNotAMethodAlone(t *testing.T) {
	for _, name := range []string{
		"users/:id",
		"things/:key/sub",
		"notamethod:x",
		"send-email",
		"a:b:c",
		":leading",
		"trailing:",
	} {
		route, method, split := splitBookingKey(name)
		if split {
			t.Errorf("%q was split into (%q, %q) — it names no HTTP method", name, route, method)
		}
		if route != name || method != "" {
			t.Errorf("%q was altered to (%q, %q); an unrecognised prefix must pass through untouched", name, route, method)
		}
	}
}
