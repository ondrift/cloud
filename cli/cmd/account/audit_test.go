package account

import (
	"testing"
	"time"
)

// A DURATION IS AN AGO. `--since 7d` means seven days back from now, which is
// what someone asking about last week means — not a seven-day-long window
// starting at some other point.
func TestParseWhen_ADurationIsMeasuredBackwardsFromNow(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		back time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"7d", 7 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
	} {
		got, err := parseWhen(tc.raw)
		if err != nil {
			t.Errorf("parseWhen(%q) = %v", tc.raw, err)
			continue
		}
		want := time.Now().Add(-tc.back)
		// A second of slack: `now` moves between the call and the comparison.
		if diff := got.Sub(want); diff > time.Second || diff < -time.Second {
			t.Errorf("parseWhen(%q) is %v from the expected point — a duration must be an AGO",
				tc.raw, diff)
		}
	}
}

// `7d` is the unit this question is asked in and the one Go's ParseDuration
// refuses, so it is handled before the rest is handed over. This is the case
// that regresses if that branch is ever "simplified" away.
func TestParseWhen_DaysAreAcceptedEvenThoughGoRefusesThem(t *testing.T) {
	if _, err := time.ParseDuration("7d"); err == nil {
		t.Skip("Go now parses a day unit; this test's premise is gone")
	}
	if _, err := parseWhen("7d"); err != nil {
		t.Errorf("parseWhen(7d) = %v — the unit everyone types is refused", err)
	}
}

// A bare date is the START of that day, in the caller's own zone: someone typing
// a calendar date means their day, not UTC's.
func TestParseWhen_ABareDateIsLocalMidnight(t *testing.T) {
	got, err := parseWhen("2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("parseWhen(date) = %v, want local midnight %v", got, want)
	}
}

func TestParseWhen_FullTimestamps(t *testing.T) {
	for _, raw := range []string{"2026-09-01T10:30:00Z", "2026-09-01 10:30:00"} {
		if _, err := parseWhen(raw); err != nil {
			t.Errorf("parseWhen(%q) = %v", raw, err)
		}
	}
}

// An unreadable value is refused, and the refusal lists the forms that work —
// the alternative is a user guessing at a vocabulary nothing shows them.
func TestParseWhen_RefusesWhatItCannotReadAndSaysWhatItCan(t *testing.T) {
	_, err := parseWhen("last-tuesday")
	if err == nil {
		t.Fatal("an unreadable time was accepted")
	}
	for _, form := range []string{"7d", "24h", "RFC 3339"} {
		if !contains(err.Error(), form) {
			t.Errorf("the refusal does not mention %q: %v", form, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
