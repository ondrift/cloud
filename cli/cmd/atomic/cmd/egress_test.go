package atomic_cmd

import "testing"

// `drift atomic egress test` — the diagnostic that answers "can this slice
// reach that host?".
//
// It had no test at all, while `matchHostAgainstList`'s own comment claimed one
// covered it. That mattered more than an ordinary coverage gap, because the
// branch it lacked a test for was one that CONFIRMED a control that did not
// exist.

// A WILDCARD ENTRY MATCHES NOTHING, and this is the whole reason this file
// exists.
//
// The matcher used to read a leading `*.` as a suffix match, so this printed
// "✔ s3.amazonaws.com matches "*.amazonaws.com" on the allowlist" — for a rule
// the platform had never created. The operator resolves each entry to an
// `<ip>/32` NetworkPolicy rule and skips a wildcard with a log line, so a
// wildcard is no rule at all.
//
// A diagnostic that confirms a control which does not exist is worse than no
// diagnostic: it is the reason somebody stops checking.
func TestMatchHostAgainstList_AWildcardConfirmsNothing(t *testing.T) {
	for _, target := range []string{
		"s3.amazonaws.com",
		"deep.nested.amazonaws.com",
		"amazonaws.com",
	} {
		if got := matchHostAgainstList(target, []string{"*.amazonaws.com"}); got != "" {
			t.Errorf("%s matched %q, confirming an allowlist rule that cannot be rendered", target, got)
		}
	}
}

// An exact host matches. The control: without it every assertion above passes
// against a matcher that matches nothing at all.
func TestMatchHostAgainstList_AnExactHostMatches(t *testing.T) {
	list := []string{"api.stripe.com", "api.github.com"}
	if got := matchHostAgainstList("api.stripe.com", list); got != "api.stripe.com" {
		t.Errorf("got %q, want the declared entry", got)
	}
	if got := matchHostAgainstList("api.github.com", list); got != "api.github.com" {
		t.Errorf("got %q, want the declared entry", got)
	}
}

// A host nobody declared does not match, and a near-miss is a near-miss. A
// suffix or prefix relationship is not membership — that was the wildcard
// branch's mistake, and removing it must not leave a looser rule behind.
func TestMatchHostAgainstList_ANearMissIsNotAMatch(t *testing.T) {
	list := []string{"api.stripe.com"}
	for _, target := range []string{
		"evil-api.stripe.com.attacker.example",
		"stripe.com",
		"api.stripe.com.evil.example",
		"pi.stripe.com",
		"",
	} {
		if got := matchHostAgainstList(target, list); got != "" {
			t.Errorf("%q matched %q; only the declared host itself is on the list", target, got)
		}
	}
}

// A port on either side is stripped before comparison, so a slice declaring
// `smtp.sendgrid.net:587` answers for a probe of the bare host and the reverse.
// The entry is returned WITH its port, because that is what the tenant wrote
// and what they would go and edit.
func TestMatchHostAgainstList_PortsAreStrippedOnBothSides(t *testing.T) {
	list := []string{"smtp.sendgrid.net:587"}
	if got := matchHostAgainstList("smtp.sendgrid.net", list); got != "smtp.sendgrid.net:587" {
		t.Errorf("a bare probe against a ported entry got %q, want the entry as declared", got)
	}
	if got := matchHostAgainstList("smtp.sendgrid.net:25", list); got != "smtp.sendgrid.net:587" {
		t.Errorf("a ported probe got %q, want the entry as declared", got)
	}
}

// Case and surrounding whitespace do not decide reachability. A tenant who
// typed `  API.Stripe.com ` into their Driftfile declared the same host.
func TestMatchHostAgainstList_IsCaseAndWhitespaceInsensitive(t *testing.T) {
	if got := matchHostAgainstList("API.Stripe.com", []string{"  api.stripe.com "}); got == "" {
		t.Error("a differently-cased host reported as not on the allowlist")
	}
}

// An empty allowlist matches nothing. `mode: allowlist` with no hosts is a
// legitimate declaration meaning "this slice talks to nothing", and the
// diagnostic must say so rather than falling through to a match.
func TestMatchHostAgainstList_AnEmptyListMatchesNothing(t *testing.T) {
	if got := matchHostAgainstList("api.stripe.com", nil); got != "" {
		t.Errorf("an empty allowlist matched %q", got)
	}
}
