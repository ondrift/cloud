package common

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// codeLineOf is the line a coded failure ends with.
func codeLineOf(code string) string {
	return "  " + code + " · drift doctor explain " + code
}

// A failure renders as its message and then its code's line on a line of its own,
// the form an API failure has always had.
func TestFailf_RendersTheMessageThenOneCodeLine(t *testing.T) {
	err := Failf("DRIFT-1008", "no copy of the format (%d tries)", 3)

	want := "no copy of the format (3 tries)\n" + codeLineOf("DRIFT-1008")
	if got := err.Error(); got != want {
		t.Errorf("the failure renders as\n%q\nwant\n%q", got, want)
	}
	if got := CodeOf(err); got != "DRIFT-1008" {
		t.Errorf("CodeOf reads %q, want DRIFT-1008", got)
	}
}

// The text of a failure that has no code is exactly its message.
func TestFailf_WithNoCodeRendersTheMessageAlone(t *testing.T) {
	err := Failf("", "plain %s", "words")
	if got := err.Error(); got != "plain words" {
		t.Errorf("an uncoded failure renders as %q", got)
	}
	if got := CodeOf(err); got != "" {
		t.Errorf("CodeOf reads %q from an uncoded failure", got)
	}
}

// Coded gives an existing error a code and keeps it reachable: its text is the
// error's text and the line, and errors.Is and errors.As still see through it.
func TestCoded_KeepsTheErrorItWraps(t *testing.T) {
	root := errors.New("disk is full")
	err := Coded("DRIFT-1010", root)

	if want := "disk is full\n" + codeLineOf("DRIFT-1010"); err.Error() != want {
		t.Errorf("renders as %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, root) {
		t.Error("errors.Is cannot reach the wrapped error")
	}
	if got := CodeOf(err); got != "DRIFT-1010" {
		t.Errorf("CodeOf reads %q, want DRIFT-1010", got)
	}
	if Coded("DRIFT-1010", nil) != nil {
		t.Error("coding no error is an error")
	}
}

// A coded failure wrapped with fmt.Errorf and %w prints one code line, however
// many wrappers it passes through, so a wrap site needs no change.
func TestACodedFailureWrappedWithFmtErrorfPrintsOneCodeLine(t *testing.T) {
	inner := Failf("DRIFT-1008", "no copy of the format")
	once := fmt.Errorf("Driftfile\n\n%w", inner)
	twice := fmt.Errorf("lint: %w", once)

	for name, err := range map[string]error{"once": once, "twice": twice} {
		if n := strings.Count(err.Error(), "DRIFT-1008 ·"); n != 1 {
			t.Errorf("%s: the code's line appears %d times:\n%s", name, n, err)
		}
		if got := CodeOf(err); got != "DRIFT-1008" {
			t.Errorf("%s: CodeOf reads %q, want DRIFT-1008", name, got)
		}
		if !strings.HasSuffix(err.Error(), codeLineOf("DRIFT-1008")) {
			t.Errorf("%s: the code's line is not last:\n%s", name, err)
		}
	}
}

// Coding an error that already says its code does not say it again, whether the
// code is the same one or the error already carries another: the failure that was
// named first keeps its name.
func TestCoded_NeverPrintsASecondCodeLine(t *testing.T) {
	inner := Failf("DRIFT-1008", "no copy of the format")

	same := Coded("DRIFT-1008", inner)
	other := Coded("DRIFT-1009", fmt.Errorf("while linting: %w", inner))
	typed := Coded("DRIFT-1009", &APIError{Op: "list slices", Status: http.StatusNotFound})
	text := Coded("DRIFT-1008", errors.New("no copy of the format\n"+codeLineOf("DRIFT-1008")))

	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"the same code on a failure":        {same, "DRIFT-1008"},
		"another code on a wrapped failure": {other, "DRIFT-1008"},
		"another code on an API failure":    {typed, "DRIFT-1003"},
		"the same code already in the text": {text, "DRIFT-1008"},
	} {
		if n := strings.Count(tc.err.Error(), "drift doctor explain"); n != 1 {
			t.Errorf("%s: %d code lines:\n%s", name, n, tc.err)
		}
		if got := CodeOf(tc.err); got != tc.code {
			t.Errorf("%s: CodeOf reads %q, want %q", name, got, tc.code)
		}
	}
}

// CodeOf reads the code out of any error in the chain: a typed failure, an API
// failure, one a wrapper holds, and nothing at all from an error with none.
func TestCodeOf_ReadsTheCodeOutOfAnyErrorChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"a plain error", errors.New("boom"), ""},
		{"a failure", Failf("DRIFT-1010", "x"), "DRIFT-1010"},
		{"an API failure", &APIError{Op: "get", Status: http.StatusForbidden}, "DRIFT-1002"},
		{"an API failure with no code", &APIError{Op: "verify", Status: http.StatusUnauthorized, CredentialRefused: true}, ""},
		{"an API failure no other status names", &APIError{Op: "get", Status: http.StatusTeapot}, "DRIFT-1016"},
		{"a failure under %w", fmt.Errorf("a: %w", Failf("DRIFT-1010", "x")), "DRIFT-1010"},
		{"a failure under errors.Join", errors.Join(errors.New("first"), Failf("DRIFT-1010", "x")), "DRIFT-1010"},
		{"a transport failure", TransportError("get", errors.New("connection refused")), "DRIFT-1006"},
	} {
		if got := CodeOf(tc.err); got != tc.want {
			t.Errorf("%s: CodeOf reads %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The code a failure carries is one the registry knows: a constructor given a
// registered code builds a failure whose entry `drift doctor explain` finds.
func TestFailf_CarriesACodeTheRegistryExplains(t *testing.T) {
	for _, entry := range ErrorCodes() {
		got := CodeOf(Failf(entry.Code, "x"))
		if _, ok := LookupErrorCode(got); !ok {
			t.Errorf("%s: CodeOf reads %q, which the registry does not know", entry.Code, got)
		}
	}
}
