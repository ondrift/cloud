package atomic_cmd

import "testing"

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
