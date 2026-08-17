package project

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// prorataFunctions is the flagship workload's nineteen identities, in the
// retired spelling. Thirteen of them carry a path segment or a route parameter,
// which is the whole reason the identity cannot be a bare slug.
var prorataFunctions = []string{
	"post:auth/challenge", "post:auth/verify", "get:auth/me",
	"post:groups", "get:groups/:id", "post:groups/join", "post:groups/delete",
	"get:my-groups", "post:vault",
	"post:link/begin", "post:link/session", "post:link/qr",
	"post:expense", "post:settle", "get:ledger",
	"post:ops", "get:ops-since", "get:proof", "post:receipt",
}

// The invariant the whole card rests on: the string the platform books against
// must come out byte-identical whichever spelling produced it.
//
// It is asserted as a SET, not in order. The two real prorata manifests genuinely
// differ in order — the current file ends `post:ops, get:ops-since, get:proof,
// post:receipt` while the ported specimen groups them differently — so an
// order-sensitive assertion would fail against the file it exists to prove.
func TestFunctionIdentity_BothSpellingsProduceTheSameBookingKeys(t *testing.T) {
	fromRetired := bookingKeys(t, driftfileRetired(prorataFunctions))
	fromCurrent := bookingKeys(t, driftfileCurrent(prorataFunctions))

	if len(fromCurrent) != len(prorataFunctions) {
		t.Fatalf("got %d identities, want %d", len(fromCurrent), len(prorataFunctions))
	}
	if strings.Join(fromRetired, "\n") != strings.Join(fromCurrent, "\n") {
		t.Errorf("the two spellings book different keys.\nretired:\n%s\ncurrent:\n%s\n"+
			"a key the slice's pool map does not hold falls back to the shared pool "+
			"with no error, so a difference here unbooks functions silently",
			strings.Join(fromRetired, "\n"), strings.Join(fromCurrent, "\n"))
	}
}

// A route carrying colons of its own is the case a split on the LAST colon, or a
// naive SplitN(3), gets wrong.
func TestFunctionIdentity_ARouteKeepsItsOwnColons(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"name": "get:groups/:id"},
		map[string]any{"name": "delete:items/:id/tags/:tag"},
	}}}
	normaliseFunctionIdentities(doc)

	fns := doc.Nodes("atomic", "functions")
	for i, want := range []struct{ method, route string }{
		{"get", "groups/:id"},
		{"delete", "items/:id/tags/:tag"},
	} {
		if got := fns[i].Str("method"); got != want.method {
			t.Errorf("entry %d method = %q, want %q", i, got, want.method)
		}
		if got := fns[i].Str("route"); got != want.route {
			t.Errorf("entry %d route = %q, want %q", i, got, want.route)
		}
	}
}

// The pair is the current spelling, so a document carrying both means the pair.
// Deriving from the retired string instead would let the old name win.
func TestFunctionIdentity_ThePairWinsWhenADocumentStatesBoth(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"name": "get:stale", "route": "fresh", "method": "post"},
	}}}
	normaliseFunctionIdentities(doc)

	if got := doc.Nodes("atomic", "functions")[0].Str("name"); got != "post:fresh" {
		t.Errorf("name = %q, want post:fresh — the pair is what the document means", got)
	}
}

// The method is lowercased into the key because the runtime's lookup is exact
// against a lowercase-method key.
func TestFunctionIdentity_TheMethodIsLowercasedIntoTheKey(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"route": "ping", "method": "GET"},
	}}}
	normaliseFunctionIdentities(doc)

	if got := doc.Nodes("atomic", "functions")[0].Str("name"); got != "get:ping" {
		t.Errorf("name = %q, want get:ping — a key stored with an uppercase method "+
			"names a pool no invocation reaches", got)
	}
}

// A queue function written as the pair must be indistinguishable downstream from
// one written as `queue:orders`, because Trigger() and QueueSource() read the
// composed string and neither was repointed.
func TestFunctionIdentity_AQueuePairIsTheSameFunctionAsTheRetiredForm(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"route": "orders", "method": "queue", "handler": "H"},
	}}}
	normaliseFunctionIdentities(doc)

	spec := atomic_cmd.FunctionSpec{Name: doc.Nodes("atomic", "functions")[0].Str("name")}
	if spec.Trigger() != "queue" {
		t.Errorf("Trigger() = %q, want queue", spec.Trigger())
	}
	if spec.QueueSource() != "orders" {
		t.Errorf("QueueSource() = %q, want orders", spec.QueueSource())
	}
}

// One notice per run, however many entries use the retired spelling. Prorata
// declares nineteen and hears about it once.
func TestFunctionIdentity_TheRetiredSpellingIsAnnouncedExactlyOnce(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	bookingKeys(t, driftfileRetired(prorataFunctions))

	if n := strings.Count(notices.String(), "atomic.functions[].name"); n != 1 {
		t.Errorf("the retired spelling was announced %d times across %d entries, want 1:\n%s",
			n, len(prorataFunctions), notices.String())
	}
	if !strings.Contains(notices.String(), "route") || !strings.Contains(notices.String(), "method") {
		t.Errorf("the notice must name both replacements, got:\n%s", notices.String())
	}
}

// The current spelling is not deprecated, so it says nothing at all.
func TestFunctionIdentity_TheCurrentSpellingIsSilent(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	bookingKeys(t, driftfileCurrent(prorataFunctions))

	if strings.Contains(notices.String(), "atomic.functions[].name") {
		t.Errorf("a Driftfile using route/method was told about a key it does not "+
			"declare:\n%s", notices.String())
	}
}

// bookingKeys parses a Driftfile and returns its function identities, sorted, so
// callers compare sets rather than orderings.
func bookingKeys(t *testing.T, body string) []string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	var out []string
	for _, fn := range m.Slice().Nodes("atomic", "functions") {
		out = append(out, fn.Str("name"))
	}
	sort.Strings(out)
	return out
}

// driftfileRetired writes each identity as a single `name:`.
func driftfileRetired(identities []string) string {
	var b strings.Builder
	b.WriteString("name: prorata\natomic:\n  functions:\n")
	for i, id := range identities {
		b.WriteString("    - name: \"" + id + "\"\n")
		b.WriteString("      handler: H" + string(rune('A'+i%26)) + "\n")
		b.WriteString("      memory: 16MB\n")
	}
	return b.String()
}

// driftfileCurrent writes each identity as the `route` + `method` pair, and
// books no memory — the pair's required set does not include it.
func driftfileCurrent(identities []string) string {
	var b strings.Builder
	b.WriteString("name: prorata\natomic:\n  functions:\n")
	for i, id := range identities {
		method, route, _ := strings.Cut(id, ":")
		b.WriteString("    - route: \"" + route + "\"\n")
		b.WriteString("      method: " + method + "\n")
		b.WriteString("      handler: H" + string(rune('A'+i%26)) + "\n")
	}
	return b.String()
}
