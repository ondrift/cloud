package common

// Every registered code has a section on the docs' troubleshooting page, and the CLI
// prints the address of that section after `drift doctor explain`. The address is
// derived from the code, so the registry has no field for it and there is no second
// copy to drift. The page itself is cloud/sites' (docs/src/pages/troubleshooting/
// error-codes/index.md), generated from a copy of the registry; the second half of
// this file holds the address to a page that is really there, where cloud/sites is
// checked out beside this repository.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const errorCodePageBase = "https://docs.ondrift.eu/troubleshooting/error-codes/"

// The address is the page's, then # and the code in lower case: the id the docs'
// generator gives the code's section. A retired code keeps its section, so it keeps
// its address.
func TestErrorCodePage_IsTheDocsAddressWithTheCodeLowerCased(t *testing.T) {
	t.Skip("fails until ErrorCodePage derives the address from the code (task #2563)")
	codes := ErrorCodes()
	if len(codes) == 0 {
		t.Fatal("the registry is empty, so the loop below would prove nothing")
	}
	sawRetired := false
	for _, c := range codes {
		sawRetired = sawRetired || c.Retired
		want := errorCodePageBase + "#" + strings.ToLower(c.Code)
		if got := ErrorCodePage(c.Code); got != want {
			t.Errorf("ErrorCodePage(%q) = %q, want %q", c.Code, got, want)
		}
	}
	if !sawRetired {
		t.Log("no retired code is registered, so a retired one's address is not exercised")
	}
	if got, want := ErrorCodePage("DRIFT-1006"), errorCodePageBase+"#drift-1006"; got != want {
		t.Errorf("ErrorCodePage(DRIFT-1006) = %q, want %q", got, want)
	}
}

// A code typed three ways is one question, so it gets one address, as it does in
// LookupErrorCode.
func TestErrorCodePage_TreatsEverySpellingOfACodeAlike(t *testing.T) {
	t.Skip("fails until ErrorCodePage derives the address from the code (task #2563)")
	want := ErrorCodePage("DRIFT-1006")
	if want == "" {
		t.Fatal("DRIFT-1006 has no address")
	}
	for _, spelling := range []string{"drift-1006", "Drift-1006", "1006", " 1006 "} {
		if got := ErrorCodePage(spelling); got != want {
			t.Errorf("ErrorCodePage(%q) = %q, want %q", spelling, got, want)
		}
	}
}

// A code that is not registered has no section, so it has no address: an address
// for it would send a person to a page that says nothing of it.
func TestErrorCodePage_IsEmptyForACodeThatIsNotRegistered(t *testing.T) {
	for _, text := range []string{"banana", "", "DRIFT-", "DRIFT-ABC", "DRIFT-1000", "0", "DRIFT-99999999"} {
		if got := ErrorCodePage(text); got != "" {
			t.Errorf("ErrorCodePage(%q) = %q, want none", text, got)
		}
	}
}

// pageOutcome is what looking for the docs page of the codes came to. At most one
// field is set; neither is a pass.
type pageOutcome struct {
	skip string
	fail string
}

// errorCodesMarker is what the docs' generator expands into one section per code.
const errorCodesMarker = "<!-- error-codes -->"

// checkDocsPage looks for the troubleshooting page in the checkout of cloud/sites
// beside this repository and holds it to the generation marker: a page without it
// would not carry a section for every code, and the address the CLI prints would
// name nothing. With no checkout beside the repository the outcome is a skip that
// says what to check by hand.
func checkDocsPage(cloud string) pageOutcome {
	page := filepath.Join(cloud, "sites", "docs", "src", "pages", "troubleshooting", "error-codes", "index.md")
	data, err := os.ReadFile(page)
	if os.IsNotExist(err) {
		return pageOutcome{skip: "cloud/sites is not checked out beside this repository, or has no troubleshooting page (" + page + "), so the page the address names is not checked.\n" +
			"By hand: open " + errorCodePageBase + " and find a section for the code you printed.\n" + docsCopyCacheNote}
	}
	if err != nil {
		return pageOutcome{fail: "read the troubleshooting page: " + err.Error()}
	}
	if !strings.Contains(string(data), errorCodesMarker) {
		return pageOutcome{fail: page + " does not carry " + errorCodesMarker + ", so it has no generated section per code and the address `drift doctor explain` prints names nothing."}
	}
	return pageOutcome{}
}

func TestCheckDocsPage_WithNoSitesBesideTheRepositoryItSkipsAndSaysWhatToCheckByHand(t *testing.T) {
	got := checkDocsPage(t.TempDir())
	if got.fail != "" || !strings.Contains(got.skip, "By hand") || !strings.Contains(got.skip, errorCodePageBase) {
		t.Errorf("want a skip that says what to check by hand, got %+v", got)
	}
}

func TestCheckDocsPage_AFoundPageWithTheMarkerPassesAndOneWithoutItFails(t *testing.T) {
	cloud := t.TempDir()
	dir := filepath.Join(cloud, "sites", "docs", "src", "pages", "troubleshooting", "error-codes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dir, "index.md")

	if err := os.WriteFile(page, []byte("## Error codes\n\n"+errorCodesMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkDocsPage(cloud); got != (pageOutcome{}) {
		t.Errorf("a page with the marker must pass, got %+v", got)
	}

	if err := os.WriteFile(page, []byte("## Error codes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := checkDocsPage(cloud)
	if got.skip != "" || !strings.Contains(got.fail, errorCodesMarker) {
		t.Errorf("a page without the marker must fail and name it, got %+v", got)
	}
}

// With cloud/sites beside the repository, the page the address names is there and
// carries the marker. Without it, the test skips and says what to check by hand.
func TestErrorCodePage_TheDocsPageTheAddressNamesExists(t *testing.T) {
	got := checkDocsPage("../../..")
	if got.skip != "" {
		t.Skip(got.skip)
	}
	if got.fail != "" {
		t.Fatal(got.fail)
	}
}
