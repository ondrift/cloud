package wallet

// money.go — euros as integer cents, in and out.
//
// The platform holds and moves money as whole euro cents and never as a
// fraction, and this file keeps to that: an amount a person types is parsed
// from its decimal digits straight to cents, and a figure the platform sends is
// printed from cents by integer division. No float64 touches an amount, so
// 0.1 + 0.2 style rounding cannot reach a balance, a top-up or a history line.

import (
	"fmt"
	"strings"
)

// maxWholeEuros bounds what a person may type, so the digits always fit an
// int64 of cents with room to spare. The platform has its own minimum and
// decides everything else; this only keeps the arithmetic safe.
const maxWholeEuros = 9

// formatEuros prints cents as euros with two decimals: 1250 is "€12.50" and
// -5 is "-€0.05".
func formatEuros(cents int64) string {
	sign := ""
	abs := uint64(cents)
	if cents < 0 {
		sign = "-"
		abs = -abs
	}
	return fmt.Sprintf("%s€%d.%02d", sign, abs/100, abs%100)
}

// parseEuros reads an amount a person typed: "10", "10.5", "10.50", "€10",
// "10,50". It returns whole cents, and refuses what is not a positive amount of
// at most two decimals, so "10.505" is an error and is never rounded.
func parseEuros(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "€")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("give an amount in euros, like 10 or 12.50")
	}
	s = strings.Replace(s, ",", ".", 1)

	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && !hasDot {
		return 0, fmt.Errorf("%q is not an amount in euros", raw)
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (hasDot && frac != "" && !allDigits(frac)) {
		return 0, fmt.Errorf("%q is not an amount in euros; use digits, like 10 or 12.50", raw)
	}
	if len(frac) > 2 {
		return 0, fmt.Errorf("%q has more than two decimals; an amount is whole cents", raw)
	}
	if len(whole) > maxWholeEuros {
		return 0, fmt.Errorf("%q is larger than a top-up can be", raw)
	}
	for len(frac) < 2 {
		frac += "0"
	}

	var cents int64
	for _, d := range whole + frac {
		cents = cents*10 + int64(d-'0')
	}
	if cents <= 0 {
		return 0, fmt.Errorf("an amount must be more than zero")
	}
	return cents, nil
}

func allDigits(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
