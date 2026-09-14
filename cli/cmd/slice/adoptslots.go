package slice

// adoptslots.go — renaming a slot the slice holds and nothing uses.
//
// # The first hour ended here, and the slot was always there
//
// A fresh free slice holds five function slots named `get:slot-1` … `get:slot-5`
// — placeholders, in the platform's own vocabulary, for a tenant to type over.
// `drift file new` scaffolds `route: hello`. So `drift file apply` refused:
//
//	your Driftfile names a resource that slice "hello" does not have:
//	  - function "get:hello"
//	Add them with `drift slice resize hello`, then deploy again.
//
// and `drift slice resize` draws a form, which needs a terminal. The first hour
// could not be completed without one, and no script, CI job or SDK consumer
// could provision a slice's shape at all.
//
// The refusal was correct and the remedy was a form for a change that is not
// what forms are for. The slot EXISTS, at the right size, doing nothing. Only
// the name differs, and the pricing page already says renaming is free and that
// nothing about a free slice is compared by name.
//
// # Why this is allowed to skip the form
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

import (
	"fmt"
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

// AdoptIdleSlots renames slots the slice holds and nothing names, to the booking
// keys a Driftfile declares but the slice does not have.
//
// `want` is the missing booking keys, `keep` every key the Driftfile DOES name —
// a slot the manifest already uses is not idle, whatever it is called, and
// renaming one would take a live function off the air.
//
// It returns ErrNoIdleSlots when there is nothing to work with, which is a
// question rather than a failure: the caller then reports what it was going to
// report anyway.
func AdoptIdleSlots(sliceName string, want []string, keep map[string]bool) ([]SlotAdoption, error) {
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

	// An idle slot is one the manifest does not name and that is not itself
	// something we are trying to create.
	var idle []int
	for i, s := range slots {
		key := bookingKey(s.Method, s.Route)
		if keep[key] {
			continue
		}
		if contains(wanted, key) {
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

	// NO acknowledge_monthly_cents AND NO confirm_slice_name. See the header:
	// those are what a form sends after asking a person, and a rename must need
	// neither. If the platform asks for one, this is not the rename it claimed to
	// be and the answer is to refuse rather than to answer the question.
	ok, refusal, perr := postResize(map[string]any{
		"name":                  sliceName,
		"config":                rec.Config,
		"billing_period_months": 1,
		"payment_token":         "cli",
	})
	if perr != nil {
		return nil, perr
	}
	if ok {
		return adoptions, nil
	}

	switch {
	case refusal == nil:
		return nil, fmt.Errorf("the slice refused the rename")
	case refusal.PriceChange != nil:
		return nil, fmt.Errorf(
			"renaming those slots would change what this slice costs, from %s to %s a month — "+
				"so it is not a rename. Open `drift slice resize %s` and agree to the price there",
			euros(refusal.PriceChange.CurrentMonthlyCents),
			euros(refusal.PriceChange.NewMonthlyCents), sliceName)
	case refusal.ConfirmationRequired != nil:
		return nil, fmt.Errorf(
			"renaming those slots would take something away from this slice — "+
				"so it is not a rename. Open `drift slice resize %s`, which lists what and asks you to confirm it",
			sliceName)
	default:
		return nil, fmt.Errorf("the slice refused the rename: %s", refusal.Error)
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
