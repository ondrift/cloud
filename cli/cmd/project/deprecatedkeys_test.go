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
		Because:     "The configurator owns a slice's shape now.",
	})
}

func alias(oldPath, newLeaf string) common.KeyDeprecation {
	return common.KeyDeprecationFor(oldPath, common.KeyAlias, common.Deprecation{New: newLeaf})
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

	walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "path")})

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

	walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "path")})

	site := doc.Nodes("canvas", "sites")[0]
	if site.Str("path") != "/new" {
		t.Errorf("the declared new spelling was overwritten by the old one: %v", site)
	}
}

// A list element that is a bare string has no key to rewrite. Both forms are
// live — `canvas: ["./canvas"]` is sugar the section expander leaves alone — so
// skipping is correct rather than a gap, and it must not panic.
func TestABareStringListEntryIsSkipped(t *testing.T) {
	doc := Node{"canvas": map[string]any{"sites": []any{"./canvas"}}}

	got := walkFor(t, doc, []common.KeyDeprecation{alias("canvas.sites[].route", "path")})

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
func TestAnIgnoredKeysNoticeNamesTheNewOwner(t *testing.T) {
	doc := Node{"log_retention": "72h"}
	got := walkFor(t, doc, []common.KeyDeprecation{ignored("log_retention")})

	if !strings.Contains(got, "configurator") {
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
		alias("canvas.sites[].route", "path"),
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
