package common

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The floor. Eleven characters cleared the rule this replaces, and the whole
// point of checking here is that the user finds out before the emailed-code
// round trip rather than after it.
func TestValidatePassword_RefusesUnderTheFloor(t *testing.T) {
	for _, pw := range []string{"", "short", "aaaaaaaa", "elevenchars"} {
		if err := ValidatePassword(pw); err == nil {
			t.Errorf("ValidatePassword(%q) = nil, want a refusal", pw)
		}
	}
}

// Bytes, not characters — the ceiling belongs to bcrypt, which counts bytes.
// Twenty-four emoji is 24 characters and 96 bytes, and a character-counted
// ceiling would let it through to be refused on receipt.
func TestValidatePassword_TheCeilingCountsBytes(t *testing.T) {
	pw := strings.Repeat("😀", 24)
	if utf8.RuneCountInString(pw) > MaxPasswordBytes {
		t.Fatalf("fixture has %d runes — not the case under test", utf8.RuneCountInString(pw))
	}
	if err := ValidatePassword(pw); err == nil {
		t.Errorf("%d bytes was accepted", len(pw))
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Errorf("exactly %d bytes must be accepted: %v", MaxPasswordBytes, err)
	}
}

// The control, and the half that keeps this client honest: a breached password
// long enough to clear the bounds is ACCEPTED here, because the corpus lives on
// the platform and this binary does not carry it. The server refuses it.
//
// If this ever starts failing, someone has shipped a second copy of the corpus
// into the CLI — which is the thing the package comment argues against.
func TestValidatePassword_LeavesTheBreachScreenToTheServer(t *testing.T) {
	if err := ValidatePassword("Password123!"); err != nil {
		t.Errorf("the CLI refused %q locally: %v — the breach screen belongs to the platform",
			"Password123!", err)
	}
}

// A strong passphrase passes, or every test above passes against a function that
// refuses everything.
func TestValidatePassword_AcceptsAStrongPassphrase(t *testing.T) {
	if err := ValidatePassword("vier-en-twintig-schapen"); err != nil {
		t.Errorf("unexpected refusal: %v", err)
	}
}
