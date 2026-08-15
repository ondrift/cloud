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
// That rule is mechanical rather than remembered: the sequence must be dense, so
// deleting an entry leaves a gap and the test fails. Reuse is impossible without
// first deleting, so the same check covers both.

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
	// Meaning is one line saying what happened, in the user's terms.
	Meaning string `json:"meaning"`
	// Remedy is what to do about it. Written for someone who is stuck, so it
	// names the command rather than describing it.
	Remedy string `json:"remedy"`
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

	q := strings.ToUpper(strings.TrimSpace(code))
	if !strings.HasPrefix(q, "DRIFT-") {
		q = "DRIFT-" + q
	}
	c, ok := errorCodesByID[q]
	return c, ok
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
// shaped like one. Used by the registry's own tests to prove the sequence is
// dense.
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
// instructions is a lookup the user has to guess the shape of.
func withCode(message, code string) string {
	if code == "" {
		return message
	}
	return fmt.Sprintf("%s\n  %s · drift doctor explain %s", message, code, code)
}
