package slice

import (
	"encoding/json"
	"testing"
)

// The number a user wants from `slice list` is what the slice costs. The label
// it replaces, "configured", restates the tier they already chose.
//
// The price is the STORED one, locked in when the slice was created or last
// resized. Rendering it from anything else would print a figure that differs
// from the one being billed the moment unit prices move — which is worse than
// printing nothing, because it looks authoritative.
func TestPriceLabel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tier  string
		cents int
		want  string
	}{
		{"free tier reads as free", "hacker", 0, "free"},
		// A free slice carries no charge, so a stored figure on one is a
		// record-keeping artefact rather than something to bill or show.
		{"free tier stays free even with a stored figure", "hacker", 500, "free"},
		{"a configured slice shows its price", "custom", 272, "EUR 2.72/mo"},
		{"a whole euro keeps both decimals", "custom", 300, "EUR 3.00/mo"},
		{"sub-euro does not lose its leading zero", "custom", 5, "EUR 0.05/mo"},
		// No stored price is not a price of zero. "EUR 0.00/mo" would state
		// that the slice is free, which is a billing claim nothing here can
		// make; falling back to the old label says only what is known.
		{"no stored price falls back to the tier label", "custom", 0, "configured"},
		{"a negative figure is not rendered as a price", "custom", -100, "configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PriceLabel(tc.tier, tc.cents); got != tc.want {
				t.Errorf("PriceLabel(%q, %d) = %q, want %q", tc.tier, tc.cents, got, tc.want)
			}
		})
	}
}

// TierLabel is shared with the portal TUI, which shows a slice's tier rather
// than its price. Adding the price to `slice list` must not change what that
// caller renders.
func TestTierLabelIsUnchanged(t *testing.T) {
	if got := TierLabel("hacker"); got != "free" {
		t.Errorf("TierLabel(hacker) = %q, want free", got)
	}
	if got := TierLabel("custom"); got != "configured" {
		t.Errorf("TierLabel(custom) = %q, want configured", got)
	}
}

// The API encodes the whole slice document, so the price is already on the
// wire — this pins that SliceEntry reads the field the server actually sends.
// A renamed json tag would otherwise show every slice as "configured" while
// the endpoint kept answering correctly.
func TestSliceEntryDecodesTheStoredPrice(t *testing.T) {
	const body = `[{"name":"lab","tier":"custom","monthly_cost_cents":272}]`

	var got []SliceEntry
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].MonthlyCostCents != 272 {
		t.Errorf("MonthlyCostCents = %d, want 272", got[0].MonthlyCostCents)
	}
}

// Labels are read from INSIDE the config, which is where the platform keeps
// them — beside canvas, atomic and backbone, not beside the name.
//
// A flattened `"labels"` at the top level would decode to nothing while the
// endpoint answered perfectly, and every slice would list as unlabelled. This is
// the assertion that fails if the nesting is ever "simplified".
func TestSliceEntryReadsLabelsFromInsideTheConfig(t *testing.T) {
	const body = `[{"name":"shop","tier":"hacker","monthly_cost_cents":0,` +
		`"config":{"labels":{"team":"backend","client":"bakkerij & zn."}}}]`

	var got []SliceEntry
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n := len(got[0].Config.Labels); n != 2 {
		t.Fatalf("read %d labels, want 2 — they are nested under `config`", n)
	}
	if got[0].Config.Labels["client"] != "bakkerij & zn." {
		t.Errorf("client = %q — a value with spaces and an ampersand must survive",
			got[0].Config.Labels["client"])
	}
}

// A slice that carries no labels prints NO line, rather than an empty one.
//
// The whole set is optional and most slices have none, so a blank line under
// every row would be the common case — turning a list you can scan into one you
// have to read.
func TestLabelsLine_NothingToShowIsNoLine(t *testing.T) {
	if got := LabelsLine(nil); got != "" {
		t.Errorf("LabelsLine(nil) = %q, want empty", got)
	}
	if got := LabelsLine(map[string]string{}); got != "" {
		t.Errorf("LabelsLine(empty) = %q, want empty", got)
	}
}

// SORTED, because Go randomises map iteration. Without this the same slice
// prints its labels in a different order on consecutive runs, which cannot be
// diffed and reads as the labels having changed.
func TestLabelsLine_IsOrderedByKey(t *testing.T) {
	labels := map[string]string{"team": "backend", "client": "acme", "env": "staging"}

	want := "client=acme  env=staging  team=backend"
	for i := 0; i < 20; i++ {
		if got := LabelsLine(labels); got != want {
			t.Fatalf("run %d rendered %q, want %q — map order reached the output", i, got, want)
		}
	}
}
