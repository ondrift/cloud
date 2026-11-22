package common

import (
	"strings"
	"testing"
)

// The version the CLI asks a new account to accept must be the exact string
// the platform checks `terms_version` against (drift-common/models.TermsVersion)
// — the two repositories cannot share a Go constant, so this pins the shape and
// the address a person reading this file can compare by hand against the
// platform's.
func TestTermsVersionAndURL(t *testing.T) {
	if TermsVersion == "" {
		t.Fatal("TermsVersion is empty — a signup would present no version to accept")
	}
	if !strings.HasPrefix(TermsURL, "https://docs.ondrift.eu/legal/terms/") {
		t.Errorf("TermsURL = %q, want the published terms page under docs.ondrift.eu", TermsURL)
	}
}
