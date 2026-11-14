package slice

import (
	"encoding/json"
	"strings"
	"testing"
)

// The offer view is the platform's wire shape, written by the operator
// (routes/slice_transfer.go, transferView) and decoded here. A renamed field
// would decode to its zero value with the endpoint answering perfectly — a price
// of nothing, an empty sender — on the screen a person reads before accepting a
// bill. This body is that shape, field for field.
func TestTransferOffer_DecodesThePlatformsView(t *testing.T) {
	const body = `{"offers":[{"id":"0123456789abcdef0123456789abcdef","slice":"shop","from":"alice","to":"bob",` +
		`"status":"pending","direction":"incoming","host":"bob-shop.ondrift.eu",` +
		`"previous_host":"alice-shop.ondrift.eu","redirect_days":30,` +
		`"created_at":"2026-10-03T10:00:00Z","expires_at":"2026-10-10T10:00:00Z",` +
		`"terms":{"tier":"custom","monthly_cost_cents":900,"billing_period_months":12,` +
		`"paid_until":"2027-01-01T00:00:00Z","config":{"atomic":{"functions":[{},{}]}}}}]}`

	var got struct {
		Offers []transferOffer `json:"offers"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	o := got.Offers[0]
	if o.Slice != "shop" || o.From != "alice" || o.Host != "bob-shop.ondrift.eu" || o.Direction != "incoming" {
		t.Errorf("offer = %+v", o)
	}
	if o.PreviousHost != "alice-shop.ondrift.eu" || o.RedirectDays != 30 {
		t.Errorf("address move = %q for %d days — the screens name the old address and its redirect from these",
			o.PreviousHost, o.RedirectDays)
	}
	if o.Terms.MonthlyCostCents != 900 || o.Terms.BillingPeriodMonths != 12 || o.Terms.PaidUntil == nil {
		t.Errorf("terms = %+v — the price and the prepaid period are what an accept acknowledges", o.Terms)
	}
}

// A redirect window is said in days: the platform's figure when it sent one, the
// documented one when it did not.
func TestRedirectWindow_IsThePlatformsFigureOrTheDocumentedOne(t *testing.T) {
	for days, want := range map[int]string{30: "30 days", 14: "14 days", 1: "1 day", 0: "30 days", -1: "30 days"} {
		if got := redirectWindow(days); got != want {
			t.Errorf("redirectWindow(%d) = %q, want %q", days, got, want)
		}
	}
}

func offersFixture() []transferOffer {
	mk := func(id, slice, from, dir, status string) transferOffer {
		return transferOffer{ID: id, Slice: slice, From: from, Direction: dir, Status: status}
	}
	return []transferOffer{
		mk(strings.Repeat("a", 32), "shop", "alice", "incoming", "pending"),
		mk(strings.Repeat("b", 32), "blog", "carol", "incoming", "pending"),
		mk(strings.Repeat("c", 32), "blog", "dave", "incoming", "pending"),
		mk(strings.Repeat("d", 32), "notes", "erin", "incoming", "declined"),
		mk(strings.Repeat("e", 32), "shop", "me", "outgoing", "pending"),
	}
}

// A name resolves to the one open offer by that name in the caller's
// direction. Accepting `shop` must find alice's offer TO me, never my own
// offer of a different `shop` to somebody else.
func TestResolveOffer_ANameFindsTheOneOpenOfferInTheCallersDirection(t *testing.T) {
	o, err := resolveOffer(offersFixture(), "shop", "incoming")
	if err != nil || o.From != "alice" {
		t.Fatalf("got %+v, %v; want alice's offer", o, err)
	}
	if o, err := resolveOffer(offersFixture(), "shop", "outgoing"); err != nil || o.From != "me" {
		t.Errorf("cancelling `shop` found %+v, %v; want my own offer", o, err)
	}
}

// Two accounts offering a slice by the same name must not be guessed between:
// accepting the wrong one takes the wrong slice, and its bill, into the account.
func TestResolveOffer_TwoOffersByOneNameAreNotGuessedBetween(t *testing.T) {
	if o, err := resolveOffer(offersFixture(), "blog", "incoming"); err == nil {
		t.Errorf("resolved %+v from two offers named blog", o)
	}
	o, err := resolveOffer(offersFixture(), strings.Repeat("c", 32), "incoming")
	if err != nil || o.From != "dave" {
		t.Errorf("by id: got %+v, %v; want dave's", o, err)
	}
}

// A decided offer is not found by name: declining or accepting one again would
// only meet the platform's refusal.
func TestResolveOffer_ADecidedOfferIsNotFoundByName(t *testing.T) {
	if o, err := resolveOffer(offersFixture(), "notes", "incoming"); err == nil {
		t.Errorf("resolved the declined offer %+v", o)
	}
}

// An id must be in the caller's direction too: my own outgoing offer's id is
// not one I can accept.
func TestResolveOffer_AnIdOutsideTheDirectionIsNotFound(t *testing.T) {
	if o, err := resolveOffer(offersFixture(), strings.Repeat("e", 32), "incoming"); err == nil {
		t.Errorf("accepted-side resolution found my own offer %+v", o)
	}
}

// The shape line counts what the slice declares, from the config the platform
// sent.
func TestShapeSummary_CountsTheDeclaredSets(t *testing.T) {
	raw := json.RawMessage(`{"atomic":{"functions":[{},{},{}]},"backbone":{"nosql":{"collections":{"a":1,"b":1}},` +
		`"blobs":{"buckets":{"x":1}},"sql":{"databases":{}},"queues":{"queues":{"q":1}}}}`)
	got := shapeSummary(raw)
	for _, want := range []string{"3 function(s)", "2 collection(s)", "1 bucket(s)", "1 queue(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "database") {
		t.Errorf("summary %q names an empty set", got)
	}
	if shapeSummary(nil) != "" || shapeSummary(json.RawMessage(`not json`)) != "" {
		t.Error("an absent or unreadable shape must print nothing rather than a guess")
	}
}
