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
	"fmt"
	"io"
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
	Databases []string
	Alerts    []string
	Domains   []string
}

func (u unnamedResources) empty() bool {
	return len(u.Databases) == 0 && len(u.Alerts) == 0 && len(u.Domains) == 0
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

	classes := []struct {
		label  string
		names  []string
		remove func(string) string
	}{
		{"sql database", u.Databases, func(n string) string { return "drift backbone sql drop " + n }},
		{"alert", u.Alerts, func(n string) string { return "drift atomic alert remove " + n }},
		{"domain", u.Domains, func(n string) string { return "drift slice domain remove " + n }},
	}

	fmt.Fprintf(out, "\n  %s Live on this slice, and not named in this Driftfile:\n", common.Hint("·"))
	for _, c := range classes {
		for _, name := range c.names {
			fmt.Fprintf(out, "      %s %s\n", c.label, name)
			fmt.Fprintf(out, "        remove it with: %s\n", c.remove(name))
		}
	}
	fmt.Fprintf(out, "    Nothing was removed: a slice can serve more than one project, and a\n"+
		"    deploy cannot tell a resource that is not yours from one you deleted.\n")
}
