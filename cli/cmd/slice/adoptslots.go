package slice

// adoptslots.go — renaming a slot the slice holds and nothing uses.
//
// # A slot that exists under another name
//
// A fresh free slice holds five function slots named `get:slot-1` … `get:slot-5`
// — placeholders, in the platform's own vocabulary, for a tenant to type over.
// When the deploy's reference reconcile finds a slot the Driftfile names that
// the slice does not hold, and an idle placeholder of the right size, the slot
// EXISTS, doing nothing: only the name differs, and renaming is free.
//
// # Why this is allowed to skip the form, and why it may not skip the PERSON
//
// The form exists because "a resize can reprice a slice or destroy what it
// holds, and both are answered at the form". A rename within the same count and
// the same sizes does neither — and that is not an assertion made here, it is
// one the PLATFORM CHECKS.
//
// `/ops/slice/resize` refuses a repricing change unless the caller sends
// `acknowledge_monthly_cents`, and a destructive one unless it sends
// `confirm_slice_name`. THIS CODE SENDS NEITHER, EVER. So a rename that somehow
// moved the price, or took something away, is refused by the same gate that
// refuses it on the form — and the caller falls back to the original message.
//
// The safety is the platform's existing refusal rather than a second opinion
// computed here, which is what keeps this from becoming a quieter way to do
// something the form would have questioned.
//
// What it must NOT skip is the person. Applying a Driftfile changes what RUNS on
// a slice; this changes what the slice DECLARES, and doing that as an unannounced
// side effect of a deploy is the thing to avoid whatever it costs. So the plan
// and the write are two calls: a caller computes the renames, shows them, asks,
// and only then commits. They were one function, and a deploy rewrote the shape
// of the slice while reporting it in the past tense.
//
// The split is deliberately not a boolean parameter. A `confirm bool` is how the
// two readings end up back in one function, and how the un-asked one gets called
// by accident — the same lesson CheckSliceReferences and ReconcileSliceReferences
// learned one repository over.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SlotAdoption is one idle slot taken over by a name the Driftfile declares.
type SlotAdoption struct {
	From string // the booking key the slot had, e.g. "get:slot-1"
	To   string // the booking key it now carries, e.g. "get:hello"
}

// ErrNoIdleSlots means the slice has nothing spare to rename, so the caller's
// original refusal — naming the resize form — is the right answer after all.
var ErrNoIdleSlots = fmt.Errorf("no idle slot to rename")

// SlotAdoptionPlan is a set of renames worked out and not yet made.
//
// It carries the edited config rather than recomputing it at Commit, so what the
// caller SHOWED and what Commit SENDS are the same bytes. Recomputing between the
// two would let the slice change underneath the question a person just answered.
type SlotAdoptionPlan struct {
	// Adoptions is what a caller shows before asking. Empty is impossible: a plan
	// with nothing to do is ErrNoIdleSlots instead.
	Adoptions []SlotAdoption

	sliceName string
	config    map[string]any
}

// PlanIdleSlotAdoptions works out which idle slots would be renamed to the
// booking keys a Driftfile declares but the slice does not have. IT WRITES
// NOTHING — see the header for why this is not the same function as Commit.
//
// `want` is the missing booking keys, `keep` every key the Driftfile DOES name —
// a slot the manifest already uses is not idle, whatever it is called, and
// renaming one would take a live function off the air.
//
// It returns ErrNoIdleSlots when there is nothing to work with, which is a
// question rather than a failure: the caller then reports what it was going to
// report anyway.
func PlanIdleSlotAdoptions(sliceName string, want []string, keep map[string]bool) (*SlotAdoptionPlan, error) {
	if len(want) == 0 {
		return nil, ErrNoIdleSlots
	}

	rec, err := fetchSliceRecord(sliceName)
	if err != nil {
		return nil, err
	}
	slots := declaredFunctions(rec.Config)
	if len(slots) == 0 {
		// No declared slots at all means no declaration model on this slice, and
		// nothing here to rename. Deploys are not gated on names in that case.
		return nil, ErrNoIdleSlots
	}

	wanted := append([]string(nil), want...)
	sort.Strings(wanted)

	// An idle slot is a PLACEHOLDER the slice was provisioned with and nothing
	// has taken over yet — `get:slot-3`, in the platform's own vocabulary.
	//
	// It used to be "any slot this manifest does not name", which is the same
	// thing only on a slice serving ONE project. A slice may serve several —
	// the apply's own notice says so in as many words — and another project's
	// live function is not named by this Driftfile, so it looked idle and the
	// rename aimed at it. The platform refused, correctly, as a change that
	// takes something away, and the caller fell back to its original message:
	//
	//	your Driftfile names a resource that slice "x" does not have
	//
	// on a slice holding two spare slots. The remedy it named, the resize form,
	// would have shown the slots sitting there.
	var idle []int
	for i, s := range slots {
		key := bookingKey(s.Method, s.Route)
		if keep[key] || contains(wanted, key) {
			continue
		}
		if !isPlaceholderSlot(s.Route) {
			continue
		}
		idle = append(idle, i)
	}
	if len(idle) < len(wanted) {
		return nil, ErrNoIdleSlots
	}

	// The config is mutated in the shape the server sent it, rather than rebuilt
	// from the form's model. Rebuilding would re-encode every other dial on the
	// slice through a second representation, and a rename must change exactly the
	// two fields it says it changes.
	fnList, err := functionList(rec.Config)
	if err != nil {
		return nil, err
	}

	adoptions := make([]SlotAdoption, 0, len(wanted))
	for n, key := range wanted {
		method, route, ok := splitBooking(key)
		if !ok {
			return nil, fmt.Errorf("%q is not a booking key the slice can hold", key)
		}
		at := idle[n]
		entry, ok := fnList[at].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the slice's function list is not in a shape this version can edit")
		}
		adoptions = append(adoptions, SlotAdoption{
			From: bookingKey(slots[at].Method, slots[at].Route),
			To:   key,
		})
		entry["route"] = route
		entry["method"] = method
		// `name` is derived by the server from route+method, but a config that
		// carries one must not keep the old value beside the new halves.
		if _, present := entry["name"]; present {
			entry["name"] = key
		}
	}

	return &SlotAdoptionPlan{Adoptions: adoptions, sliceName: sliceName, config: rec.Config}, nil
}

// Commit sends the renames the plan describes. It is the ONLY thing here that
// writes, and a caller reaches it only after showing Adoptions and being told to
// go ahead.
//
// NO acknowledge_monthly_cents AND NO confirm_slice_name. See the header: those
// are what a form sends after asking a person about a PRICE or a REMOVAL, and a
// rename must need neither. If the platform asks for one, this is not the rename
// it claimed to be, and the answer is to refuse rather than to answer the
// question — a deploy must never be the thing that agrees to a bill.
func (p *SlotAdoptionPlan) Commit() error {
	ok, refusal, perr := postResize(map[string]any{
		"name":                  p.sliceName,
		"config":                p.config,
		"billing_period_months": 1,
		"payment_token":         "cli",
	})
	if perr != nil {
		return perr
	}
	if ok {
		return nil
	}

	switch {
	case refusal == nil:
		return fmt.Errorf("the slice refused the rename")
	case refusal.PriceChange != nil:
		return fmt.Errorf(
			"renaming those slots would change what this slice costs, from %s to %s a month — "+
				"so it is not a rename. Open `drift slice resize %s` and agree to the price there",
			euros(refusal.PriceChange.CurrentMonthlyCents),
			euros(refusal.PriceChange.NewMonthlyCents), p.sliceName)
	case refusal.ConfirmationRequired != nil:
		return fmt.Errorf(
			"renaming those slots would take something away from this slice — "+
				"so it is not a rename. Open `drift slice resize %s`, which lists what and asks you to confirm it",
			p.sliceName)
	default:
		return fmt.Errorf("the slice refused the rename: %s", refusal.Error)
	}
}

// functionList reaches the raw, mutable function entries inside a decoded config.
func functionList(cfg map[string]any) ([]any, error) {
	atomic, ok := cfg["atomic"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the slice's config carries no atomic section")
	}
	list, ok := atomic["functions"].([]any)
	if !ok {
		return nil, fmt.Errorf("the slice's function list is not in a shape this version can edit")
	}
	return list, nil
}

// placeholderSlot matches the routes a slice is PROVISIONED with — `slot-1`,
// `slot-12` — and nothing else.
//
// A tenant who genuinely routes `slot-4` keeps it: this only ever narrows what
// may be renamed, so the failure it can cause is the original refusal naming
// the resize form, which is a true statement and was the behaviour before.
var placeholderSlot = regexp.MustCompile(`^slot-\d+$`)

// isPlaceholderSlot reports whether a route is one of those placeholders.
func isPlaceholderSlot(route string) bool { return placeholderSlot.MatchString(route) }

// bookingKey is how a function is named everywhere a person sees one.
func bookingKey(method, route string) string {
	return strings.ToLower(method) + ":" + route
}

// splitBooking is bookingKey's inverse. A key with no method is not one.
func splitBooking(key string) (method, route string, ok bool) {
	method, route, found := strings.Cut(key, ":")
	if !found || method == "" || route == "" {
		return "", "", false
	}
	return strings.ToLower(method), route, true
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
