package portal

// Small formatting helpers shared across the dashboard's panes.

import (
	"fmt"
	"strings"
)

// cents renders an integer cent amount as euros.
func cents(c int) string {
	return fmt.Sprintf("€%d.%02d", c/100, c%100)
}

// wrapText word-wraps plain text to a visible width of w (no ANSI inside).
func wrapText(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
