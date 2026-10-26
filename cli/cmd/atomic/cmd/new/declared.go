// declared.go — does the Driftfile beside us already declare this function?
//
// ONE QUESTION, AND IT FAILS OPEN. `drift file new` scaffolds a Driftfile that
// declares `function "GET /hello"` and prints the `drift atomic new` command that
// writes its source. A user who follows that hint would otherwise be handed a
// block to paste that duplicates one three lines above, and two functions on one
// trigger is a compile error, not a harmless repeat.
//
// This is NOT a second Driftfile reader and must not grow into one.
// `cmd/project` owns reading: variables, environments, the schema, handlers and
// backends. This reads the function labels to decide which of two HINTS to print,
// so every failure — no file, unreadable, unparseable — answers "not declared"
// and prints the snippet. The snippet is the safe answer in every ambiguous
// case: a user pasting a block that already exists sees a lint error naming the
// duplicate, while a user never shown the block has nothing to paste at all.
//
// It deliberately does NOT import cmd/project. That package's deploy path
// imports this one's sibling under cmd/atomic, and a hint is not worth an
// import cycle.
package atomic_cmd_new

import (
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// functionLabel is how the Driftfile labels a function: its method and route, or
// QUEUE and the queue it drains.
func functionLabel(funcMethod, route string) string {
	if funcMethod == "queue" {
		return "QUEUE " + route
	}
	return strings.ToUpper(funcMethod) + " /" + route
}

// declaredRoute reports whether the Driftfile in the working directory already
// declares this function, in any backend.
//
// `method` is the Driftfile's own vocabulary, so a queue function is `queue`
// with the QUEUE name as its route — which is what `runNew` resolved before
// calling here.
func declaredRoute(funcMethod, name, queue string, isQueue bool) bool {
	route, method := name, funcMethod
	if isQueue {
		route, method = queue, "queue"
	}
	if route == "" {
		return false
	}

	data, err := os.ReadFile("Driftfile")
	if err != nil {
		return false
	}
	f, diags := hclsyntax.ParseConfig(data, "Driftfile", hcl.InitialPos)
	if diags.HasErrors() {
		return false
	}
	want := functionLabel(method, route)
	for _, top := range f.Body.(*hclsyntax.Body).Blocks {
		if top.Type != "atomic" {
			continue
		}
		for _, el := range top.Body.Blocks {
			if el.Type != "backend" {
				continue
			}
			for _, p := range el.Body.Blocks {
				if p.Type == "function" && len(p.Labels) == 1 && strings.EqualFold(p.Labels[0], want) {
					return true
				}
			}
		}
	}
	return false
}
