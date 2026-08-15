// references.go — does the slice actually hold what the manifest names?
//
// The Driftfile references resources by NAME because the code does:
// `backbone.nosql: [{name: sessions}]` exists because something calls
// `NoSQL.Write("sessions", …)`. A write to a collection or bucket the slice
// does not declare is refused by the slice with a 400, which lands at RUNTIME,
// on a live slice, after the upload. This turns that into a refusal the
// developer sees on their own terminal, before anything is sent.
//
// It reports every miss at once. A check that stops at the first one turns a
// single fix into as many deploy attempts as there are missing names.
package project

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// CheckSliceReferences refuses a manifest naming a function, collection, bucket
// or database that live does not declare.
//
// Comparison is EXACT, never case-insensitive. The runtime finds a function's
// pool by exact string lookup against a lowercase-method key, so a declared name
// that matches only in case is a pool no invocation ever reaches — calling it
// "declared" would hide the exact failure this check exists to surface.
//
// An EMPTY declared set for a class skips that class. That is the same escape
// hatch the slice reads as "no declaration model in play", and the same reason a
// slice predating per-function memory stays deployable: it declares none, and
// refusing those would strand every one of them on its next deploy.
//
// Three classes are deliberately NOT checked, because the slice's config carries
// no names for them — canvas sites (a total size), queues (a per-queue depth)
// and secrets (a count). The refusal names the classes it checked rather than
// claiming to be complete.
func CheckSliceReferences(m *Manifest, live *LiveSlice) error {
	if live == nil {
		return fmt.Errorf("slice %q does not exist — create it first at %s, then deploy into it",
			m.Name(), common.ConfiguratorBaseURL)
	}

	declaredFunctions := make(map[string]bool, len(live.Config.Atomic.Functions))
	for _, f := range live.Config.Atomic.Functions {
		declaredFunctions[f.Name] = true
	}

	var missing []referenceMiss
	missing = append(missing, missesIn("function", namedFunctions(m), declaredFunctions)...)
	missing = append(missing, missesIn("nosql collection",
		entryNames(m, "backbone", "nosql"), keySet(live.Config.Backbone.NoSQL.Collections))...)
	missing = append(missing, missesIn("blob bucket",
		entryNames(m, "backbone", "blobs"), keySet(live.Config.Backbone.Blobs.Buckets))...)
	missing = append(missing, missesIn("sql database",
		entryNames(m, "backbone", "sql"), keySet(live.Config.Backbone.SQL.Databases))...)

	if len(missing) == 0 {
		return nil
	}
	return referenceError(m.Name(), missing)
}

// referenceMiss is one name the manifest uses and the slice does not have.
// NearMiss carries a declared name differing only in case, which is the failure
// most worth naming outright: it looks declared and reaches nothing.
type referenceMiss struct {
	Class    string
	Name     string
	NearMiss string
}

// missesIn reports the referenced names absent from declared, preserving the
// order the document lists them in. An empty declared set means the class is not
// under a declaration model and nothing is missing.
func missesIn(class string, referenced []string, declared map[string]bool) []referenceMiss {
	if len(declared) == 0 {
		return nil
	}
	lowered := make(map[string]string, len(declared))
	for name := range declared {
		lowered[strings.ToLower(name)] = name
	}

	var out []referenceMiss
	seen := map[string]bool{}
	for _, name := range referenced {
		if name == "" || declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, referenceMiss{Class: class, Name: name, NearMiss: lowered[strings.ToLower(name)]})
	}
	return out
}

// namedFunctions reads function identities through FunctionSpecs rather than off
// the raw node, so the later route+method split moves one join site and leaves
// this file alone.
func namedFunctions(m *Manifest) []string {
	specs := FunctionSpecs(m)
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func entryNames(m *Manifest, path ...string) []string {
	entries := m.Slice().Entries("name", path...)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSpace(e.Str("name")))
	}
	return out
}

func keySet(m map[string]int) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// referenceError renders every miss in one block, grouped by class in the order
// they were checked, so one edit in the configurator fixes the whole list.
func referenceError(sliceName string, missing []referenceMiss) error {
	byClass := map[string][]referenceMiss{}
	var order []string
	for _, miss := range missing {
		if _, seen := byClass[miss.Class]; !seen {
			order = append(order, miss.Class)
		}
		byClass[miss.Class] = append(byClass[miss.Class], miss)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "your Driftfile names %s that slice %q does not have:",
		plural(len(missing), "a resource", "resources"), sliceName)
	for _, class := range order {
		items := byClass[class]
		sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		for _, miss := range items {
			fmt.Fprintf(&b, "\n  - %s %q", miss.Class, miss.Name)
			if miss.NearMiss != "" {
				fmt.Fprintf(&b, " — the slice declares %q, which differs only in case; "+
					"the runtime matches this name exactly, so %q reaches nothing",
					miss.NearMiss, miss.NearMiss)
			}
		}
	}
	fmt.Fprintf(&b, "\n\nAdd them to the slice at %s, then deploy again. "+
		"Checked: functions, nosql collections, blob buckets and sql databases.", common.ConfiguratorBaseURL)
	return fmt.Errorf("%s", b.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
