// unnamed.go — what the slice holds that this Driftfile does not name.
//
// The mirror of references.go. That file refuses a manifest naming a resource
// the slice does not have; this one reports a resource the slice has and the
// manifest does not name. A renamed `sql` block or a dropped `domain` block leaves
// the live resource in place — nothing on the apply path removes one — so
// without this it is invisible from the terminal that stopped naming it.
//
// IT REPORTS. It does not refuse and it does not delete, for the same reason
// ReportOrphanedFunctions does not: nothing records which project owns a
// resource, so two Driftfiles can legitimately share one slice, and a reconcile
// that cannot tell "not mine" from "deleted" must do neither.
package project

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// unnamedResources is what is live on the slice and unnamed by the manifest,
// one list per class. Each reconciler fills its own field, because the rule
// that joins a declaration to a live name differs per class: a host and a
// database match lowercased, a site by its slug. Holding the join beside the
// declaration is what keeps the spellings from being restated here, where only
// one of them would be right.
type unnamedResources struct {
	Databases   []string
	Domains     []string
	CanvasSites []string
}

func (u unnamedResources) empty() bool {
	return len(u.Databases) == 0 && len(u.Domains) == 0 && len(u.CanvasSites) == 0
}

// unnamedClass is one class of what the slice holds, with the command that
// removes one of it.
type unnamedClass struct {
	label string
	names []string
	// remove is the command that removes one name, or nil for a class no `drift`
	// verb removes one of. Canvas is the only one: the slice takes a keep list
	// rather than a name, and no command sends it. Saying so beats naming a
	// command that does not exist.
	remove func(string) string
	// inPlanLines is whether the plan's own lines already say each of the class
	// (a site the file does not name is a "-" line), so its section leaves it out.
	inPlanLines bool
}

// classes is the three classes in the order they are reported.
func (u unnamedResources) classes() []unnamedClass {
	return []unnamedClass{
		{"sql database", u.Databases, func(n string) string { return "drift backbone sql drop " + n }, false},
		{"domain", u.Domains, func(n string) string { return "drift slice domain remove " + n }, false},
		{"canvas site", u.CanvasSites, nil, true},
	}
}

// printHeld prints the plan's section for the databases and domains the slice
// holds and the file does not name, each with the command that removes it. It
// prints nothing when there are none.
func (u unnamedResources) printHeld(out io.Writer) {
	type row struct{ what, remove string }
	var rows []row
	width := 0
	for _, c := range u.classes() {
		if c.inPlanLines {
			continue
		}
		for _, name := range c.names {
			r := row{c.label + " " + name, c.remove(name)}
			rows = append(rows, r)
			width = max(width, len(r.what))
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(out, "  Held, not in the file: apply removes none of these, because a slice can serve more than one project\n")
	for _, r := range rows {
		fmt.Fprintf(out, "    %-*s  remove it with: %s\n", width, r.what, r.remove)
	}
	fmt.Fprintln(out)
}

// unnamed is what the slice holds that the file does not name, joined from the
// reads the plan made: every domain, every SQL database and every site the slice
// has, whether or not the file declares one. It is the one place the join is made.
// A slice that does not exist yet holds nothing.
//
// A list the plan could not read is unread in the verdict and is no list: the
// plan does not say that nothing is held on the strength of a read that failed.
// A list a comparison of the plan's own lines already asked for is recorded
// there, once.
//
// A database the plan itself removes is not held: the slice's shape has it, the
// file's does not, and its own "-" line says so.
func (p *planState) unnamed(d *planDiff) unnamedResources {
	var u unnamedResources
	if p.live == nil {
		return u
	}
	m := p.c.Manifest

	if p.sqlErr != nil {
		d.noteUnread("sql databases", "sql databases", p.sqlErr)
	} else {
		accounted := map[string]bool{}
		for _, name := range entryNames(m, "name", "backbone", "sql") {
			accounted[strings.ToLower(name)] = true
		}
		for name := range p.live.Config.Backbone.SQL.Databases {
			if _, inFile := p.c.Shape.Backbone.SQL.Databases[name]; !inFile {
				accounted[strings.ToLower(strings.TrimSpace(name))] = true
			}
		}
		names := make([]string, 0, len(p.sql))
		for _, db := range p.sql {
			names = append(names, strings.ToLower(strings.TrimSpace(db.Name)))
		}
		u.Databases = unnamedIn(names, accounted)
	}

	if p.domainsErr != nil {
		if !d.hasUnread("domain") {
			d.noteUnread("domain", "domain", p.domainsErr)
		}
	} else {
		declared := map[string]bool{}
		for _, w := range declaredDomains(m) {
			declared[w.Host] = true
		}
		hosts := make([]string, 0, len(p.domains))
		for _, l := range p.domains {
			hosts = append(hosts, strings.ToLower(strings.TrimSpace(l.Host)))
		}
		u.Domains = unnamedIn(hosts, declared)
	}

	// A file whose sites cannot be resolved has no list to join with, and says so
	// in the note diffSites gives it; apply refuses such a file before it reads
	// the slice.
	declaredSites, serr := canvasSites(m)
	switch {
	case p.sitesErr != nil:
		if !d.hasUnread("sites") {
			d.noteUnread("sites", "sites", p.sitesErr)
		}
	case serr == nil:
		declared := make(map[string]bool, len(declaredSites))
		for _, s := range declaredSites {
			declared[s.Slug] = true
		}
		slugs := make([]string, 0, len(p.sites))
		for _, s := range p.sites {
			slugs = append(slugs, s.Slug)
		}
		u.CanvasSites = unnamedIn(slugs, declared)
	}
	return u
}

// liveCanvasSite is one entry of the slice's canvas registry.
type liveCanvasSite struct {
	Slug  string `json:"slug"`
	Route string `json:"route"`
}

func fetchLiveCanvasSites() ([]liveCanvasSite, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/canvas", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list canvas sites")
	if err != nil {
		return nil, err
	}
	// The registry arrives wrapped: `deployed` answers "is there anything at
	// all", `sites` is the list. Decoding straight into a slice yields nothing
	// and no error, which is how the list read as absent for a long time.
	var out struct {
		Sites []liveCanvasSite `json:"sites"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Sites, nil
}

// unnamedIn reports the live names absent from declared, sorted and deduplicated
// so the section reads the same on every apply. The caller normalises both sides
// to the spelling its class stores.
func unnamedIn(live []string, declared map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range live {
		if name == "" || declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// reportUnnamedResources prints one section naming every live resource the
// manifest does not, with the command that removes each.
//
// A class whose live set could not be read contributes nothing here. That is
// not a silent all-clear: each reconciler states its own fetch failure on the
// way past, so the run says which class it could not check.
func reportUnnamedResources(u unnamedResources, out io.Writer) {
	if u.empty() {
		return
	}

	fmt.Fprintf(out, "\n  %s Live on this slice, and not named in this Driftfile:\n", common.Hint("·"))
	for _, c := range u.classes() {
		for _, name := range c.names {
			fmt.Fprintf(out, "      %s %s\n", c.label, name)
			if c.remove == nil {
				fmt.Fprintf(out, "        still served — no drift command removes a site\n")
				continue
			}
			fmt.Fprintf(out, "        remove it with: %s\n", c.remove(name))
		}
	}
	fmt.Fprintf(out, "    Nothing was removed: a slice can serve more than one project, and a\n"+
		"    deploy cannot tell a resource that is not yours from one you deleted.\n")
}
