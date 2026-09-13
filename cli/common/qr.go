package common

// qr.go — a QR code drawn in the terminal.
//
// An enrolment hands over an `otpauth://` URI. Typing a 32-character base32
// secret into a phone by hand is where people give up on turning a second factor
// on, and the whole point of the URI is that a camera can read it — so it is
// worth drawing where the camera can see it.
//
// # Why this one IS a dependency, when drift-common/totp is not
//
// TOTP is written out in the platform because the algorithm is HMAC over a
// counter, truncated: two short functions, unchanged since 2011, with published
// test vectors to prove the result interoperates.
//
// QR is the opposite shape. Encoding needs mode and version selection,
// Reed-Solomon error correction over GF(256), eight candidate masks and a
// penalty score to choose between them. Writing that out is several hundred
// lines whose failure mode is a code that renders beautifully and scans as
// nothing — and no camera here to notice. `rsc.io/qr` is small, has no
// third-party dependencies of its own, and has been stable for a decade.
//
// # Half blocks, and explicit colours
//
// One character cell is two module rows, using ▀ and ▄, so a code that would be
// 33 lines tall is 17 — the difference between fitting in a terminal and not.
//
// The colours are set EXPLICITLY rather than left to the terminal's own. A QR
// code is dark-on-light by specification; drawing it with the terminal's default
// palette produces light-on-dark on the majority of terminals, which is an
// inverted code that many phone cameras silently refuse. So the background is
// forced white and the foreground black, and the quiet zone — four light modules
// on every side, which scanners require to find the code at all — is drawn in
// that same white rather than left as whatever is behind the text.

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

const (
	// quietZone is the margin of light modules the specification requires on
	// every side. Without it a scanner cannot find the code's edges, and the
	// symptom is a code that looks perfect and never reads.
	quietZone = 4

	// White background, black foreground, from the 256-colour palette so the
	// result does not depend on how a terminal renders "bright white".
	ansiLight = "\033[48;5;231m\033[38;5;16m"
	ansiReset = "\033[0m"
)

// QRCode renders text as a scannable QR code and writes it to w.
//
// Medium error correction: enough that a phone reads it across a slightly dirty
// terminal font or a photograph of a screen, without growing the symbol so much
// that it stops fitting.
//
// An encoding failure is returned rather than printed. The caller has already
// shown the secret and the URI, so a terminal that cannot draw this loses a
// convenience and not the enrolment.
func QRCode(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return fmt.Errorf("encode QR code: %w", err)
	}

	size := code.Size
	full := size + quietZone*2

	// Two module rows per line. The step is 2, and the second row of a pair may
	// fall outside the symbol on an odd height — dark(x, y) treats anything out
	// of range as light, which is exactly the quiet zone continuing.
	var b strings.Builder
	for y := 0; y < full; y += 2 {
		b.WriteString(ansiLight)
		for x := 0; x < full; x++ {
			upper := dark(code, x-quietZone, y-quietZone)
			lower := dark(code, x-quietZone, y+1-quietZone)
			switch {
			case upper && lower:
				b.WriteString("█")
			case upper:
				b.WriteString("▀")
			case lower:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString(ansiReset)
		b.WriteString("\n")
	}

	_, err = io.WriteString(w, b.String())
	return err
}

// dark reports whether a module is black, treating everything outside the symbol
// as light. That is what makes the quiet zone fall out of the same loop rather
// than needing its own.
func dark(code *qr.Code, x, y int) bool {
	if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}
