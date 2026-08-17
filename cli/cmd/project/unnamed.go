// unnamed.go — what the slice holds that this Driftfile does not name.
//
// The mirror of references.go. That file refuses a manifest naming a resource
// the slice does not have; this one reports a resource the slice has and the
// manifest does not name. A renamed `sql:` entry, a dropped `domains:` host or
// an alert taken off a function leaves the live resource in place — nothing on
// the apply path removes one — so without this it is invisible from the
// terminal that stopped naming it.
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

	"github.com/ondrift/cloud/cli/common"
)

// unnamedResources is what is live on the slice and unnamed by the manifest,
// one list per class. Each reconciler fills its own field, because the rule
// that joins a declaration to a live name differs per class: a host and a
// database match lowercased, an alert on the derived `<function>-<index>`.
// Holding the join beside the declaration is what keeps the two spellings from
// being restated here, where only one of them would be right.
type unnamedResources struct {
	Databases   []string
	Alerts      []string
	Domains     []string
	CanvasSites []string
}

func (u unnamedResources) empty() bool {
	return len(u.Databases) == 0 && len(u.Alerts) == 0 &&
		len(u.Domains) == 0 && len(u.CanvasSites) == 0
}

// liveCanvasSite is one entry of the slice's canvas registry.
type liveCanvasSite struct {
	Slug  string `json:"slug"`
	Route string `json:"route"`
}

// unnamedCanvasSites reports the slugs the slice serves that this manifest does
// not declare.
//
// Canvas is checked here rather than inside applyCanvas for two reasons: the
// triad runs its three phases behind one uniform signature, and applyCanvas
// returns early when the manifest declares no sites — which is precisely the
// case where every live site is unnamed. The declared side still resolves
// through canvasSites, the same resolver the deploy uses, so the slug rule is
// read from one place rather than restated.
func unnamedCanvasSites(m *Manifest) ([]string, error) {
	declared, err := canvasSites(m)
	if err != nil {
		return nil, err
	}
	declaredSlugs := make(map[string]bool, len(declared))
	for _, s := range declared {
		declaredSlugs[s.Slug] = true
	}

	live, err := fetchLiveCanvasSites()
	if err != nil {
		return nil, err
	}
	liveSlugs := make([]string, 0, len(live))
	for _, s := range live {
		liveSlugs = append(liveSlugs, s.Slug)
	}
	return unnamedIn(liveSlugs, declaredSlugs), nil
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

	// A nil `remove` is a class no `drift` verb removes one of. Canvas is the
	// only one: the slice takes a keep list rather than a name, and no command
	// sends it. Saying so beats naming a command that does not exist.
	classes := []struct {
		label  string
		names  []string
		remove func(string) string
	}{
		{"sql database", u.Databases, func(n string) string { return "drift backbone sql drop " + n }},
		{"alert", u.Alerts, func(n string) string { return "drift atomic alert remove " + n }},
		{"domain", u.Domains, func(n string) string { return "drift slice domain remove " + n }},
		{"canvas site", u.CanvasSites, nil},
	}

	fmt.Fprintf(out, "\n  %s Live on this slice, and not named in this Driftfile:\n", common.Hint("·"))
	for _, c := range classes {
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
