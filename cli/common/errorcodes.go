package common

// errorcodes.go — names for the failures a user can hit.
//
// A failure cannot explain itself until it has a name. Before this, an error was
// a sentence and nothing else: an FAQ could not reference one, docs could not
// anchor one, and a user pasting a message handed the operator a string that
// appears in no source file.
//
// # Why it is embedded
//
// `go:embed`, so `drift doctor explain DRIFT-1006` answers during an outage —
// which is exactly when someone is holding that code. A lookup that needs the
// network cannot explain why the network failed. The Driftfile schema already
// proves the pattern in the other direction: it is served BY the platform
// because the platform owns the format, and this is owned by the binary because
// the binary is what prints it.
//
// # The one rule
//
// A code is minted once. Never reused, never renumbered, never deleted — a code
// in a chat log a year old must still resolve to the same failure. To stop using
// one, mark it `retired` and leave it where it is.
//
// That rule is mechanical rather than remembered, in two parts. The sequence
// must be dense, so deleting an entry leaves a gap and the test fails. And every
// entry carries a Key, the name of the failure its code stands for, which is
// unique and never changes: the pull-request job hands the tests the registry as
// it stands on the base branch, and a code that went missing or whose key changed
// fails the build. Two entries that swap contents, or a code pointed at another
// failure, leave the sequence dense and change a key.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed errorcodes.json
var errorCodesJSON []byte

// ErrorCode is one named failure.
type ErrorCode struct {
	// Code is the stable identifier a user sees and pastes, "DRIFT-1006".
	Code string `json:"code"`
	// Key names the failure this code stands for, "session-expired": lowercase
	// words joined by hyphens, unique in the registry, and never changed once the
	// code is minted. The wording around a code can be improved and its number is
	// only an address; the key is what the code IS, and what a pull request's
	// registry is compared on against the base branch's.
	Key string `json:"key"`
	// Meaning is one line saying what happened, in the user's terms.
	Meaning string `json:"meaning"`
	// Remedy is the prose: what to do and why, for `doctor explain`.
	Remedy string `json:"remedy"`
	// Command is the fix, in at most THREE WORDS — "drift account login".
	//
	// Separate from Remedy because it is machine-checkable and the prose is not:
	// a test asserts every one of these is three words or fewer AND resolves to a
	// command that exists, which is the whole reason the rule survives contact.
	//
	// Empty is a real answer, and an important one: DRIFT-1005 and DRIFT-1006 are
	// a platform fault and an unreachable network, where inventing a command for
	// the user to run would send them somewhere that fails again.
	Command string `json:"command"`
	// Retired marks a code that is no longer produced. It stays in the registry
	// so an old paste still resolves, and so the sequence stays dense.
	Retired bool `json:"retired,omitempty"`
}

var (
	errorCodesOnce sync.Once
	errorCodesByID map[string]ErrorCode
	errorCodesAll  []ErrorCode
)

func loadErrorCodes() {
	var doc struct {
		Codes []ErrorCode `json:"codes"`
	}
	// A malformed registry is a build-time mistake in this repo, not a condition
	// a user can reach, so it panics rather than degrading: shipping a binary
	// whose codes silently resolve to nothing is worse than not shipping.
	if err := json.Unmarshal(errorCodesJSON, &doc); err != nil {
		panic("errorcodes.json does not parse: " + err.Error())
	}
	errorCodesAll = doc.Codes
	errorCodesByID = make(map[string]ErrorCode, len(doc.Codes))
	for _, c := range doc.Codes {
		errorCodesByID[c.Code] = c
	}
}

// LookupErrorCode returns the registered failure for a code, and whether it is
// registered at all.
//
// Case-insensitive and tolerant of a missing prefix, because the input is
// something a person retyped from a terminal they have already closed:
// "drift-1006", "DRIFT-1006" and "1006" are the same question.
func LookupErrorCode(code string) (ErrorCode, bool) {
	errorCodesOnce.Do(loadErrorCodes)

	c, ok := errorCodesByID[normalizeErrorCode(code)]
	return c, ok
}

// errorCodesPage is the docs' troubleshooting page, where each code has a section
// whose id is the code in lower case.
const errorCodesPage = "https://docs.ondrift.eu/troubleshooting/error-codes/"

// ErrorCodePage is the address of a code's section on the docs, or "" for text that
// is not a registered code. It is derived from the code, so the registry holds no
// field for it and there is no second copy to drift. A retired code keeps its
// section, because a message from an older install can still name it. The address
// is printed and never fetched.
func ErrorCodePage(code string) string {
	entry, ok := LookupErrorCode(code)
	if !ok {
		return ""
	}
	return errorCodesPage + "#" + strings.ToLower(entry.Code)
}

// normalizeErrorCode puts what a person typed into the registry's spelling,
// "DRIFT-1006", whether they typed "drift-1006", " 1006 " or the code itself. It
// does not judge the result: "banana" becomes "DRIFT-BANANA".
func normalizeErrorCode(typed string) string {
	q := strings.ToUpper(strings.TrimSpace(typed))
	if !strings.HasPrefix(q, "DRIFT-") {
		q = "DRIFT-" + q
	}
	return q
}

// NewerErrorCode reports whether typed is shaped like a failure code and numbered
// above every registered one. That is the code a drift newer than this binary
// prints, and this binary has never heard of it. It returns the code in the
// registry's spelling and the highest code this binary registers, so the caller
// can say what this binary knows.
//
// Text that is not a code is never newer, and neither is a number at or below the
// highest.
func NewerErrorCode(typed string) (code, highest string, newer bool) {
	errorCodesOnce.Do(loadErrorCodes)

	code = normalizeErrorCode(typed)
	digits := strings.TrimPrefix(code, "DRIFT-")
	// Atoi reads a sign and a code has none, so only digits are let through to it.
	if strings.Trim(digits, "0123456789") != "" {
		return "", "", false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", "", false
	}

	top := -1
	for _, c := range errorCodesAll {
		if m := errorCodeNumber(c.Code); m > top {
			top, highest = m, c.Code
		}
	}
	return code, highest, n > top
}

// ErrorCodes returns every registered failure, in code order — retired ones
// included, because `doctor explain` must still answer for them.
func ErrorCodes() []ErrorCode {
	errorCodesOnce.Do(loadErrorCodes)
	out := make([]ErrorCode, len(errorCodesAll))
	copy(out, errorCodesAll)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// errorCodeNumber pulls the numeric part out of a code, or -1 if it is not
// shaped like one. NewerErrorCode uses it to find the highest registered code,
// and the registry's own tests use it to prove the sequence is dense.
func errorCodeNumber(code string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(code), "DRIFT-"))
	if err != nil {
		return -1
	}
	return n
}

// withCode appends the code to a rendered message, so the failure carries its
// own name and nobody has to discover a verb to use it.
//
// The trailing line names the command rather than only the code: a code with no
// instructions is a lookup the user has to guess the shape of. A message that
// already carries that line is returned as it is, so a code is never printed
// twice.
func withCode(message, code string) string {
	if code == "" {
		return message
	}
	line := fmt.Sprintf("  %s · drift doctor explain %s", code, code)
	if strings.Contains(message, "\n"+line) {
		return message
	}
	return message + "\n" + line
}

// Failure is an error that carries the code of a registered failure. Any package
// raises one with Failf, or gives an error it already has a code with Coded, and
// reads the code back with CodeOf.
//
// # One code line
//
// A failure renders as its message and then one line naming its code and the
// verb that explains it:
//
//	no copy of the Driftfile format yet.
//	  DRIFT-1008 · drift doctor explain DRIFT-1008
//
// It prints that line once however it travels. A failure wrapped by fmt.Errorf
// with %w keeps the one line it renders, so a wrap site adds its context and
// nothing else, and Coded returns an error that already carries a code as it is:
// the failure that was named first keeps its name, and the text a person pastes
// agrees with what CodeOf reads.
type Failure struct {
	code string
	err  error
}

// Failf raises a failure under a registered code, with its message formatted as
// fmt.Errorf formats it, %w included. An empty code raises an error with no code.
func Failf(code, format string, args ...any) error {
	return Coded(code, fmt.Errorf(format, args...))
}

// Coded gives err the code, keeping err reachable through errors.Is and
// errors.As. An error that already carries a code is returned as it is, and so is
// one given an empty code. A nil error stays nil.
func Coded(code string, err error) error {
	if err == nil || code == "" || CodeOf(err) != "" {
		return err
	}
	return &Failure{code: code, err: err}
}

// Error renders the message, then the code's line.
func (f *Failure) Error() string { return withCode(f.err.Error(), f.code) }

// Unwrap exposes the error the failure was raised from.
func (f *Failure) Unwrap() error { return f.err }

// FailureCode is the code the failure carries.
func (f *Failure) FailureCode() string { return f.code }

// coded is an error that names its failure code. A Failure is one, and so is an
// error whose code depends on what it holds, as an APIError's does on its status.
type coded interface{ FailureCode() string }

// CodeOf returns the code an error carries: the first one found walking the chain
// outermost first, through fmt.Errorf's %w and errors.Join. It returns "" for an
// error with no code and for nil.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	if c, ok := err.(coded); ok {
		if code := c.FailureCode(); code != "" {
			return code
		}
	}
	switch e := err.(type) {
	case interface{ Unwrap() error }:
		return CodeOf(e.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			if code := CodeOf(inner); code != "" {
				return code
			}
		}
	}
	return ""
}
