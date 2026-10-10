package project

import (
	"sort"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// prorataFunctions is the flagship workload's nineteen identities, as the
// booking keys the platform holds them under. Thirteen of them carry a path
// segment or a route parameter, which is the whole reason the identity cannot
// be a bare slug.
var prorataFunctions = []string{
	"post:auth/challenge", "post:auth/verify", "get:auth/me",
	"post:groups", "get:groups/:id", "post:groups/join", "post:groups/delete",
	"get:my-groups", "post:vault",
	"post:link/begin", "post:link/session", "post:link/qr",
	"post:expense", "post:settle", "get:ledger",
	"post:ops", "get:ops-since", "get:proof", "post:receipt",
}

// The invariant the whole card rests on: a Pulse labelled "METHOD /route" must
// book under exactly the string the slice composes, lower(method) + ":" + route.
//
// It is asserted as a SET, not in order, so a reordering of the file cannot fail
// it for a reason that is not about the identity.
func TestFunctionIdentity_ALabelBooksTheKeyTheSliceComposes(t *testing.T) {
	withRealSchema(t)
	got := bookingKeys(t, driftfileFor(prorataFunctions))

	want := append([]string(nil), prorataFunctions...)
	sort.Strings(want)
	if len(got) != len(prorataFunctions) {
		t.Fatalf("got %d identities, want %d", len(got), len(prorataFunctions))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the labels book different keys.\ngot:\n%s\nwant:\n%s\n"+
			"a key the slice's pool map does not hold falls back to the shared pool "+
			"with no error, so a difference here unbooks functions silently",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A route carrying colons of its own is the case a split on the LAST colon, or a
// naive SplitN(3), gets wrong: the route is composed whole, never parsed back.
func TestFunctionIdentity_ARouteKeepsItsOwnColons(t *testing.T) {
	doc := Node{"atomic": map[string]any{"pulses": []any{
		map[string]any{"route": "groups/:id", "method": "get"},
		map[string]any{"route": "items/:id/tags/:tag", "method": "delete"},
	}}}
	normaliseFunctionIdentities(doc)

	fns := doc.Nodes("atomic", "pulses")
	for i, want := range []string{"get:groups/:id", "delete:items/:id/tags/:tag"} {
		if got := fns[i].Str("name"); got != want {
			t.Errorf("entry %d name = %q, want %q", i, got, want)
		}
		if got := fns[i].Str("route"); got != strings.SplitN(want, ":", 2)[1] {
			t.Errorf("entry %d route = %q, which composing the name changed", i, got)
		}
	}
}

// The pair is what the document means, so a document carrying a stale name as
// well means the pair. Deriving from the name instead would let the old string
// win.
func TestFunctionIdentity_ThePairWinsWhenADocumentStatesBoth(t *testing.T) {
	doc := Node{"atomic": map[string]any{"pulses": []any{
		map[string]any{"name": "get:stale", "route": "fresh", "method": "post"},
	}}}
	normaliseFunctionIdentities(doc)

	if got := doc.Nodes("atomic", "pulses")[0].Str("name"); got != "post:fresh" {
		t.Errorf("name = %q, want post:fresh — the pair is what the document means", got)
	}
}

// The method is lowercased into the key because the runtime's lookup is exact
// against a lowercase-method key.
func TestFunctionIdentity_TheMethodIsLowercasedIntoTheKey(t *testing.T) {
	doc := Node{"atomic": map[string]any{"pulses": []any{
		map[string]any{"route": "ping", "method": "GET"},
	}}}
	normaliseFunctionIdentities(doc)

	if got := doc.Nodes("atomic", "pulses")[0].Str("name"); got != "get:ping" {
		t.Errorf("name = %q, want get:ping — a key stored with an uppercase method "+
			"names a pool no invocation reaches", got)
	}
}

// A queue Pulse is the pair with the method `queue`, and Trigger() and
// QueueSource() read the composed string, so the composition has to be the one
// they parse.
func TestFunctionIdentity_AQueuePairComposesTheQueueKey(t *testing.T) {
	doc := Node{"atomic": map[string]any{"pulses": []any{
		map[string]any{"route": "orders", "method": "queue", "handler": "H"},
	}}}
	normaliseFunctionIdentities(doc)

	spec := atomic_cmd.FunctionSpec{Name: doc.Nodes("atomic", "pulses")[0].Str("name")}
	if spec.Trigger() != "queue" {
		t.Errorf("Trigger() = %q, want queue", spec.Trigger())
	}
	if spec.QueueSource() != "orders" {
		t.Errorf("QueueSource() = %q, want orders", spec.QueueSource())
	}
}

// bookingKeys parses a Driftfile and returns its function identities, sorted, so
// callers compare sets rather than orderings. The default element's folder
// exists beside it.
func bookingKeys(t *testing.T, body string) []string {
	t.Helper()
	m, err := ParseDriftfile(writeDriftfile(t, body, "atomic/"))
	if err != nil {
		t.Fatalf("parse failed:\n%v", err)
	}
	var out []string
	for _, fn := range m.Slice().Nodes("atomic", "pulses") {
		out = append(out, fn.Str("name"))
	}
	sort.Strings(out)
	return out
}

// driftfileFor writes each identity as a Pulse labelled with its method and
// route, in the default element.
func driftfileFor(identities []string) string {
	var b strings.Builder
	b.WriteString("slice \"prorata\" { billing = \"monthly\" }\n\nelement \"default\" {\n")
	for i, id := range identities {
		method, route, _ := strings.Cut(id, ":")
		b.WriteString("  pulse \"" + strings.ToUpper(method) + " /" + route + "\" {\n")
		b.WriteString("    handler = \"H" + string(rune('A'+i%26)) + "\"\n")
		b.WriteString("    memory = \"16MiB\"\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}
