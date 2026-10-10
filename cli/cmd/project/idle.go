// idle.go: what the slice declares and this Driftfile does not fill.
//
// The other half of ReportOrphanedFunctions. That one reports a function the
// slice serves and the manifest no longer names; this one reports a function
// slot, a collection, a bucket, a database or a queue the slice DECLARES and the
// manifest does not name. A slot is billed for its booking from the moment it
// is declared, deployed or not, so a slice shaped for six functions running
// five pays for one that does nothing, and the apply that deploys the five is
// the one place both numbers are in hand.
//
// IT REPORTS, and does nothing else. It never prompts and never resizes:
// applying a file does not change what a slice holds in either direction, and a
// question here that could be answered into a smaller slice is the same mistake
// as one answered into a bigger bill. The Driftfile anchors what runs, the
// slice's shape is what was bought, and the two are allowed to differ. What is
// not allowed is for the difference to be invisible. `drift slice resize` acts.
//
// A FREE SLICE IS NOT REPORTED. Its declared sets are the free preset's
// placeholders (five slots, two collections, ten buckets, a database and a
// queue), granted at no charge and bounded by count and size rather than by
// name. None of them is paid for and releasing one saves nothing, so listing up
// to eighteen of them after every free apply would be noise about a grant.
package project

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// idleClass is one declared set: how many the slice declares, how many of those
// the Driftfile names, and one sentence per declared name it does not.
type idleClass struct {
	one, many string
	declared  int
	named     int
	lines     []string
}

// ReportIdleDeclarations prints, per declared set, what the slice declares and
// this manifest does not name, then the one command that changes a slice's
// shape. It prints nothing when every declared name is named, and nothing at all
// for a free slice.
//
// `deployed` is what the slice serves right now, as DeployedFunctions reads it,
// and nil when that could not be read. A slot serving a function this Driftfile
// does not name is not idle: another project on the same slice may own it, and
// ReportOrphanedFunctions already names it. Without the list a slot cannot be
// called idle, only unnamed, and the sentence says exactly that.
//
// Names are compared exactly, in the spelling the reference check compares
// them in.
func ReportIdleDeclarations(m *Manifest, live *LiveSlice, deployed []atomic_cmd.DeployedFunction, out io.Writer) {
	if live == nil || slice.TierLabel(live.Tier) == "free" {
		return
	}

	cfg := live.Config
	classes := []idleClass{
		idleFunctions(m, cfg.Atomic.Functions, deployed),
		idleStorage("collection", "collections", cfg.Backbone.NoSQL.Collections,
			entryNames(m, "slot", "backbone", "nosql")),
		idleStorage("bucket", "buckets", cfg.Backbone.Blobs.Buckets,
			entryNames(m, "name", "backbone", "blobs")),
		idleStorage("database", "databases", cfg.Backbone.SQL.Databases,
			entryNames(m, "name", "backbone", "sql")),
		idleQueues(m, cfg.Backbone.Queues.Queues),
	}

	total := 0
	for _, c := range classes {
		total += len(c.lines)
	}
	if total == 0 {
		return
	}

	fmt.Fprintln(out)
	for _, c := range classes {
		if len(c.lines) == 0 {
			continue
		}
		fmt.Fprintf(out, "  %s This slice declares %d %s and your Driftfile names %s.\n",
			common.Hint("!"), c.declared, plural(c.declared, c.one, c.many), namedCount(c.named))
		for _, line := range c.lines {
			fmt.Fprintf(out, "      %s\n", line)
		}
	}
	fmt.Fprintf(out, "      Run `drift slice resize %s` to release %s.\n",
		m.Name(), plural(total, "it", "them"))
}

// idleFunctions reports the declared slots the manifest does not name and
// nothing runs, each with the memory it books.
//
// Pulse slots and Field slots are one set, as the slice holds them: a Field's
// slot is `field:<name>`, named when this manifest declares that Field, running
// when its record is deployed, and idle otherwise, exactly as a Pulse's is.
func idleFunctions(m *Manifest, slots []AtomicFunction, deployed []atomic_cmd.DeployedFunction) idleClass {
	c := idleClass{one: "atomic slot", many: "atomic slots", declared: len(slots)}
	named := stringSet(namedFunctions(m))

	var serving map[string]bool
	if deployed != nil {
		serving = make(map[string]bool, len(deployed))
		for _, d := range deployed {
			serving[d.Key] = true
		}
	}

	ordered := append([]AtomicFunction(nil), slots...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for _, s := range ordered {
		if named[s.Name] {
			c.named++
			continue
		}
		cost := "you are paying for " + bookingLabel(s.MemoryBytes)
		switch {
		case serving == nil:
			c.lines = append(c.lines, fmt.Sprintf("%s is not named by your Driftfile, and %s.", s.Name, cost))
		case serving[atomic_cmd.DeployedKey(specMethodAndPath(s.Name))]:
			// Running something this file does not name, so not idle, and
			// already reported by ReportOrphanedFunctions.
		default:
			c.lines = append(c.lines, fmt.Sprintf("%s is idle, and %s.", s.Name, cost))
		}
	}
	return c
}

// idleStorage reports the declared collections, buckets or databases the
// manifest does not name. Each is billed for the bytes it declares whether or
// not anything writes to it, which is why the size is the figure given.
//
// Unnamed is all that can be said: code may write to a declared name the
// Driftfile never lists, so nothing here calls one idle.
func idleStorage(one, many string, declared map[string]int, named []string) idleClass {
	c := idleClass{one: one, many: many, declared: len(declared)}
	isNamed := stringSet(named)
	for _, name := range sortedKeys(declared) {
		if isNamed[name] {
			c.named++
			continue
		}
		c.lines = append(c.lines, fmt.Sprintf("%s is not named by your Driftfile, and you are paying for %s.",
			name, storageLabel(declared[name])))
	}
	return c
}

// idleQueues reports the declared queues the manifest does not name. A queue is
// named by a `backbone.queues[]` entry or by a queue-triggered function draining
// it. A queue declares a depth and no bytes, so it is not billed, and the line
// says so rather than claiming a charge.
func idleQueues(m *Manifest, declared map[string]int) idleClass {
	c := idleClass{one: "queue", many: "queues", declared: len(declared)}
	named := stringSet(entryNames(m, "name", "backbone", "queues"))
	for _, s := range FunctionSpecs(m) {
		if q := s.QueueSource(); q != "" {
			named[q] = true
		}
	}
	for _, name := range sortedKeys(declared) {
		if named[name] {
			c.named++
			continue
		}
		c.lines = append(c.lines, fmt.Sprintf("%s is not named by your Driftfile; queues are not billed, so this one is free.", name))
	}
	return c
}

// bookingLabel names a slot's memory booking. A slot declaring none is still
// one, and is named without a figure rather than as zero.
func bookingLabel(bytes int) string {
	if bytes <= 0 {
		return "its booking"
	}
	return "its " + sizeLabel(bytes) + " booking"
}

// storageLabel names a declared size, which is what storage is billed on.
func storageLabel(bytes int) string {
	if bytes <= 0 {
		return "its declared storage"
	}
	return "its " + sizeLabel(bytes) + " of declared storage"
}

// namedCount says how many of a declared set the Driftfile names, in words when
// it names none.
func namedCount(n int) string {
	if n == 0 {
		return "none of them"
	}
	return strconv.Itoa(n)
}

func stringSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
