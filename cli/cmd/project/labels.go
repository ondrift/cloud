package project

// labels.go — Driftfile reconcile of the tenant's own slice labels.
//
// ONE ENTRYPOINT. The Driftfile declares the labels a slice carries, and an
// apply makes the slice match: a label added to the file appears, a label
// removed from it goes, a value changed is changed. That is what "declarative"
// means here, and it is the whole reason labels are reconciled from the deploy
// path at all rather than needing a command of their own.
//
// # Why this may run inside an apply when a resize may not
//
// Apply must never change what a slice COSTS or what it HOLDS — see
// `cmd/slice/labels.go` for the argument in full. A label has no price and
// destroys nothing, so the Driftfile can own it outright without apply gaining
// the power it was deliberately denied.
//
// # And why it does not ask
//
// A slot rename is shown and confirmed, because it changes what the slice
// DECLARES and can take a live route off the air. A label can do neither. It is
// reported, never prompted: a question on every label edit would make the
// Driftfile a worse anchor rather than a better one, and `--yes` would become
// the flag everybody passes.
//
// # The absent block means none
//
// A Driftfile with no `labels:` key declares no labels, and a slice carrying
// some is brought to match. That is the same rule as every other declared set,
// and it is the half that makes removal work at all — there is nowhere else to
// say "and none of the others".

import (
	"fmt"
	"io"
	"sort"

	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// labelChange is one difference between what the Driftfile declares and what the
// slice carries, for reporting.
type labelChange struct {
	Key  string
	From string // "" when the label is being added
	To   string // "" when the label is being removed
}

// diffLabels lists what applying `declared` to `current` would change, in key
// order so two runs of the same change read identically.
func diffLabels(declared, current map[string]string) []labelChange {
	keys := make([]string, 0, len(declared)+len(current))
	seen := map[string]bool{}
	for _, m := range []map[string]string{declared, current} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)

	var out []labelChange
	for _, k := range keys {
		was, had := current[k]
		now, want := declared[k]
		if had == want && was == now {
			continue
		}
		out = append(out, labelChange{Key: k, From: was, To: now})
	}
	return out
}

// describeLabelChange renders one change the way a person reads it.
func describeLabelChange(c labelChange) string {
	switch {
	case c.From == "":
		return fmt.Sprintf("+ %s=%s", c.Key, c.To)
	case c.To == "":
		return fmt.Sprintf("- %s (was %s)", c.Key, c.From)
	default:
		return fmt.Sprintf("~ %s=%s (was %s)", c.Key, c.To, c.From)
	}
}

// applyLabels brings the slice's labels in line with the Driftfile's.
//
// `live` is the record apply already read, so this costs no extra call on the
// common path — and the common path is "no labels anywhere", which returns
// before doing anything at all.
func applyLabels(m *Manifest, live *LiveSlice, out io.Writer) error {
	if live == nil {
		return nil
	}
	declared := m.Slice().StrMap("labels")
	changes := diffLabels(declared, live.Config.Labels)
	if len(changes) == 0 {
		return nil
	}

	if err := slice.SetLabels(m.Name(), declared); err != nil {
		// Loud, and it fails the apply, for the reason applyEgress states: a
		// declaration that reached nothing while the deploy reported Done is the
		// shape of every "succeeded without doing anything" defect there is.
		return fmt.Errorf("labels: %w", err)
	}

	fmt.Fprintf(out, "  %s labels updated:\n", common.Check())
	for _, c := range changes {
		fmt.Fprintf(out, "      %s\n", describeLabelChange(c))
	}
	return nil
}
