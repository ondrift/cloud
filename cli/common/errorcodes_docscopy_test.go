package common

// The registry, errorcodes.json, owns the failure codes. cloud/sites holds a copy of
// it, docs/src/errorcodes.json, and generates its troubleshooting pages from the copy,
// so the copy has to be the registry byte for byte. docsCopyDrift compares the two and
// says which codes differ and how: added, removed, reworded or retired.
//
// This repository's pull-request job checks out this repository alone, so the copy can
// be reached only where a checkout of cloud/sites stands beside it, as it does in the
// workspace, where cloud/public and cloud/sites sit under cloud/. compareDocsCopy looks
// for it there. Where it is not found the test skips and says how the copy is made;
// where it is found, a copy that is not the registry fails the test.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// docsCopyDrift compares the registry with the copy of it that cloud/sites holds and
// returns one line for each way the copy differs, in code order. A code only the
// registry has is added, a code only the copy has is removed, a code whose meaning,
// remedy or command differs is reworded, and a code that only one of the two marks
// retired is retired. Identical bytes name nothing.
//
// Bytes that differ with no code differing are one line that names no code: the copy
// is not the registry, and only its layout or its comment can be what differs. A
// side that is not a registry in JSON is one line about that side, and nothing is
// compared then.
func docsCopyDrift(registry, docs []byte) []string {
	// Equal bytes are decided before anything is parsed, so the common case costs
	// nothing and a copy that is only laid out another way cannot pass for equal.
	if bytes.Equal(registry, docs) {
		return nil
	}

	registered, registryErr := docsCopyEntries(registry)
	copied, copyErr := docsCopyEntries(docs)
	var lines []string
	if registryErr != nil {
		lines = append(lines, "the registry is not a registry in JSON: "+registryErr.Error())
	}
	if copyErr != nil {
		lines = append(lines, "the copy is not a registry in JSON: "+copyErr.Error())
	}
	if lines != nil {
		return lines
	}

	// Every code either side holds, once, in code order.
	codes := make([]string, 0, len(registered)+len(copied))
	for code := range registered {
		codes = append(codes, code)
	}
	for code := range copied {
		if _, held := registered[code]; !held {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)

	for _, code := range codes {
		inRegistry, registryHas := registered[code]
		inCopy, copyHas := copied[code]
		switch {
		case !copyHas:
			lines = append(lines, code+" added: in the registry, not in the copy")
		case !registryHas:
			lines = append(lines, code+" removed: in the copy, not in the registry")
		default:
			if inRegistry.Retired != inCopy.Retired {
				marks := "registry"
				if inCopy.Retired {
					marks = "copy"
				}
				lines = append(lines, code+" retired: only the "+marks+" marks it retired")
			}
			if fields := docsCopyWordedFields(inRegistry, inCopy); len(fields) > 0 {
				lines = append(lines, code+" reworded ("+strings.Join(fields, ", ")+")")
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "the copy is not byte-identical to the registry, though no code differs: the two differ only in layout or comment")
	}
	return lines
}

// docsCopyEntries reads the entries of a registry by code. A code listed twice keeps
// its last entry, as the registry's own loader does.
func docsCopyEntries(data []byte) (map[string]ErrorCode, error) {
	var doc struct {
		Codes []ErrorCode `json:"codes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	byCode := make(map[string]ErrorCode, len(doc.Codes))
	for _, entry := range doc.Codes {
		byCode[entry.Code] = entry
	}
	return byCode, nil
}

// docsCopyWordedFields names, as the registry spells them, the fields in which two
// entries of one code differ, leaving out the code and whether it is retired, which
// are reported on their own. It reads the fields of ErrorCode itself, so a field the
// registry gains is compared without this function being touched.
func docsCopyWordedFields(registry, docs ErrorCode) []string {
	var names []string
	r, d := reflect.ValueOf(registry), reflect.ValueOf(docs)
	for i := 0; i < r.NumField(); i++ {
		name, _, _ := strings.Cut(r.Type().Field(i).Tag.Get("json"), ",")
		if name == "code" || name == "retired" {
			continue
		}
		if !reflect.DeepEqual(r.Field(i).Interface(), d.Field(i).Interface()) {
			names = append(names, name)
		}
	}
	return names
}

// docsCopyOutcome is what comparing the registry with its copy in cloud/sites came
// to. At most one field is set; neither is a pass.
type docsCopyOutcome struct {
	// skip says why nothing was compared.
	skip string
	// fail says what is wrong.
	fail string
}

// docsCopyCacheNote ends every skip. go test keeps the result of a test and checks
// only the files inside the module for a change, so a cloud/sites checked out after a
// skip is not noticed until the test is run again with -count=1.
const docsCopyCacheNote = "go test keeps a result and does not watch cloud/sites, so once it is there, run this test with -count=1."

// compareDocsCopy finds the checkout of cloud/sites beside this repository and holds
// the copy of the registry that it keeps to the registry itself.
//
// cloud is the directory that holds this repository and its siblings, and registry
// is the path of this package's errorcodes.json. The climb to cloud is checked before
// anything is looked for beside the repository, so that a wrong count fails as such
// and does not look like a checkout with nothing beside it. With no cloud/sites
// beside the repository, or one that holds no copy, nothing is compared and the
// outcome is a skip that names the command that makes the copy: that is what a runner
// that checks out this repository alone sees. A file that is there and cannot be
// read, and a copy that is not the registry, are failures.
func compareDocsCopy(cloud, registry string) docsCopyOutcome {
	registryBytes, err := os.ReadFile(registry)
	if err != nil {
		return docsCopyOutcome{fail: "read the registry: " + err.Error()}
	}
	if err := holdsRepository(cloud, registry); err != nil {
		return docsCopyOutcome{fail: "the climb to the directory above this repository is miscounted: " + err.Error()}
	}

	sites := filepath.Join(cloud, "sites")
	if _, err := os.Stat(sites); err != nil {
		return docsCopyOutcome{skip: fmt.Sprintf("cloud/sites is not checked out beside this repository (%v), so the docs copy of the registry is not compared.\n"+
			"With it checked out, make the copy by running this in cloud/sites:\n  %s\n%s", err, docsCopyCommand(registry), docsCopyCacheNote)}
	}
	copyFile := filepath.Join(sites, "docs", "src", "errorcodes.json")
	if _, err := os.Stat(copyFile); err != nil {
		return docsCopyOutcome{skip: fmt.Sprintf("cloud/sites is beside this repository but holds no docs copy of the registry (%v), so there is nothing to compare.\n"+
			"Make the copy by running this in cloud/sites:\n  %s\n%s", err, docsCopyCommand(registry), docsCopyCacheNote)}
	}

	copyBytes, err := os.ReadFile(copyFile)
	if err != nil {
		return docsCopyOutcome{fail: "read the docs copy of the registry: " + err.Error()}
	}
	if lines := docsCopyDrift(registryBytes, copyBytes); len(lines) > 0 {
		return docsCopyOutcome{fail: fmt.Sprintf("the docs copy of the registry, %s, is not the registry:\n%s\n\nRefresh the copy by running this in cloud/sites:\n  %s",
			docsCopyAbs(copyFile), docsCopyShow(lines), docsCopyCommand(registry))}
	}
	return docsCopyOutcome{}
}

// holdsRepository returns nil when dir holds a checkout of this repository, and the
// reason when it does not. A checkout is an entry of dir whose cli/common/errorcodes.json
// is the registry at the given path: the same file, not another like it.
//
// The entry is not asked for by name. The directory is called public in the
// workspace, but a clone of the repository from GitHub is called cloud, and a
// contributor can call theirs anything.
func holdsRepository(dir, registry string) error {
	registryInfo, err := os.Stat(registry)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		candidate, err := os.Stat(filepath.Join(dir, entry.Name(), "cli", "common", "errorcodes.json"))
		if err == nil && os.SameFile(registryInfo, candidate) {
			return nil
		}
	}
	return fmt.Errorf("%s holds no checkout of this repository", dir)
}

// docsCopyCommand is the command that makes the copy in cloud/sites, or brings it up
// to date, from the registry at the given path. It is run in cloud/sites.
func docsCopyCommand(registry string) string {
	return "python3 docs/scripts/sync-errorcodes.py --from " + docsCopyAbs(registry)
}

// docsCopyAbs is path as an absolute path, so that a command or a message that names
// it means the same wherever it is read.
func docsCopyAbs(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// docsCopyCodes is the registry the cases below start from: three live codes, the
// last with no command, which the registry allows.
var docsCopyCodes = []ErrorCode{
	{Code: "DRIFT-1001", Meaning: "Your session has expired.", Remedy: "Run `drift account login` and try again.", Command: "drift account login"},
	{Code: "DRIFT-1002", Meaning: "The platform refused the action.", Remedy: "The account lacks the permission for this operation.", Command: "drift slice list"},
	{Code: "DRIFT-1003", Meaning: "The platform failed while handling the request.", Remedy: "Retry once.", Command: ""},
}

// docsCopyRender lays codes out as errorcodes.json is laid out: a comment, then the
// codes, indented two spaces, with a newline at the end.
func docsCopyRender(t *testing.T, codes []ErrorCode) []byte {
	t.Helper()
	out, err := json.MarshalIndent(struct {
		Comment string      `json:"$comment"`
		Codes   []ErrorCode `json:"codes"`
	}{"A fixture.", codes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// docsCopyEdit returns codes with the entry of one code changed by edit. The slice it
// is given is left as it was.
func docsCopyEdit(codes []ErrorCode, code string, edit func(*ErrorCode)) []ErrorCode {
	out := slices.Clone(codes)
	for i := range out {
		if out[i].Code == code {
			edit(&out[i])
		}
	}
	return out
}

// docsCopyDrop returns codes without the entry of one code.
func docsCopyDrop(codes []ErrorCode, code string) []ErrorCode {
	return slices.DeleteFunc(slices.Clone(codes), func(c ErrorCode) bool { return c.Code == code })
}

// docsCopyReversed returns codes in the opposite order.
func docsCopyReversed(codes []ErrorCode) []ErrorCode {
	out := slices.Clone(codes)
	slices.Reverse(out)
	return out
}

// docsCopyShow lays lines out for a failure message, one to a line.
func docsCopyShow(lines []string) string {
	if len(lines) == 0 {
		return "  (no line)"
	}
	return "  " + strings.Join(lines, "\n  ")
}

// The common case costs nothing and is the one that must name nothing: a copy that
// is the registry as it stands.
func TestDocsCopyDrift_IdenticalBytesNameNothing(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	if got := docsCopyDrift(registry, slices.Clone(registry)); len(got) != 0 {
		t.Errorf("a copy that is the registry byte for byte must name nothing, got: %q", got)
	}
	if got := docsCopyDrift(errorCodesJSON, slices.Clone(errorCodesJSON)); len(got) != 0 {
		t.Errorf("the registry the CLI ships, copied byte for byte, must name nothing, got: %q", got)
	}
}

// Each way a code can differ gives a line that starts with the code, and a code that
// does not differ is named nowhere.
func TestDocsCopyDrift_EachWayACopyDiffersNamesItsCode(t *testing.T) {
	cases := []struct {
		name     string
		registry []ErrorCode
		docs     []ErrorCode
		want     []string
	}{
		{
			name:     "a code only the registry has is added",
			registry: docsCopyCodes,
			docs:     docsCopyDrop(docsCopyCodes, "DRIFT-1003"),
			want:     []string{"DRIFT-1003 added: in the registry, not in the copy"},
		},
		{
			name:     "a code only the copy has is removed",
			registry: docsCopyDrop(docsCopyCodes, "DRIFT-1003"),
			docs:     docsCopyCodes,
			want:     []string{"DRIFT-1003 removed: in the copy, not in the registry"},
		},
		{
			name:     "a reworded meaning",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Meaning = "The platform refused it." }),
			want:     []string{"DRIFT-1002 reworded (meaning)"},
		},
		{
			name:     "a reworded remedy",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Remedy = "Ask the owner of the slice." }),
			want:     []string{"DRIFT-1002 reworded (remedy)"},
		},
		{
			name:     "a reworded command",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Command = "drift account login" }),
			want:     []string{"DRIFT-1002 reworded (command)"},
		},
		{
			name:     "a reworded meaning and remedy are one line",
			registry: docsCopyCodes,
			docs: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) {
				c.Meaning = "The platform refused it."
				c.Remedy = "Ask the owner of the slice."
			}),
			want: []string{"DRIFT-1002 reworded (meaning, remedy)"},
		},
		{
			name:     "a code retired in the registry and live in the copy",
			registry: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Retired = true }),
			docs:     docsCopyCodes,
			want:     []string{"DRIFT-1002 retired: only the registry marks it retired"},
		},
		{
			name:     "a code live in the registry and retired in the copy",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Retired = true }),
			want:     []string{"DRIFT-1002 retired: only the copy marks it retired"},
		},
		{
			name: "a code retired and reworded gets a line for each",
			registry: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) {
				c.Retired = true
				c.Remedy = "Nothing to do."
			}),
			docs: docsCopyCodes,
			want: []string{
				"DRIFT-1002 retired: only the registry marks it retired",
				"DRIFT-1002 reworded (remedy)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := docsCopyDrift(docsCopyRender(t, tc.registry), docsCopyRender(t, tc.docs))
			if !slices.Equal(got, tc.want) {
				t.Errorf("got:\n%s\nwant:\n%s", docsCopyShow(got), docsCopyShow(tc.want))
			}
		})
	}
}

// Bytes that differ with no code differing are still a difference, and the copy is
// not the registry: one line, naming no code, because there is none to name. The
// sync script writes the registry's bytes, so a copy laid out another way was not
// made by it.
func TestDocsCopyDrift_ACopyDifferingOnlyInLayoutIsNotIdenticalAndNamesNoCode(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cases := []struct {
		name string
		docs []byte
	}{
		{"indented with four spaces", bytes.ReplaceAll(registry, []byte("\n  "), []byte("\n    "))},
		{"windows line endings", bytes.ReplaceAll(registry, []byte("\n"), []byte("\r\n"))},
		{"no newline at the end", bytes.TrimRight(registry, "\n")},
		{"one space at the end of a line", bytes.Replace(registry, []byte("\n"), []byte(" \n"), 1)},
		{"the comment worded differently", bytes.Replace(registry, []byte("A fixture."), []byte("Another fixture."), 1)},
		{"the entries in another order", docsCopyRender(t, docsCopyReversed(docsCopyCodes))},
		{"a field written that says what leaving it out says", bytes.Replace(registry, []byte(`"command": ""`), []byte(`"command": "", "retired": false`), 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Equal(registry, tc.docs) {
				t.Fatal("the case does not differ from the registry, so it proves nothing")
			}
			got := docsCopyDrift(registry, tc.docs)
			if len(got) != 1 {
				t.Fatalf("want one line, got %d:\n%s", len(got), docsCopyShow(got))
			}
			if strings.Contains(got[0], "DRIFT-") {
				t.Errorf("no code differs, so none may be named, got: %s", got[0])
			}
			if !strings.Contains(got[0], "not byte-identical") {
				t.Errorf("the line must say the copy is not byte-identical to the registry, got: %s", got[0])
			}
		})
	}
}

// A copy that cannot be read as a registry is one line that says so, and the line
// is about the side that is wrong.
func TestDocsCopyDrift_ACopyThatIsNotJSONIsOneLineSayingSo(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cases := []struct {
		name string
		docs []byte
	}{
		{"text", []byte("this is not a registry")},
		{"nothing", nil},
		{"the registry cut in half", registry[:len(registry)/2]},
		{"a JSON array", []byte("[]")},
		{"two documents", slices.Concat(registry, registry)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := docsCopyDrift(registry, tc.docs)
			if len(got) != 1 {
				t.Fatalf("want one line, got %d:\n%s", len(got), docsCopyShow(got))
			}
			if !strings.HasPrefix(got[0], "the copy is not a registry in JSON") {
				t.Errorf("the line must say the copy is not a registry in JSON, got: %s", got[0])
			}
		})
	}

	t.Run("the registry is the one that is not JSON", func(t *testing.T) {
		got := docsCopyDrift([]byte("{"), registry)
		if len(got) != 1 || !strings.HasPrefix(got[0], "the registry is not a registry in JSON") {
			t.Errorf("want one line about the registry, got: %q", got)
		}
	})

	t.Run("neither is JSON", func(t *testing.T) {
		got := docsCopyDrift([]byte("{"), []byte("}"))
		if len(got) != 2 ||
			!strings.HasPrefix(got[0], "the registry is not a registry in JSON") ||
			!strings.HasPrefix(got[1], "the copy is not a registry in JSON") {
			t.Errorf("want a line for the registry and then one for the copy, got: %q", got)
		}
	})
}

// The lines come in code order, however the two files list their entries, so the
// first line names the lowest code that differs.
func TestDocsCopyDrift_NamesCodesInCodeOrderWhateverOrderTheFilesListThem(t *testing.T) {
	// The registry lists 1003 first. The copy lists 1004, 1003 and 1002 in that order,
	// has 1003 retired and 1002 reworded, lacks 1001, and holds a 1004 the registry
	// does not.
	retired := docsCopyEdit(docsCopyCodes, "DRIFT-1003", func(c *ErrorCode) { c.Retired = true })
	reworded := docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Remedy = "Ask the owner of the slice." })
	registry := []ErrorCode{docsCopyCodes[2], docsCopyCodes[0], docsCopyCodes[1]}
	docs := []ErrorCode{
		{Code: "DRIFT-1004", Meaning: "Only the copy has it.", Remedy: "None."},
		retired[2],
		reworded[1],
	}
	want := []string{
		"DRIFT-1001 added: in the registry, not in the copy",
		"DRIFT-1002 reworded (remedy)",
		"DRIFT-1003 retired: only the copy marks it retired",
		"DRIFT-1004 removed: in the copy, not in the registry",
	}
	got := docsCopyDrift(docsCopyRender(t, registry), docsCopyRender(t, docs))
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", docsCopyShow(got), docsCopyShow(want))
	}
}

// docsCopyWorkspace builds, in a temporary directory that stands in for cloud/, a
// checkout of this repository named repo whose registry holds the given bytes. It
// returns that directory and the path of the registry.
func docsCopyWorkspace(t *testing.T, repo string, registry []byte) (cloud, registryFile string) {
	t.Helper()
	cloud = t.TempDir()
	dir := filepath.Join(cloud, repo, "cli", "common")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	registryFile = filepath.Join(dir, "errorcodes.json")
	if err := os.WriteFile(registryFile, registry, 0o644); err != nil {
		t.Fatal(err)
	}
	return cloud, registryFile
}

// docsCopyPut writes docs where cloud/sites keeps the copy, under dir, and returns the
// path it wrote.
func docsCopyPut(t *testing.T, dir string, docs []byte) string {
	t.Helper()
	file := filepath.Join(dir, "sites", "docs", "src", "errorcodes.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, docs, 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// With no cloud/sites beside the repository there is nothing to compare, which is
// the case on a runner that checks out this repository alone. The skip says what to
// do and names the command that makes the copy, and it is not a failure.
func TestCompareDocsCopy_WithNoSitesBesideTheRepositoryItSkipsAndNamesTheCommand(t *testing.T) {
	cloud, registryFile := docsCopyWorkspace(t, "public", docsCopyRender(t, docsCopyCodes))

	got := compareDocsCopy(cloud, registryFile)
	if got.fail != "" {
		t.Fatalf("a checkout with no cloud/sites beside it must not fail, got: %s", got.fail)
	}
	if got.skip == "" {
		t.Fatal("a checkout with no cloud/sites beside it must skip")
	}
	for _, want := range []string{
		"cloud/sites is not checked out beside this repository",
		"python3 docs/scripts/sync-errorcodes.py --from " + registryFile,
		"-count=1",
	} {
		if !strings.Contains(got.skip, want) {
			t.Errorf("the skip must say %q, got: %s", want, got.skip)
		}
	}
}

// A cloud/sites that holds no docs/src/errorcodes.json has nothing to compare either,
// and the skip names the same command.
func TestCompareDocsCopy_WithSitesButNoCopyItSkipsAndNamesTheCommand(t *testing.T) {
	cloud, registryFile := docsCopyWorkspace(t, "public", docsCopyRender(t, docsCopyCodes))
	if err := os.MkdirAll(filepath.Join(cloud, "sites", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := compareDocsCopy(cloud, registryFile)
	if got.fail != "" {
		t.Fatalf("a cloud/sites that holds no copy yet must not fail, got: %s", got.fail)
	}
	if got.skip == "" {
		t.Fatal("a cloud/sites that holds no copy yet must skip")
	}
	for _, want := range []string{
		"holds no docs copy of the registry",
		"python3 docs/scripts/sync-errorcodes.py --from " + registryFile,
	} {
		if !strings.Contains(got.skip, want) {
			t.Errorf("the skip must say %q, got: %s", want, got.skip)
		}
	}
}

// The pass: a copy that is the registry byte for byte neither skips nor fails.
func TestCompareDocsCopy_AnEqualCopyIsNeitherASkipNorAFailure(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cloud, registryFile := docsCopyWorkspace(t, "public", registry)
	docsCopyPut(t, cloud, registry)

	if got := compareDocsCopy(cloud, registryFile); got != (docsCopyOutcome{}) {
		t.Errorf("an equal copy must pass, got: %+v", got)
	}
}

// A copy that differs fails, and the failure is what the person has to act on: where
// the copy is, every code that differs and how, and the command that refreshes it.
func TestCompareDocsCopy_ACopyThatDiffersFailsNamingEachCodeAndTheCommand(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cloud, registryFile := docsCopyWorkspace(t, "public", registry)

	docs := docsCopyDrop(docsCopyCodes, "DRIFT-1002")
	docs = docsCopyEdit(docs, "DRIFT-1001", func(c *ErrorCode) { c.Remedy = "Log in again." })
	docs = docsCopyEdit(docs, "DRIFT-1003", func(c *ErrorCode) { c.Retired = true })
	copyFile := docsCopyPut(t, cloud, docsCopyRender(t, docs))

	got := compareDocsCopy(cloud, registryFile)
	if got.skip != "" {
		t.Fatalf("a copy that differs must not skip, got: %s", got.skip)
	}
	for _, want := range []string{
		copyFile,
		"DRIFT-1001 reworded (remedy)",
		"DRIFT-1002 added: in the registry, not in the copy",
		"DRIFT-1003 retired: only the copy marks it retired",
		"python3 docs/scripts/sync-errorcodes.py --from " + registryFile,
	} {
		if !strings.Contains(got.fail, want) {
			t.Errorf("the failure must say %q, got:\n%s", want, got.fail)
		}
	}
}

// A copy laid out differently from the registry is a failure too, though no code is
// named: the sync script writes the registry's bytes, and a copy that is not those
// bytes was not made by it.
func TestCompareDocsCopy_ACopyLaidOutDifferentlyFailsAndSaysItIsNotByteIdentical(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cloud, registryFile := docsCopyWorkspace(t, "public", registry)
	docsCopyPut(t, cloud, bytes.ReplaceAll(registry, []byte("\n"), []byte("\r\n")))

	got := compareDocsCopy(cloud, registryFile)
	if got.skip != "" {
		t.Fatalf("a copy that is laid out differently must not skip, got: %s", got.skip)
	}
	for _, want := range []string{
		"not byte-identical",
		"python3 docs/scripts/sync-errorcodes.py --from " + registryFile,
	} {
		if !strings.Contains(got.fail, want) {
			t.Errorf("the failure must say %q, got:\n%s", want, got.fail)
		}
	}
}

// A climb that is counted wrong finds no cloud/sites anywhere, and a test that
// skipped for that would skip for ever. So the climb is checked before anything is
// looked for, and a wrong one fails. Every case has a copy that a search from the
// wrong directory would find, so it is the check of the climb that is under test.
func TestCompareDocsCopy_AMiscountedClimbFailsWhereverTheCopyIs(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cloud, registryFile := docsCopyWorkspace(t, "public", registry)
	docsCopyPut(t, filepath.Join(cloud, "public"), registry)
	docsCopyPut(t, filepath.Dir(cloud), registry)
	docsCopyPut(t, filepath.Join(cloud, "public", "cli"), registry)

	cases := []struct {
		name string
		from string
	}{
		{"one directory too deep: the repository's root", filepath.Join(cloud, "public")},
		{"two directories too deep: cli", filepath.Join(cloud, "public", "cli")},
		{"one directory too far: above the workspace", filepath.Dir(cloud)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compareDocsCopy(tc.from, registryFile)
			if got.skip != "" {
				t.Errorf("a miscounted climb must fail and never skip, got the skip: %s", got.skip)
			}
			if !strings.Contains(got.fail, "miscounted") || !strings.Contains(got.fail, tc.from) {
				t.Errorf("the failure must say the climb is miscounted and name where it reached (%s), got: %q", tc.from, got.fail)
			}
		})
	}
}

// The check of the climb asks what the directory holds, not what the repository is
// called: a clone of the repository from GitHub is named cloud, and a contributor
// can name theirs anything.
func TestCompareDocsCopy_TheRepositoryMayBeCheckedOutUnderAnyName(t *testing.T) {
	for _, name := range []string{"public", "cloud", "drift-cli"} {
		t.Run(name, func(t *testing.T) {
			registry := docsCopyRender(t, docsCopyCodes)
			cloud, registryFile := docsCopyWorkspace(t, name, registry)

			if got := compareDocsCopy(cloud, registryFile); got.skip == "" || got.fail != "" {
				t.Errorf("with no cloud/sites beside it, want a skip and no failure, got: %+v", got)
			}
			docsCopyPut(t, cloud, registry)
			if got := compareDocsCopy(cloud, registryFile); got != (docsCopyOutcome{}) {
				t.Errorf("with an equal copy beside it, want a pass, got: %+v", got)
			}
			docsCopyPut(t, cloud, docsCopyRender(t, docsCopyDrop(docsCopyCodes, "DRIFT-1003")))
			if got := compareDocsCopy(cloud, registryFile); got.fail == "" || got.skip != "" {
				t.Errorf("with a copy that differs beside it, want a failure, got: %+v", got)
			}
		})
	}
}

// Present and unreadable is a failure, not a skip: the skip is for what is absent.
func TestCompareDocsCopy_AFileThatCannotBeReadFails(t *testing.T) {
	t.Run("the copy", func(t *testing.T) {
		registry := docsCopyRender(t, docsCopyCodes)
		cloud, registryFile := docsCopyWorkspace(t, "public", registry)
		// A directory where the file should be: it exists, and it cannot be read as one.
		if err := os.MkdirAll(filepath.Join(cloud, "sites", "docs", "src", "errorcodes.json"), 0o755); err != nil {
			t.Fatal(err)
		}

		got := compareDocsCopy(cloud, registryFile)
		if got.skip != "" || !strings.Contains(got.fail, "read the docs copy of the registry") {
			t.Errorf("a copy that exists and cannot be read must fail, got: %+v", got)
		}
	})

	t.Run("the registry", func(t *testing.T) {
		cloud, _ := docsCopyWorkspace(t, "public", docsCopyRender(t, docsCopyCodes))

		got := compareDocsCopy(cloud, filepath.Join(cloud, "no-such-registry.json"))
		if got.skip != "" || !strings.Contains(got.fail, "read the registry") {
			t.Errorf("a registry that cannot be read must fail, got: %+v", got)
		}
	})
}

// The copy that cloud/sites holds must be the registry, in every checkout that has
// both. This is that check on the real directories: it passes when the copy is the
// registry, fails naming what differs when it is not, and skips where this
// repository stands alone.
func TestErrorCodes_TheDocsCopyIsCurrent(t *testing.T) {
	// The test runs in cli/common. Each .. leaves one directory: the first leaves
	// common, the second leaves cli, the third leaves public, the repository's root,
	// and lands in cloud/, which holds public and its sibling sites.
	got := compareDocsCopy("../../..", "errorcodes.json")
	if got.fail != "" {
		t.Fatal(got.fail)
	}
	if got.skip != "" {
		t.Skip(got.skip)
	}
}
