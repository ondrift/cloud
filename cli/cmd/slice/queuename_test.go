package slice

import "testing"

// The form must refuse a queue name the platform will refuse, on the row being
// typed rather than after the slice has been priced, summarised and posted.
//
// This is the shape of the bug it is written against: `queue_test` passed the
// form's route check, survived the summary, and came back from the API as
// "queue name %q is not usable" — after the tenant had already agreed to a
// price. The rule is tier.bookingQueuePattern; these cases are the same ones it
// draws.
func TestValidQueueName(t *testing.T) {
	for _, ok := range []string{"emails", "email-out", "q1", "a", "sendmail2"} {
		if err := validQueueName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"queue_test",  // the reported one: underscores are not queue names
		"auth/refill", // a path is not a queue name
		"Emails",      // uppercase
		"-leading",    // a hyphen must be interior
		"trailing-",
		"",
	} {
		if err := validQueueName(bad); err == nil {
			t.Errorf("%q was accepted, and the platform will not take it", bad)
		}
	}
}

// A route is checked by the other rule, so the two do not borrow each other's
// alphabet: a path may hold characters a queue may not, and the reverse.
func TestRouteAndQueueRulesAreDifferent(t *testing.T) {
	if err := validRoute("auth/challenge"); err != nil {
		t.Errorf("a real route was refused: %v", err)
	}
	if err := validQueueName("auth/challenge"); err == nil {
		t.Error("a path passed the queue rule — that is the bug, one step later")
	}
}
