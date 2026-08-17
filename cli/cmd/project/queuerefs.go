package project

// queuerefs.go — does a queue-triggered function name a queue this file declares?
//
// A `method: queue` function consumes the queue its route names, and a
// `backbone.queues[]` entry is what declares one. Nothing joined the two, so a
// trigger can point at a queue the file never mentions and nothing says so.
//
// It WARNS rather than refuses, and the reason is worth keeping: an undeclared
// queue write is COUNTED AND ALLOWED by the slice today (the refusal exists but
// is not enabled for queues — see drift_backbone_undeclared_writes_total). So a
// queue springs into being on its first push, and a trigger on an undeclared
// queue genuinely fires. Refusing would break a setup that works.
//
// What it is, then, is a declaration gap rather than a broken reference: the
// queue has no declared depth, no line on the bill, and nothing on the slice
// records that it should exist. Saying so is useful; stopping the deploy is not,
// and would stop being merely annoying the day the queue refusal is enabled, at
// which point this becomes the warning that predicted it.
//
// It runs in the PARSE, beside checkLocalPaths rather than inside it: that pass
// may ask the filesystem and nothing else, and this one asks only the document.
// Being in the parse is what gives `drift file lint` the check with no network
// and no session, which is where a CI gate wants it.

import (
	"fmt"
	"sort"
	"strings"
)

// checkQueueReferences reports every queue-triggered function whose queue the
// document does not declare.
//
// The trigger is read through FunctionSpec.QueueSource so this needs no
// knowledge of how an identity is spelled — the parser has already resolved
// `route` + `method` into the one composite the rest of the CLI speaks.
func checkQueueReferences(m *Manifest) []string {
	declared := map[string]bool{}
	for _, q := range m.Slice().Entries("name", "backbone", "queues") {
		if name := strings.TrimSpace(q.Str("name")); name != "" {
			declared[name] = true
		}
	}

	missing := map[string][]string{}
	for _, s := range FunctionSpecs(m) {
		q := s.QueueSource()
		if q == "" || declared[q] {
			continue
		}
		missing[q] = append(missing[q], s.Name)
	}
	if len(missing) == 0 {
		return nil
	}

	queues := make([]string, 0, len(missing))
	for q := range missing {
		queues = append(queues, q)
	}
	sort.Strings(queues)

	out := make([]string, 0, len(queues))
	for _, q := range queues {
		fns := missing[q]
		sort.Strings(fns)
		out = append(out, fmt.Sprintf(
			"function %s is triggered by queue %q, which this Driftfile does not declare. "+
				"It will still fire — an undeclared queue is created on its first push — but the "+
				"queue has no declared depth and nothing records that it should exist. "+
				"Add it under `backbone.queues`.",
			strings.Join(fns, ", "), q))
	}
	return out
}
