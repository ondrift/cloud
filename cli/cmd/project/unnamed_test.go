package project

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// What the slice holds and the file does not name is one list, the plan's, and
// `drift apply` closes by printing it. These tests pin how each class is joined
// to what the file declares, and the text of the report.

// sqlListBody is a live inventory the SQL admin list can answer with.
const sqlListBody = `[{"name":"orders","size_bytes":1},{"name":"ledger","size_bytes":2},{"name":"audit","size_bytes":3}]`

// liveEverything answers each class's list endpoint with a set larger than any
// test manifest names, so a reconcile that reports nothing has failed to join
// rather than found nothing.
func liveEverything(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ops/backbone/sql/admin/list":
		_, _ = w.Write([]byte(sqlListBody))
	case "/ops/slice/domain":
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"host":"shop.example.com","status":"live"},{"host":"admin.example.com","status":"live"}]`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}
}

// unnamedFor is the plan's list for the fxevm file, edited by edit, on the slice
// that is the file and holds these domains, databases and sites (each a JSON
// body of the platform's list).
func unnamedFor(t *testing.T, edit func(string) string, domains, databases, sites string) unnamedResources {
	t.Helper()
	live, deployed := matchingSlice(t)
	planStubWith(t, live, deployed, map[string]string{
		"/ops/slice/domain":            domains,
		"/ops/backbone/sql/admin/list": databases,
		"/ops/canvas":                  sites,
	})
	return diffIn(t, edit).unnamed
}

// Each class is joined to the file the way the slice stores a name: a host and a
// database lowercased, a site by its slug. A name the file declares in another
// case is the name the slice holds, and calling it unnamed would be a lie. Each
// list is sorted, so the section reads the same on every plan.
func TestUnnamed_EachClassIsJoinedTheWayTheSliceStoresIt(t *testing.T) {
	u := unnamedFor(t, withDomains(t, `  domain "Live.Example.com" {}`),
		`[{"host":"live.example.com","status":"live"},{"host":"old.example.com","status":"live"},{"host":"admin.example.com","status":"live"}]`,
		`[{"name":"chain","size_bytes":1},{"name":"Ledger","size_bytes":2},{"name":"audit","size_bytes":3}]`,
		`{"deployed":true,"sites":[{"slug":"default","route":"/"},{"slug":"docs","route":"/docs"},{"slug":"blog","route":"/blog"}]}`)

	for _, c := range []struct {
		class     string
		got, want []string
	}{
		{"sql database", u.Databases, []string{"audit", "ledger"}},
		{"domain", u.Domains, []string{"admin.example.com", "old.example.com"}},
		{"canvas site", u.CanvasSites, []string{"blog", "docs"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("the %ss the slice holds and the file does not name are %q, want %q", c.class, c.got, c.want)
		}
	}
}

// The control: a file that names everything the slice holds leaves nothing
// unnamed. Without it every test above passes for a join that returns the whole
// live set.
func TestUnnamed_NothingIsUnnamedWhenTheFileNamesEverythingTheSliceHolds(t *testing.T) {
	u := unnamedFor(t, withDomains(t, `  domain "live.example.com" {}`),
		`[{"host":"live.example.com","status":"live"}]`,
		`[{"name":"chain","size_bytes":1}]`,
		`{"deployed":true,"sites":[{"slug":"default","route":"/"}]}`)

	if !u.empty() {
		t.Errorf("the file names everything the slice holds and the plan says %+v is unnamed", u)
	}
}

// The registry arrives wrapped in {"deployed":..., "sites":[...]}. Decoding it as
// a bare array yields an empty list and no error, which reads as "this slice
// serves nothing". A file that declares no site leaves every live site unnamed.
func TestUnnamed_AFileThatDeclaresNoSiteLeavesEverySiteUnnamed(t *testing.T) {
	u := unnamedFor(t, func(s string) string {
		return mustReplace(t, s, "canvas {\n  site \"/\" {\n    dir = \"canvas\"\n  }\n}\n", "")
	}, noList, `[{"name":"chain","size_bytes":1}]`,
		`{"deployed":true,"sites":[{"slug":"orphan","route":"/old"}]}`)

	if !slices.Equal(u.CanvasSites, []string{"orphan"}) {
		t.Errorf("a file that declares no site leaves every live site unnamed, got %q", u.CanvasSites)
	}
}

// The report names each class, the resource, and the command that removes it,
// the three things that turn "something is live" into an action.
func TestReportUnnamedResources_NamesEachClassAndHowToRemoveIt(t *testing.T) {
	var out bytes.Buffer
	reportUnnamedResources(unnamedResources{
		Databases: []string{"ledger"},
		Domains:   []string{"admin.example.com"},
	}, &out)

	got := out.String()
	for _, want := range []string{
		"ledger", "drift backbone sql drop ledger",
		"admin.example.com", "drift slice domain remove admin.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report must carry %q, got %q", want, got)
		}
	}
	if !strings.Contains(got, "Nothing was removed") {
		t.Errorf("the report must say that nothing was deleted, and why, got %q", got)
	}
}

// The control: nothing unnamed prints nothing at all, so an ordinary apply
// gains no noise.
func TestReportUnnamedResources_NothingUnnamedPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	reportUnnamedResources(unnamedResources{}, &out)

	if out.Len() != 0 {
		t.Errorf("nothing is unnamed, so nothing must be printed, got %q", out.String())
	}
}

// The canvas class names no removal command, because no drift verb removes a
// site: the slice takes a keep list and nothing sends it. Printing an invented
// command would be worse than printing none.
func TestReportUnnamedResources_SaysNoCommandRemovesACanvasSite(t *testing.T) {
	var out bytes.Buffer
	reportUnnamedResources(unnamedResources{CanvasSites: []string{"blog"}}, &out)

	got := out.String()
	if !strings.Contains(got, "canvas site blog") {
		t.Errorf("the unnamed site must be reported, got %q", got)
	}
	if !strings.Contains(got, "no drift command removes a site") {
		t.Errorf("the report must say no verb removes one rather than invent one, got %q", got)
	}
	if strings.Contains(got, "remove it with") {
		t.Errorf("no removal command exists for a site, so none must be offered, got %q", got)
	}
}
