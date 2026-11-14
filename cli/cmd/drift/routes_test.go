package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// THE CLI SENDS THE API'S CANONICAL ROUTES, AND NO SOURCE NAMES A LEGACY SPELLING.
//
// The api names a resource once and lets the method carry the intent: POST, GET
// and DELETE /ops/slice, GET /ops/slice/snapshot, GET and DELETE /ops/atomic.
// The spellings that repeat the verb in the path still answer and always will, so
// nothing fails when a call keeps using one. The platform's tests hold the
// aliases in place; this holds the CLI to the canonical paths. A path that says
// "list" for a request whose method already says GET routes the next reader
// wrong.
//
// Every non-test .go file in the module is read, comments included, because a
// comment naming `/ops/slice/get` is the same lie told to a person instead of a
// server. The match is the path as written, so a path assembled from pieces at
// run time is not seen; no call builds one of these that way.
type legacyRoute struct {
	legacy    string
	canonical string
}

// legacyRoutes are the seven verb-in-path spellings the api keeps as permanent
// aliases, each with the method and path the CLI sends instead.
var legacyRoutes = []legacyRoute{
	{"/ops/slice/create", "POST /ops/slice"},
	{"/ops/slice/list", "GET /ops/slice"},
	{"/ops/slice/get", "GET /ops/slice?name="},
	{"/ops/slice/delete", "DELETE /ops/slice?name="},
	{"/ops/slice/snapshot/list", "GET /ops/slice/snapshot"},
	{"/ops/atomic/list", "GET /ops/atomic"},
	{"/ops/atomic/delete", "DELETE /ops/atomic?name="},
}

// pathByte reports whether c can continue a path segment, which is what makes a
// match a longer path's prefix rather than the spelling itself.
func pathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// legacyRoutesIn returns every legacy spelling that appears in line as a whole
// path, and not as the start of a longer one.
func legacyRoutesIn(line string) []legacyRoute {
	var found []legacyRoute
	for _, r := range legacyRoutes {
		for from := 0; ; {
			i := strings.Index(line[from:], r.legacy)
			if i < 0 {
				break
			}
			end := from + i + len(r.legacy)
			if end == len(line) || !pathByte(line[end]) {
				found = append(found, r)
				break
			}
			from = end
		}
	}
	return found
}

func TestNoCLISourceNamesALegacyRouteSpelling(t *testing.T) {
	t.Skip("callers still send the legacy spellings; #1651 removes this skip once #1650 and #1651 have moved them all")

	root := repoRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path) // #nosec G304 -- this repo's own source
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			for _, r := range legacyRoutesIn(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+r.legacy+" is now "+r.canonical)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("these sources name a legacy route spelling. The api keeps each as an alias, "+
			"so it answers, and the CLI sends the canonical method and path instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// The guard above passes on a tree with nothing to find, so it has to be shown
// able to find something: each spelling, in a string and in a comment, and not
// the canonical paths that sit beside them.
func TestTheLegacyRouteGuardFindsEachSpelling(t *testing.T) {
	for _, r := range legacyRoutes {
		for _, line := range []string{
			`u := common.APIBaseURL + "` + r.legacy + `"`,
			`u := common.APIBaseURL + "` + r.legacy + `?name=" + name`,
			`// ` + r.legacy + ` answers the same`,
			"`" + r.legacy + "`",
		} {
			got := legacyRoutesIn(line)
			if len(got) != 1 || got[0] != r {
				t.Errorf("%q: found %v, want exactly %q", line, got, r.legacy)
			}
		}
	}

	for _, line := range []string{
		`"/ops/slice"`,
		`"/ops/slice?name=" + name`,
		`"/ops/slice/snapshot"`,
		`"/ops/slice/snapshot/status"`,
		`"/ops/slice/snapshot/restore"`,
		`"/ops/slice/status"`,
		`"/ops/slice/free"`,
		`"/ops/atomic"`,
		`"/ops/atomic/auth"`,
		`"/ops/atomic/alert"`,
		`"/ops/slice/get-started"`,
		`"/ops/slice/listing"`,
		`"/ops/atomic/delete_all"`,
	} {
		if got := legacyRoutesIn(line); len(got) != 0 {
			t.Errorf("%q: found %v, want nothing", line, got)
		}
	}

	// A later occurrence is still found when an earlier one is a longer path.
	if got := legacyRoutesIn(`"/ops/slice/listing" "/ops/slice/list"`); len(got) != 1 || got[0].legacy != "/ops/slice/list" {
		t.Errorf("a longer path hid a real spelling: found %v", got)
	}
}
