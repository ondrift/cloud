package wallet

import (
	"math"
	"testing"
)

// An amount is printed from whole cents with two decimals, and a negative one
// keeps its sign in front of the euro sign.
func TestFormatEuros_PrintsWholeCentsWithTwoDecimals(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		want  string
	}{
		{0, "€0.00"},
		{5, "€0.05"},
		{97, "€0.97"},
		{100, "€1.00"},
		{1250, "€12.50"},
		{123456789, "€1234567.89"},
		{-5, "-€0.05"},
		{-1250, "-€12.50"},
		{math.MaxInt64, "€92233720368547758.07"},
		{math.MinInt64, "-€92233720368547758.08"},
	} {
		if got := formatEuros(tc.cents); got != tc.want {
			t.Errorf("formatEuros(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

// What a person types becomes exact cents. The cases a float would get wrong
// (0.29 is 28.999999999999996 cents in float64 arithmetic after multiplying by
// 100) are in the table on purpose.
func TestParseEuros_ReadsExactCents(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"10", 1000},
		{"10.5", 1050},
		{"10.50", 1050},
		{"10,50", 1050},
		{"€10", 1000},
		{"€ 10.00", 1000},
		{" 7 ", 700},
		{"0.29", 29},
		{"0.07", 7},
		{"1.15", 115},
		{"19.99", 1999},
		{".5", 50},
		{"10.", 1000},
		{"999999999.99", 99999999999},
	} {
		got, err := parseEuros(tc.in)
		if err != nil {
			t.Errorf("parseEuros(%q) failed: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseEuros(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// What is not a positive amount of whole cents is refused, never rounded or
// guessed at.
func TestParseEuros_RefusesWhatIsNotAPositiveAmountOfWholeCents(t *testing.T) {
	for _, in := range []string{
		"", " ", "€", "abc", "10x", "-5", "+5", "0", "0.00", ".", "10.505", "10.001",
		"1e3", "1_000", "10.5.5", "1,000.50", "1000000000", "9999999999999999999999",
	} {
		if got, err := parseEuros(in); err == nil {
			t.Errorf("parseEuros(%q) = %d, want a refusal", in, got)
		}
	}
}
