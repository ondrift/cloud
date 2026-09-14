// declared.go — does the Driftfile beside us already list this function?
//
// ONE QUESTION, AND IT FAILS OPEN. `drift file new` scaffolds a Driftfile that
// declares `route: hello, method: get` and prints the `drift atomic new` command
// that writes its source. A user who follows that hint would otherwise be told
// "Declare it in your Driftfile, under atomic.functions:" and handed a duplicate
// of an entry three lines above the one they just read — and two entries for one
// route is a parse error, not a harmless repeat.
//
// This is NOT a second Driftfile parser and must not grow into one. `cmd/project`
// owns parsing: it resolves environments, validates against the served schema,
// resolves handlers and builds elements. This reads one list to decide which of
// two HINTS to print, so every failure — no file, unreadable, unparseable, a
// shape it does not recognise — answers "not declared" and prints the snippet.
// The snippet is the safe answer in every ambiguous case: a user pasting an entry
// that already exists sees a lint error naming the duplicate, while a user never
// shown the entry has nothing to paste at all.
//
// It deliberately does NOT import cmd/project. That package's deploy path imports
// this one's sibling under cmd/atomic, and a hint is not worth an import cycle.
package atomic_cmd_new

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// driftfileRoutes is the sliver of a Driftfile this file reads. Every other key
// is ignored — including `environments`, which can override the function list
// per environment. A per-environment override is exactly the ambiguous case the
// fail-open rule is for: this answers for the base list, and when the base list
// does not name the function the snippet is printed.
type driftfileRoutes struct {
	Atomic struct {
		Functions []struct {
			Route  string `yaml:"route"`
			Method string `yaml:"method"`
		} `yaml:"functions"`
	} `yaml:"atomic"`
}

// declaredRoute reports whether the Driftfile in the working directory already
// declares this route and method.
//
// `method` is the Driftfile's own vocabulary, so a queue function is
// `method: queue` with the QUEUE name as its route — which is what `runNew`
// resolved before calling here.
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
	var df driftfileRoutes
	if yaml.Unmarshal(data, &df) != nil {
		return false
	}
	for _, fn := range df.Atomic.Functions {
		if fn.Route == route && strings.EqualFold(fn.Method, method) {
			return true
		}
	}
	return false
}
