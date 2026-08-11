package slice

import (
	"strings"
	"testing"
)

// THE regression. `slice use` warned and set the name anyway, so a typo left
// every later command answering differently about the same missing slice:
// `atomic list` said "No functions deployed." — the api really does return 200
// with an empty array — while `backbone` said the slice was not found. One of
// those reads as an empty slice and the other as a broken platform, and neither
// says what to do.
//
// The names are already in hand from the lookup that detects the miss, so
// listing them turns "does not exist" into "here is what you meant".
func TestUnknownSliceMessage_NamesItAndListsTheRealOnes(t *testing.T) {
	got := unknownSliceMessage("nosuchslice", []string{"prorata", "lab", "imprente"})

	if !strings.Contains(got, "nosuchslice") {
		t.Errorf("must name the slice that was asked for:\n%s", got)
	}
	for _, real := range []string{"prorata", "lab", "imprente"} {
		if !strings.Contains(got, real) {
			t.Errorf("must list %q:\n%s", real, got)
		}
	}
	// Sorted, so the same account always reads the same way.
	if strings.Index(got, "imprente") > strings.Index(got, "lab") {
		t.Errorf("slices should be sorted:\n%s", got)
	}
	// It must say the active slice did NOT change, or the user cannot tell
	// whether the command half-succeeded.
	if !strings.Contains(got, "unchanged") {
		t.Errorf("must say the active slice is unchanged:\n%s", got)
	}
}

// An account with no slices is a different problem: nothing is misspelled, there
// is simply nothing to select. Listing an empty set reads as a bug, so this path
// says what to do instead.
func TestUnknownSliceMessage_NoSlicesYet(t *testing.T) {
	got := unknownSliceMessage("anything", nil)

	if !strings.Contains(got, "no slices yet") {
		t.Errorf("must say the account has none:\n%s", got)
	}
	if !strings.Contains(got, "drift slice create") {
		t.Errorf("must say how to make one:\n%s", got)
	}
	if strings.Contains(got, "This account has:") {
		t.Errorf("must not print an empty list:\n%s", got)
	}
}
