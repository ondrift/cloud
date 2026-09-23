package slice

// labels.go — writing the tenant's own labels onto a slice.
//
// # Why a deploy is allowed to call this, when it may not resize
//
// `drift file apply` must never change what a slice COSTS or what it HOLDS.
// That rule is why it refuses a Driftfile naming more functions than the slice
// has, instead of growing one, and why there is no prompt on that path that
// could be answered into a larger bill.
//
// A label is neither. It has no price — `plan.PriceConfig` charges for scheduled
// jobs, realtime connections, booked function memory and storage bytes, and a
// label is none of them — and it destroys nothing: no limit moves, no declared
// set shrinks, no route stops answering. So the Driftfile can own labels outright
// without apply acquiring the power it was deliberately denied.
//
// # A labels-only PATCH, which is what makes it possible at all
//
// The config sent carries `labels` and nothing else. A resize applies the keys
// PRESENT in the request over the stored config, so every limit, every booking
// and every declared set is kept by omission. Against a platform that still
// REPLACED the config wholesale this body would ask for a slice with every limit
// zeroed — which that platform refuses, loudly, rather than applying; the caller
// reports it as what it is.
//
// # Neither acknowledgement, ever
//
// No `acknowledge_monthly_cents` and no `confirm_slice_name`, the same rule
// AdoptIdleSlots states one file over. If the platform asks for one, this is not
// the label change it claimed to be — a deploy must never be the thing that
// agrees to a bill — and the answer is to refuse and say so.

import "fmt"

// SetLabels replaces a slice's tenant labels with exactly the set given.
//
// REPLACES, not merges: the Driftfile is the whole declaration, so a label it
// stops naming is a label the slice stops carrying. An empty map is therefore
// meaningful and is sent as one — it is how a tenant removes the last label, and
// it must not be confused with "say nothing about labels", which is what omitting
// the key from the patch would mean.
func SetLabels(sliceName string, labels map[string]string) error {
	if labels == nil {
		labels = map[string]string{}
	}

	ok, refusal, err := postResize(map[string]any{
		"name":                  sliceName,
		"config":                map[string]any{"labels": labels},
		"billing_period_months": 1,
		"payment_token":         "cli",
	})
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	switch {
	case refusal == nil:
		return fmt.Errorf("the slice refused the label change")

	case refusal.PriceChange != nil:
		return fmt.Errorf(
			"the platform priced a labels-only change at %s a month, up from %s — a label is "+
				"not a billable resource, so this is not the change it was sent as. Nothing was "+
				"applied; `drift slice resize %s` is where a change to the shape belongs",
			euros(refusal.PriceChange.NewMonthlyCents),
			euros(refusal.PriceChange.CurrentMonthlyCents), sliceName)

	case refusal.ConfirmationRequired != nil:
		// The shape of this refusal against a platform that replaces the config
		// rather than patching it: every limit the body did not mention arrives
		// as zero, and the destructive guard reports them all. Naming that reading
		// is the difference between an error someone can act on and one that
		// looks like the labels were the problem.
		return fmt.Errorf(
			"the platform read a labels-only change as taking things away from slice '%s', "+
				"which means it applies a resize by REPLACING the config rather than patching "+
				"it. Nothing was applied. Labels need a platform that keeps the fields a "+
				"request does not mention",
			sliceName)

	default:
		return fmt.Errorf("the slice refused the label change: %s", refusal.Error)
	}
}
