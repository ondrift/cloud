package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// walkFor runs the deprecation pass over a document and returns what a user
// would have seen on stderr.
func walkFor(t *testing.T, doc Node, keys []common.KeyDeprecation) string {
	t.Helper()
	var out bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&out)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	common.ResetDeprecationState()

	applyKeyDeprecations(doc, keys)
	return out.String()
}

func ignored(path string) common.KeyDeprecation {
	return common.KeyDeprecationFor(path, common.KeyIgnored, common.Deprecation{
		RemoveAfter: "no reachable Driftfile still writes it",
		Because:     "A slice's shape is chosen on the form `drift slice resize` draws.",
	})
}

func alias(oldPath, newPath string) common.KeyDeprecation {
	return common.KeyDeprecationFor(oldPath, common.KeyAlias, common.Deprecation{New: newPath})
}

// One notice per PATH, not per occurrence. Prorata declares `memory` nineteen
// times; a person renaming one thing should hear about it once.
func TestOnePathYieldsOneNoticeHoweverOftenItOccurs(t *testing.T) {
	fns := make([]any, 19)
	for i := range fns {
		fns[i] = map[string]any{"name": "post:x", "memory": "16MB"}
	}
	doc := Node{"atomic": map[string]any{"functions": fns}}

	got := walkFor(t, doc, []common.KeyDeprecation{ignored("atomic.functions[].memory")})

	if n := strings.Count(got, "atomic.functions[].memory"); n != 1 {
		t.Errorf("the path was announced %d times across nineteen occurrences, want 1:\n%s", n, got)
	}
}

// Parsing twice in one process still says it once — the registry's rule, which
// this reuses rather than reimplements.
func TestASecondParseInTheSameProcessIsSilent(t *testing.T) {
	doc := Node{"log_retention": "72h"}
	keys := []common.KeyDeprecation{ignored("log_retention")}

	var out bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&out)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	common.ResetDeprecationState()

	applyKeyDeprecations(doc, keys)
	applyKeyDeprecations(doc, keys)

	if n := strings.Count(out.String(), "log_retention"); n != 1 {
		t.Errorf("two parses produced %d notices, want 1:\n%s", n, out.String())
	}
}

// A document using only current spellings hears nothing. A deprecation notice on
// a file that does not use the old name is noise, and noise is how people learn
// to stop reading these.
func TestACurrentDocumentSaysNothing(t *testing.T) {
	doc := Node{"atomic": map[string]any{"functions": []any{
		map[string]any{"name": "post:x"},
	}}}

	if got := walkFor(t, doc, []common.KeyDeprecation{ignored("atomic.functions[].memory")}); got != "" {
		t.Errorf("a current document produced a notice:\n%s", got)
	}
}

// An IGNORED key keeps its value. Nothing downstream reads it and the schema
// still accepts it, so removing it here would be a second behaviour nobody asked
// for — and would change what `drift file lint` sees.
func TestAnIgnoredKeyIsLeftWhereItIs(t *testing.T) {
	doc := Node{"log_retention": "72h"}
	walkFor(t, doc, []common.KeyDeprecation{ignored("log_retention")})

	if doc.Str("log_retention") != "72h" {
		t.Errorf("the value was disturbed: %v", doc["log_retention"])
	}
}

// An ALIAS rewrites in place, so exactly one spelling reaches every reader. That
// is what makes the old name a thin alias rather than a second implementation.
func TestAnAliasRewritesSoOneSpellingReachesReaders(t *testing.T) {
	doc := Node{"canvas": map[string]any{"sites": []any{
		map[string]any{"dir": "./canvas", "route": "/app"},
	}}}

	walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "canvas.sites[].path")})

	site := doc.Nodes("canvas", "sites")[0]
	if site.Str("path") != "/app" {
		t.Errorf("the new spelling did not receive the value: %v", site)
	}
	if site.Has("route") {
		t.Errorf("the old spelling survived, so two spellings reach readers: %v", site)
	}
}

// An alias must not overwrite a value the document already states under the new
// name. A file carrying both is contradicting itself, and the new spelling is
// the one it means.
func TestAnAliasDoesNotOverwriteTheNewSpelling(t *testing.T) {
	doc := Node{"canvas": map[string]any{"sites": []any{
		map[string]any{"dir": "./canvas", "route": "/old", "path": "/new"},
	}}}

	walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "canvas.sites[].path")})

	site := doc.Nodes("canvas", "sites")[0]
	if site.Str("path") != "/new" {
		t.Errorf("the declared new spelling was overwritten by the old one: %v", site)
	}
}

// ---------- aliases that move a key to a different parent ----------

// A key can move SECTIONS, not only be renamed in place: `atomic.egress` lives
// under `edge` now. The value lands at the new path and the old key is gone, so
// readers see one spelling.
func TestAnAliasMovesAKeyToADifferentParent(t *testing.T) {
	doc := Node{"atomic": map[string]any{
		"egress":    map[string]any{"mode": "allowlist", "hosts": []any{"api.stripe.com"}},
		"functions": []any{map[string]any{"route": "x"}},
	}}

	walkFor(t, doc, []common.KeyDeprecation{alias("atomic.egress", "edge.egress")})

	if doc.Str("edge", "egress", "mode") != "allowlist" {
		t.Errorf("the value did not arrive at edge.egress: %v", doc)
	}
	if doc.Has("atomic", "egress") {
		t.Errorf("atomic.egress survived the move, so two spellings reach readers: %v", doc)
	}
	if len(doc.List("atomic", "functions")) != 1 {
		t.Errorf("the move disturbed the old parent's other keys: %v", doc["atomic"])
	}
}

// A top-level key can move under a section that the document does not have yet.
// The section is created, because "no edge: block" is the commonest case for an
// old file.
func TestAnAliasMovesATopLevelKeyIntoASectionItCreates(t *testing.T) {
	doc := Node{"domains": []any{map[string]any{"host": "app.example.com"}}}

	walkFor(t, doc, []common.KeyDeprecation{alias("domains", "edge.domains")})

	if got := doc.Nodes("edge", "domains"); len(got) != 1 || got[0].Str("host") != "app.example.com" {
		t.Errorf("the domains did not arrive at edge.domains: %v", doc)
	}
	if doc.Has("domains") {
		t.Errorf("top-level domains survived the move: %v", doc)
	}
}

// Moving into a section the document already has keeps that section's other
// keys: an old `domains:` beside a new `edge.rate_limit` is one edge block.
func TestAMoveIntoAnExistingSectionKeepsItsOtherKeys(t *testing.T) {
	doc := Node{
		"domains": []any{map[string]any{"host": "app.example.com"}},
		"edge":    map[string]any{"rate_limit": "600/min"},
	}

	walkFor(t, doc, []common.KeyDeprecation{alias("domains", "edge.domains")})

	if doc.Str("edge", "rate_limit") != "600/min" {
		t.Errorf("the move replaced the edge section rather than adding to it: %v", doc["edge"])
	}
	if len(doc.Nodes("edge", "domains")) != 1 {
		t.Errorf("the domains did not arrive beside rate_limit: %v", doc["edge"])
	}
}

// Both spellings across parents: the new one wins, the old one is dropped, and
// the person is TOLD, because silently discarding a value they wrote leaves them
// believing it is in force.
func TestAMovedAliasDoesNotOverwriteTheNewSpellingAndSaysSo(t *testing.T) {
	doc := Node{
		"atomic": map[string]any{"egress": map[string]any{"mode": "open"}},
		"edge":   map[string]any{"egress": map[string]any{"mode": "allowlist"}},
	}

	got := walkFor(t, doc, []common.KeyDeprecation{alias("atomic.egress", "edge.egress")})

	if doc.Str("edge", "egress", "mode") != "allowlist" {
		t.Errorf("the declared new spelling was overwritten by the old one: %v", doc["edge"])
	}
	if doc.Has("atomic", "egress") {
		t.Errorf("the old spelling survived beside the new one: %v", doc["atomic"])
	}
	if !strings.Contains(got, `"edge.egress" is used and "atomic.egress" is ignored`) {
		t.Errorf("a document stating both spellings was not told which is used:\n%s", got)
	}
}

// What a person sees for an old Driftfile, exactly: the deprecated-in-favour-of
// sentence, once.
func TestAMovedAliasPrintsTheDeprecationSentence(t *testing.T) {
	doc := Node{"atomic": map[string]any{"egress": map[string]any{"mode": "open"}}}

	got := walkFor(t, doc, []common.KeyDeprecation{alias("atomic.egress", "edge.egress")})

	want := `"atomic.egress" is deprecated in favour of "edge.egress", and will be removed in a later version; ` +
		"run `drift file migrate` to update this file.\n"
	if got != want {
		t.Errorf("notice:\n got: %q\nwant: %q", got, want)
	}
}

// A destination that is not an object is the schema's to refuse, not the
// walker's to overwrite. The old key stays where it was rather than vanishing.
func TestAMoveOntoANonObjectLeavesTheOldKeyInPlace(t *testing.T) {
	doc := Node{
		"domains": []any{map[string]any{"host": "app.example.com"}},
		"edge":    "not an object",
	}

	walkFor(t, doc, []common.KeyDeprecation{alias("domains", "edge.domains")})

	if len(doc.List("domains")) != 1 {
		t.Errorf("the old key was dropped with nowhere to go: %v", doc)
	}
	if doc["edge"] != "not an object" {
		t.Errorf("the walker overwrote a value it does not own: %v", doc["edge"])
	}
}

// ---------- aliases inside environment overlays ----------

// An environment overlay is a second place a key is written, and it is MERGED
// onto the base. Walking only the root left an overlay's old spelling in place,
// so the merge carried both and readers saw whichever they looked for.
func TestAnAliasIsAppliedInsideEveryEnvironmentOverlay(t *testing.T) {
	doc := Node{
		"atomic": map[string]any{"egress": map[string]any{"mode": "open"}},
		"environments": map[string]any{
			"staging": map[string]any{"atomic": map[string]any{"egress": map[string]any{"mode": "allowlist"}}},
			"prod":    map[string]any{"domains": []any{map[string]any{"host": "app.example.com"}}},
		},
	}

	got := walkFor(t, doc, []common.KeyDeprecation{
		alias("atomic.egress", "edge.egress"),
		alias("domains", "edge.domains"),
	})

	staging := doc.Sub("environments", "staging")
	if staging.Str("edge", "egress", "mode") != "allowlist" || staging.Has("atomic", "egress") {
		t.Errorf("the staging overlay kept the old spelling: %v", staging)
	}
	prod := doc.Sub("environments", "prod")
	if len(prod.Nodes("edge", "domains")) != 1 || prod.Has("domains") {
		t.Errorf("the prod overlay kept the old spelling: %v", prod)
	}
	if doc.Str("edge", "egress", "mode") != "open" {
		t.Errorf("the root was not rewritten as well: %v", doc)
	}
	// One notice per path, wherever in the document the path occurs.
	if n := strings.Count(got, `"atomic.egress" is deprecated`); n != 1 {
		t.Errorf("atomic.egress was announced %d times across the root and an overlay, want 1:\n%s", n, got)
	}
}

// Through the real parse and merge: a base in the new spelling and an overlay in
// the old one resolve to the OVERLAY's value, under the new spelling only.
func TestAnOverlayInTheOldSpellingOverridesTheBaseAfterTheMerge(t *testing.T) {
	withSchema(t, `{"type":"object"}`)

	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	doc := strings.Join([]string{
		"slice: demo",
		"edge:",
		"  egress:",
		"    mode: allowlist",
		"    hosts: [base.example.com]",
		"environments:",
		"  staging:",
		"    atomic:",
		"      egress:",
		"        mode: allowlist",
		"        hosts: [staging.example.com]",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&out)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	common.ResetDeprecationState()

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatalf("select staging: %v", err)
	}

	hosts := m.Slice().Strings("edge", "egress", "hosts")
	if len(hosts) != 1 || hosts[0] != "staging.example.com" {
		t.Errorf("edge.egress.hosts = %v, want the staging overlay's [staging.example.com]", hosts)
	}
	if m.Slice().Has("atomic", "egress") {
		t.Errorf("the merged slice carries both spellings: %v", m.Slice()["atomic"])
	}
}

// ---------- the registered edge aliases ----------

// The three keys that moved under `edge` are ALIASES: an old file keeps taking
// effect, under the new spelling.
func TestTheEdgeKeysAreRegisteredAsAliases(t *testing.T) {
	want := map[string]string{
		"atomic.egress":     "edge.egress",
		"domains":           "edge.domains",
		"atomic.rate_limit": "edge.rate_limit",
	}
	found := map[string]bool{}
	for _, k := range driftfileKeyDeprecations {
		newPath, ok := want[k.Path]
		if !ok {
			continue
		}
		if found[k.Path] {
			t.Errorf("%s is registered more than once", k.Path)
		}
		found[k.Path] = true
		if k.Kind != common.KeyAlias {
			t.Errorf("%s is registered as kind %d, want an alias — it takes effect under %s", k.Path, k.Kind, newPath)
		}
		if k.New != newPath {
			t.Errorf("%s aliases to %q, want %q", k.Path, k.New, newPath)
		}
	}
	for path := range want {
		if !found[path] {
			t.Errorf("%s is not registered", path)
		}
	}
}

// A move to a different parent cannot address a list: a key inside list
// elements has no single place to move to. Every registered alias is checked,
// so a later entry of that shape fails here rather than silently not moving.
func TestEveryCrossParentAliasAddressesNoList(t *testing.T) {
	for _, k := range driftfileKeyDeprecations {
		if k.Kind != common.KeyAlias {
			continue
		}
		oldParent, _, _ := splitKeyPath(k.Path)
		newParent, _, _ := splitKeyPath(k.New)
		if oldParent == newParent {
			continue
		}
		if strings.Contains(k.Path, "[]") || strings.Contains(k.New, "[]") {
			t.Errorf("%s → %s moves a key across parents through a list, which the walker cannot do", k.Path, k.New)
		}
	}
}

// The whole old file, through the real parse, with the registered set: every
// edge key lands under `edge`, and each is announced with the sentence.
func TestAnOldDriftfileLandsUnderEdge(t *testing.T) {
	withSchema(t, `{"type":"object"}`)

	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	doc := strings.Join([]string{
		"slice: demo",
		"atomic:",
		"  rate_limit: 600/min",
		"  egress:",
		"    mode: allowlist",
		"    hosts: [api.stripe.com]",
		"domains:",
		"  - host: app.example.com",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&out)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	common.ResetDeprecationState()

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	s := m.Slice()
	if s.Str("edge", "rate_limit") != "600/min" || s.Has("atomic", "rate_limit") {
		t.Errorf("atomic.rate_limit did not land on edge.rate_limit: %v", s)
	}
	if s.Str("edge", "egress", "mode") != "allowlist" || s.Has("atomic", "egress") {
		t.Errorf("atomic.egress did not land on edge.egress: %v", s)
	}
	if len(s.Nodes("edge", "domains")) != 1 || s.Has("domains") {
		t.Errorf("domains did not land on edge.domains: %v", s)
	}
	for _, line := range []string{
		`"atomic.egress" is deprecated in favour of "edge.egress", and will be removed in a later version; ` + common.MigrateHint + ".",
		`"domains" is deprecated in favour of "edge.domains", and will be removed in a later version; ` + common.MigrateHint + ".",
		`"atomic.rate_limit" is deprecated in favour of "edge.rate_limit", and will be removed in a later version; ` + common.MigrateHint + ".",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("missing notice %q in:\n%s", line, out.String())
		}
	}
}

// A list element that is a bare string has no key to rewrite. Both forms are
// live — `canvas: ["./canvas"]` is sugar the section expander leaves alone — so
// skipping is correct rather than a gap, and it must not panic.
func TestABareStringListEntryIsSkipped(t *testing.T) {
	doc := Node{"canvas": map[string]any{"sites": []any{"./canvas"}}}

	got := walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "canvas.sites[].path")})

	if got != "" {
		t.Errorf("a bare string entry produced a notice:\n%s", got)
	}
	if doc.List("canvas", "sites")[0] != "./canvas" {
		t.Errorf("the bare entry was disturbed: %v", doc["canvas"])
	}
}

// A path whose parent is missing entirely is not an error. Most Driftfiles
// declare a few sections, and walking the rest must be silent.
func TestAMissingSectionIsSilent(t *testing.T) {
	doc := Node{"atomic": map[string]any{}}

	if got := walkFor(t, doc, []common.KeyDeprecation{
		ignored("backbone.blob_max_size"),
		ignored("atomic.functions[].memory"),
		ignored("canvas.canvas_size"),
	}); got != "" {
		t.Errorf("absent sections produced notices:\n%s", got)
	}
}

// The notice names what to do. An ignored shape key has no replacement spelling,
// so the sentence has to carry the owner instead — otherwise it tells someone
// their key is dead and nothing else.
//
// Asserted as the command to run rather than as a name for the owner: what owns
// a slice's shape has been called more than one thing, and a test pinned to the
// name goes green while the sentence sends the reader somewhere that is gone.
func TestAnIgnoredKeysNoticeNamesTheNewOwner(t *testing.T) {
	doc := Node{"log_retention": "72h"}
	got := walkFor(t, doc, []common.KeyDeprecation{ignored("log_retention")})

	if !strings.Contains(got, "drift slice resize") {
		t.Errorf("the notice does not say where the value moved:\n%s", got)
	}
	if strings.Contains(got, `Use ""`) {
		t.Errorf("the notice offers an empty replacement:\n%s", got)
	}
}

// The key-level entries join the command-level ones, so `drift` can list what is
// deprecated without running any of it.
func TestKeyDeprecationsAreListedAlongsideCommands(t *testing.T) {
	doc := Node{"log_retention": "72h"}
	walkFor(t, doc, []common.KeyDeprecation{ignored("log_retention")})

	for _, d := range common.Deprecations() {
		if d.Old == "log_retention" {
			return
		}
	}
	t.Error("a deprecated key is not in the registry, so `drift` cannot list it")
}

// The hook point itself, not the walker in isolation: a real Driftfile going
// through ParseDriftfile hears the notice, and the alias has been resolved by
// the time anything reads the document.
func TestParsingADriftfileAppliesKeyDeprecations(t *testing.T) {
	// A permissive schema, deliberately. What is under test is the walker and
	// where it is hooked, not the format — and without this the test reads
	// whatever schema the machine has cached, which is how this suite once passed
	// on a laptop and failed on CI.
	withSchema(t, `{"type":"object"}`)

	dir := t.TempDir()
	path := filepath.Join(dir, "Driftfile")
	doc := strings.Join([]string{
		"slice: demo",
		"log_retention: 72h",
		"canvas:",
		"  sites:",
		"    - dir: ./site",
		"      route: /app",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&out)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	common.ResetDeprecationState()

	prev := driftfileKeyDeprecations
	driftfileKeyDeprecations = []common.KeyDeprecation{
		ignored("log_retention"),
		alias("canvas.sites[].route", "canvas.sites[].path"),
	}
	t.Cleanup(func() { driftfileKeyDeprecations = prev })

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("a document using retired spellings failed to parse: %v", err)
	}

	if !strings.Contains(out.String(), "log_retention") {
		t.Errorf("no notice reached the user through ParseDriftfile:\n%s", out.String())
	}

	site := m.Doc().Nodes("canvas", "sites")[0]
	if site.Str("path") != "/app" || site.Has("route") {
		t.Errorf("the alias was not resolved before readers saw the document: %v", site)
	}
}
