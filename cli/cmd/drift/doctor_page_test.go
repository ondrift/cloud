package main

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// `drift doctor explain <code>` ends with the address of the code's page on the docs,
// for every registered code, retired ones included, in the form
//
//	more:  <address>
//
// The address is printed and never fetched, so it works with no session and no
// network: the test runs with a trap that fails on any request.
func TestDoctorExplainEndsWithTheAddressOfTheCodesPage(t *testing.T) {
	t.Skip("fails until explain prints the address (task #2564)")
	codes := common.ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so the loop below would prove nothing")
	}
	requests := noNetwork(t, t.Errorf)

	for _, c := range codes {
		t.Run(c.Code, func(t *testing.T) {
			stdout, stderr, status := runTree("doctor", "explain", c.Code)
			if status != 0 || stderr != "" {
				t.Fatalf("explain %s exited %d with stderr %q", c.Code, status, stderr)
			}
			lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
			last := strings.TrimSpace(lines[len(lines)-1])
			want := "more:  " + common.ErrorCodePage(c.Code)
			if last != want {
				t.Errorf("explain %s ends with %q, want %q; it printed:\n%s", c.Code, last, want, stdout)
			}
			if !strings.HasPrefix(common.ErrorCodePage(c.Code), "https://docs.ondrift.eu/troubleshooting/error-codes/#drift-") {
				t.Errorf("the address of %s is %q", c.Code, common.ErrorCodePage(c.Code))
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("%d request(s) arrived while explaining %d codes, want none", n, len(codes))
	}
}

// The address comes after the fix line, whether there is a command or not, and after
// the note on a retired code.
func TestDoctorExplainPutsTheAddressAfterTheFixAndTheRetiredNote(t *testing.T) {
	t.Skip("fails until explain prints the address (task #2564)")
	noNetwork(t, t.Errorf)
	for _, c := range common.ErrorCodes() {
		stdout, _, _ := runTree("doctor", "explain", c.Code)
		at := strings.Index(stdout, "more:")
		if at < 0 {
			t.Errorf("explain %s prints no address", c.Code)
			continue
		}
		for _, before := range []string{"fix:", "There is no command you can run for this one.", "This code is retired"} {
			if i := strings.Index(stdout, before); i >= 0 && i > at {
				t.Errorf("explain %s prints %q after the address", c.Code, before)
			}
		}
	}
}

// A code that is not registered keeps the message it has, on standard error, and
// prints no address.
func TestDoctorExplainPrintsNoAddressForACodeThatIsNotRegistered(t *testing.T) {
	noNetwork(t, t.Errorf)
	stdout, stderr, status := runTree("doctor", "explain", "banana")
	if status != 1 || stdout != "" {
		t.Fatalf("explain banana exited %d with stdout %q", status, stdout)
	}
	if strings.Contains(stderr, "docs.ondrift.eu") || strings.Contains(stderr, "more:") {
		t.Errorf("the not-registered message names an address: %q", stderr)
	}
}
