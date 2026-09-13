package common

import (
	"fmt"
	"unicode/utf8"
)

// The password rule this binary can check WITHOUT asking the platform.
//
// # Why the CLI checks at all
//
// Signup is a two-step flow: initiate, then an emailed code. A password the
// server was always going to refuse costs the user that whole round trip before
// they find out, so the obvious half is checked here first. That is the only
// job — the SERVER decides, and it checks these same bounds on receipt.
//
// # Why only the bounds, and not the breach screen
//
// The platform also refuses passwords that appear in a public breach corpus.
// That corpus is 440 KB of data the platform embeds, and shipping a second copy
// in the CLI would mean two copies to keep in step, in two repos, on two release
// cadences — the exact failure this rule already had when it was `len(pw) < 8`
// written out four times.
//
// So the CLI checks what a rule can state in two numbers, and says plainly that
// the server checks more. A password that clears this and is refused on receipt
// is not a bug: the message that comes back says why.
const (
	// MinPasswordLength must not exceed the platform's floor. If they disagree,
	// the honest direction is for this to be SMALLER: too lax here means one
	// wasted round trip, too strict means the CLI refuses a password the
	// platform would have taken, which looks like a broken client and cannot be
	// worked around.
	MinPasswordLength = 12

	// MaxPasswordBytes is bcrypt's limit, which is where the platform's ceiling
	// comes from too. Bytes, not characters: the hash counts bytes, and 24 emoji
	// is 24 characters and 96 bytes.
	MaxPasswordBytes = 72
)

// ValidatePassword reports why a password cannot be used, or nil. The message is
// written to be printed as-is.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return fmt.Errorf("Password must be at least %d characters.", MinPasswordLength)
	}
	if len(pw) > MaxPasswordBytes {
		return fmt.Errorf("Password must be at most %d bytes — that is the limit of the algorithm that hashes it.", MaxPasswordBytes)
	}
	return nil
}
