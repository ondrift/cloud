package common

import (
	"bytes"
	"strings"
	"testing"
)

// The properties that decide whether a phone can read this. None of them is
// about what it looks like.
func TestQRCode_IsScannableInShape(t *testing.T) {
	var buf bytes.Buffer
	uri := "otpauth://totp/Drift:alice?secret=JBSWY3DPEHPK3PXP&issuer=Drift&algorithm=SHA1&digits=6&period=30"
	if err := QRCode(&buf, uri); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	out := buf.String()

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("only %d lines — a symbol for a URI this long cannot be that short", len(lines))
	}

	// EXPLICIT COLOURS, on every line. A QR code is dark-on-light by
	// specification; drawn in the terminal's own palette it comes out
	// light-on-dark on most terminals, and an inverted code is one many phone
	// cameras silently refuse.
	for i, line := range lines {
		if !strings.HasPrefix(line, ansiLight) {
			t.Fatalf("line %d does not set its own colours — on a dark terminal this renders inverted", i)
		}
		if !strings.HasSuffix(line, ansiReset) {
			t.Fatalf("line %d does not reset — the rest of the terminal stays white", i)
		}
	}

	// THE QUIET ZONE. Four light modules on every side, or a scanner cannot find
	// the code's edges at all — and the symptom is a code that looks perfect and
	// never reads, which is the hardest kind of bug to attribute.
	//
	// Two blank rows at the top and bottom, because each row of characters is
	// two module rows.
	for _, idx := range []int{0, 1, len(lines) - 1, len(lines) - 2} {
		body := strings.TrimSuffix(strings.TrimPrefix(lines[idx], ansiLight), ansiReset)
		if strings.TrimSpace(body) != "" {
			t.Errorf("line %d is inside the quiet zone and carries dark modules: %q", idx, body)
		}
	}
	// And on the sides.
	for i, line := range lines {
		body := strings.TrimSuffix(strings.TrimPrefix(line, ansiLight), ansiReset)
		runes := []rune(body)
		if len(runes) < 2*quietZone {
			t.Fatalf("line %d is %d cells wide, narrower than two quiet zones", i, len(runes))
		}
		for _, c := range append(runes[:quietZone:quietZone], runes[len(runes)-quietZone:]...) {
			if c != ' ' {
				t.Errorf("line %d has a dark module in the side quiet zone", i)
				break
			}
		}
	}
}

// Every line must be the same width, or the symbol is skewed and the finder
// patterns no longer sit where a scanner looks for them.
func TestQRCode_EveryRowIsTheSameWidth(t *testing.T) {
	var buf bytes.Buffer
	if err := QRCode(&buf, "otpauth://totp/Drift:bob?secret=JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatalf("QRCode: %v", err)
	}

	width := -1
	for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		body := strings.TrimSuffix(strings.TrimPrefix(line, ansiLight), ansiReset)
		n := len([]rune(body))
		if width == -1 {
			width = n
			continue
		}
		if n != width {
			t.Fatalf("line %d is %d cells wide, the first was %d — the symbol is skewed", i, n, width)
		}
	}
}

// Half blocks: two module rows per character cell. Without them a symbol for an
// otpauth URI is over thirty lines tall, which does not fit a terminal beside
// the rest of the enrolment output.
func TestQRCode_UsesHalfBlocksSoItFitsATerminal(t *testing.T) {
	var buf bytes.Buffer
	if err := QRCode(&buf, "otpauth://totp/Drift:alice?secret=JBSWY3DPEHPK3PXP&issuer=Drift"); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	out := buf.String()

	if !strings.ContainsAny(out, "▀▄█") {
		t.Error("no half blocks — the symbol is twice as tall as it needs to be")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	body := strings.TrimSuffix(strings.TrimPrefix(lines[0], ansiLight), ansiReset)
	width := len([]rune(body))
	// Half the height of the width, give or take the odd-row rounding.
	if len(lines) > width/2+2 {
		t.Errorf("%d lines for a %d-cell width — the rows are not being paired", len(lines), width)
	}
}

// A longer payload needs a bigger symbol. A renderer that silently truncated, or
// that always produced the same size, would draw something that scans to the
// wrong thing — worse than not drawing it.
func TestQRCode_GrowsWithThePayload(t *testing.T) {
	height := func(text string) int {
		var buf bytes.Buffer
		if err := QRCode(&buf, text); err != nil {
			t.Fatalf("QRCode(%q): %v", text, err)
		}
		return len(strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"))
	}

	short := height("otpauth://totp/D:a?secret=JBSWY3DP")
	long := height("otpauth://totp/Drift:a-rather-long-account-name-here?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Drift&algorithm=SHA1&digits=6&period=30")

	if long <= short {
		t.Errorf("a %d-line symbol for a long URI and %d for a short one — the payload is not reaching the encoder", long, short)
	}
}
