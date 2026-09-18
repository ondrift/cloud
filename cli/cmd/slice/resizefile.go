package slice

// resizefile.go — resizing a slice without a terminal.
//
// # The gap this closes
//
// `drift slice resize` draws a form, and a form needs a TTY. So no script, no CI
// job and no SDK consumer could change a slice's shape at all: every automated
// path onto Drift began with a human at a form, and the one knob a scheduled job
// needs — the slice's scheduled-job count — was reachable no other way.
//
// The refusal was honest about its reason and it was the wrong conclusion:
//
//	a resize can book memory per function for the first time, or destroy what
//	it holds, and both are answered at the form
//
// Both are answered at the form, and the FORM IS NOT WHAT MAKES THEM SAFE. The
// platform is. `/ops/slice/resize` refuses that ONE repricing transition unless
// the caller sends `acknowledge_monthly_cents` (an ordinary price increase from
// more storage, scheduled jobs or realtime connections is not asked to confirm —
// it is exactly what was requested), and a destructive change unless it sends
// `confirm_slice_name` — it asks those questions of every caller, form or not,
// and answers them itself. The form is one way to collect the answers; a flag is
// another.
//
// That is the same argument `adoptslots.go` already makes for the rename case,
// and it is made here for the general one: the safety is the platform's existing
// refusal rather than a second opinion computed in the CLI or a terminal that
// happens to be attached.
//
// # The loop it makes possible
//
//	drift slice resize my-slice --dump > shape.json     # what it is now
//	$EDITOR shape.json                                  # what it should be
//	drift slice resize my-slice --config shape.json \
//	    --acknowledge-monthly-cents 1200                # yes, at that price
//
// # Why --acknowledge-monthly-cents takes a NUMBER rather than being a bool
//
// Because a price read from a file is a price that may have moved. The flag is
// the figure the caller believes they are agreeing to, checked against the one
// the platform quotes, and a mismatch is refused with both numbers named. A
// bare `--yes` would agree to whatever the answer turned out to be, which is the
// one thing the form never does: it shows the row before it asks.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// dumpSliceConfig writes a slice's current shape as JSON, for editing.
//
// The RECORD'S OWN CONFIG, byte for byte, not a rendering of it. A dump the
// caller cannot feed back is not a dump, and rebuilding it from the form's model
// would re-encode every dial through a second representation — the mistake
// AdoptIdleSlots avoids by mutating what the server sent.
func dumpSliceConfig(name string, out *os.File) error {
	rec, err := fetchSliceRecord(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if eerr := enc.Encode(rec.Config); eerr != nil {
		return fmt.Errorf("writing the shape of %q: %w", name, eerr)
	}
	return nil
}

// resizeFromFile applies a shape read from a file, answering the platform's two
// questions from flags instead of from a form.
//
// ackCents is -1 when the caller did not pass it, which is different from 0: a
// slice can legitimately cost nothing, so "no figure given" and "the figure is
// zero" cannot share a value.
func resizeFromFile(name, path string, billingMonths, ackCents int, confirm string) error {
	if billingMonths < 1 {
		billingMonths = 1
	}

	raw, err := os.ReadFile(path) // #nosec G304 — the operator's own file, named on the command line
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg map[string]any
	if uerr := json.Unmarshal(raw, &cfg); uerr != nil {
		return fmt.Errorf("%s is not a slice shape this version can read: %w\n"+
			"  Produce one with `drift slice resize %s --dump > shape.json`", path, uerr, name)
	}
	if len(cfg) == 0 {
		return fmt.Errorf("%s decodes to an empty shape — applying it would ask the platform "+
			"to take everything away. Produce a shape with `drift slice resize %s --dump`", path, name)
	}

	payload := map[string]any{
		"name":                  name,
		"config":                cfg,
		"billing_period_months": billingMonths,
		"payment_token":         "cli",
	}
	// THE TWO ANSWERS, and they are sent only when the caller gave them. Sending
	// either unasked would agree in advance to a question the platform has not
	// put — which is exactly what a `--yes` that means "whatever it turns out to
	// be" does.
	if ackCents >= 0 {
		payload["acknowledge_monthly_cents"] = ackCents
	}
	if confirm != "" {
		payload["confirm_slice_name"] = confirm
	}

	ok, refusal, perr := postResize(payload)
	if perr != nil {
		return perr
	}
	if ok {
		fmt.Printf("Slice '%s' resized from %s.\n", name, path)
		return nil
	}

	switch {
	case refusal == nil:
		return fmt.Errorf("the platform refused the resize and said nothing about why")

	case refusal.PriceChange != nil:
		// The platform quotes the real figure. Naming BOTH is the point: a
		// caller who passed a number that no longer matches needs to see what it
		// has become before agreeing again, which is what the form's row does.
		if ackCents >= 0 {
			return fmt.Errorf(
				"this resize costs %s a month, not the %s you acknowledged — the price moved, "+
					"or the shape is not the one you priced.\n"+
					"  Re-run with --acknowledge-monthly-cents %d once you have checked it",
				euros(refusal.PriceChange.NewMonthlyCents), euros(ackCents),
				refusal.PriceChange.NewMonthlyCents)
		}
		return fmt.Errorf(
			"this resize changes what the slice costs, from %s to %s a month.\n"+
				"  Re-run with --acknowledge-monthly-cents %d to agree to it",
			euros(refusal.PriceChange.CurrentMonthlyCents),
			euros(refusal.PriceChange.NewMonthlyCents),
			refusal.PriceChange.NewMonthlyCents)

	case refusal.ConfirmationRequired != nil:
		// What it would DESTROY, listed. A confirmation flag with nothing to
		// read is a rubber stamp, and the form's whole value here is the list.
		var b strings.Builder
		fmt.Fprintf(&b, "this resize takes something away from slice '%s':\n", name)
		for _, d := range refusal.ConfirmationRequired.Destroys {
			fmt.Fprintf(&b, "    %s\n", describeDestroy(d))
		}
		fmt.Fprintf(&b, "  Re-run with --confirm %s to say you mean it", name)
		return fmt.Errorf("%s", b.String())

	case len(refusal.Violations) > 0:
		// A limit below what the slice is already using. Not answerable by any
		// flag: the shape has to change, or the usage does.
		var b strings.Builder
		b.WriteString("this shape is smaller than what the slice already holds:\n")
		for _, v := range refusal.Violations {
			fmt.Fprintf(&b, "    %s: using %d, new limit %d\n", v.Resource, v.Used, v.NewLimit)
		}
		b.WriteString("  Raise those in the file, or remove what is using them first")
		return fmt.Errorf("%s", b.String())

	default:
		return fmt.Errorf("the platform refused the resize: %s", refusal.Error)
	}
}

// describeDestroy renders one entry of the platform's `destroys` list.
//
// The keys are whatever the operator chose to send, so this prints what is there
// in a stable order rather than naming fields that may not exist. A destroy the
// CLI could not describe would otherwise print as an empty line, which reads as
// "nothing" beside a warning that something is being removed.
func describeDestroy(d map[string]string) string {
	for _, k := range []string{"resource", "name", "kind", "detail"} {
		if v, ok := d[k]; ok && v != "" {
			if k == "resource" {
				if n, ok2 := d["name"]; ok2 && n != "" {
					return v + " " + n
				}
			}
			return v
		}
	}
	if len(d) == 0 {
		return "(an unnamed resource)"
	}
	parts := make([]string, 0, len(d))
	for k, v := range d {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}
