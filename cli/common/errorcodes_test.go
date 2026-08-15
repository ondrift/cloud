package common

import (
	"strings"
	"testing"
)

// THE test the registry exists for.
//
// A code is minted once and never reused, renumbered or deleted, because a code
// pasted from a chat log a year old must still resolve to the same failure. That
// rule cannot be enforced by remembering it, so it is enforced by DENSITY: the
// numbers run 1..N with no gaps, which means removing an entry fails here, and
// reuse is impossible without first removing.
//
// To stop producing a code, set `retired: true` and leave it in place.
func TestErrorCodes_TheSequenceIsDenseSoNoneCanBeDeletedOrReused(t *testing.T) {
	codes := ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so every failure is anonymous again")
	}

	seen := map[int]string{}
	for _, c := range codes {
		n := errorCodeNumber(c.Code)
		if n < 0 {
			t.Errorf("%q is not shaped like a code (want DRIFT-<number>)", c.Code)
			continue
		}
		if prev, dup := seen[n]; dup {
			t.Errorf("DRIFT-%d is registered twice (%q and %q) — a reused code resolves to "+
				"the wrong failure for everyone holding the old one", n, prev, c.Code)
		}
		seen[n] = c.Code
	}

	// Dense from the first code to the last. A gap means an entry was removed,
	// and a removed entry is a number free to be minted again.
	lo, hi := 1<<30, 0
	for n := range seen {
		if n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	for n := lo; n <= hi; n++ {
		if _, ok := seen[n]; !ok {
			t.Errorf("DRIFT-%d is missing from the sequence — a code is never deleted, "+
				"only retired, or the number becomes free to reuse", n)
		}
	}
}

// A code with no remedy is a name for a problem and nothing else, which is the
// state this registry was built to leave.
func TestErrorCodes_EveryEntrySaysWhatHappenedAndWhatToDo(t *testing.T) {
	for _, c := range ErrorCodes() {
		if strings.TrimSpace(c.Meaning) == "" {
			t.Errorf("%s has no meaning", c.Code)
		}
		if strings.TrimSpace(c.Remedy) == "" {
			t.Errorf("%s has no remedy — a code that only names the failure leaves the "+
				"user exactly where they were", c.Code)
		}
	}
}

// The input is something a person retyped from a terminal they have already
// closed, so the lookup must not be pedantic about how they wrote it.
func TestLookupErrorCode_AcceptsHowAPersonWouldType(t *testing.T) {
	want, ok := LookupErrorCode("DRIFT-1001")
	if !ok {
		t.Fatal("DRIFT-1001 is registered and must resolve")
	}
	for _, spelling := range []string{"drift-1001", "Drift-1001", "1001", "  DRIFT-1001  "} {
		got, ok := LookupErrorCode(spelling)
		if !ok {
			t.Errorf("%q did not resolve", spelling)
			continue
		}
		if got.Code != want.Code {
			t.Errorf("%q resolved to %s, want %s", spelling, got.Code, want.Code)
		}
	}
}

// The control: an unregistered code must NOT resolve. Without this the test
// above would pass for a lookup that returns something for any input.
func TestLookupErrorCode_AnUnknownCodeDoesNotResolve(t *testing.T) {
	for _, unknown := range []string{"DRIFT-9999", "banana", ""} {
		if _, ok := LookupErrorCode(unknown); ok {
			t.Errorf("%q resolved, but nothing is registered under it", unknown)
		}
	}
}

// The failure carries its own name, so nobody has to already know the verb.
func TestWithCode_PrintsTheCodeAndHowToExpandIt(t *testing.T) {
	got := withCode("Couldn't create slice: your session expired.", "DRIFT-1001")
	if !strings.Contains(got, "DRIFT-1001") {
		t.Errorf("the code must appear in the message, got: %s", got)
	}
	if !strings.Contains(got, "drift doctor explain DRIFT-1001") {
		t.Errorf("the message must name the command that expands it, got: %s", got)
	}
	if !strings.HasPrefix(got, "Couldn't create slice") {
		t.Errorf("the original message must lead, got: %s", got)
	}
}

// A failure with no registered code renders exactly as it did before. The
// registry is added to over time, and a path that has not been named yet must
// not grow an empty suffix.
func TestWithCode_LeavesAnUncodedMessageAlone(t *testing.T) {
	const msg = "Couldn't do the thing: reasons."
	if got := withCode(msg, ""); got != msg {
		t.Errorf("an uncoded message must be untouched, got: %s", got)
	}
}

// A failure the platform returned must carry its name, or the registry is a file
// nobody reaches. Keyed on STATUS rather than wording, so improving a message
// cannot silently drop the code.
func TestAPIError_CarriesItsCode(t *testing.T) {
	// A 5xx consults component health, so without a stub this test would reach
	// the real status page: slow, and dependent on the platform being up to
	// assert something that has nothing to do with it.
	withStatusFeed(t, 200, feedOK)

	for _, tc := range []struct {
		name   string
		err    APIError
		want   string
		absent bool
	}{
		{name: "expired session", err: APIError{Op: "list slices", Status: 401}, want: "DRIFT-1001"},
		{name: "forbidden", err: APIError{Op: "delete slice", Status: 403}, want: "DRIFT-1002"},
		{name: "not found", err: APIError{Op: "delete atomic function", Status: 404}, want: "DRIFT-1003"},
		{name: "quota", err: APIError{Op: "deploy", Status: 429}, want: "DRIFT-1004"},
		{name: "platform fault", err: APIError{Op: "deploy", Status: 500}, want: "DRIFT-1005"},
		// A refused LOGIN is not an expired session. Pointing at `account login`
		// would send the user back to the command they just ran.
		{name: "refused login", err: APIError{Op: "log in", Status: 401}, absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if tc.absent {
				if strings.Contains(got, "DRIFT-") {
					t.Errorf("this failure must carry no code, got: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %s in the message, got: %s", tc.want, got)
			}
			if _, ok := LookupErrorCode(tc.want); !ok {
				t.Errorf("%s is rendered but not registered, so `doctor explain` cannot answer it", tc.want)
			}
		})
	}
}

// Every code the CLI can PRINT must be registered, or a user pastes one that
// `doctor explain` does not know. This walks the statuses the renderer maps and
// checks each resolves — the half that would otherwise be caught only by a user.
func TestAPIError_EveryRenderedCodeIsRegistered(t *testing.T) {
	for status := 400; status < 600; status++ {
		e := APIError{Op: "do the thing", Status: status}
		code := e.code()
		if code == "" {
			continue
		}
		if _, ok := LookupErrorCode(code); !ok {
			t.Errorf("status %d renders %s, which is not in the registry", status, code)
		}
	}
}
