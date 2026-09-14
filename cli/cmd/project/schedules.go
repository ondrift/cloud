// schedules.go — resolving `atomic.functions[].cron` into what the deploy
// artifact carries.
//
// The Driftfile field was documented with worked examples and never read by any
// deploy code; `translate.go` called it "vestigial" while the spec called it the
// feature. This is the half that makes the spec true.

package project

import (
	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// declaredSchedules maps function name → cron expression for every Driftfile
// entry carrying a `cron:`.
//
// The key is `name` AFTER normaliseFunctionIdentities has run, so it is the
// composite `method:route` — `get:cronprobe`, not `cronprobe`. Every entry has
// one by the time a Manifest exists, whichever spelling the Driftfile used,
// which is why this reads a single key rather than recomposing one.
//
// The other end of that agreement is triggersFor, which must look the schedule
// up by the SAME string. It did not, for the whole life of this feature: it
// asked for the route half, missed every time, and shipped an artifact with no
// trigger on it. TestTheScheduleKeyIsTheKeyTheDeployLooksUp is the seam.
func declaredSchedules(m *Manifest) map[string]string {
	out := map[string]string{}
	for _, fn := range m.Slice().Entries("name", "atomic", "functions") {
		if fn.Str("cron") != "" && fn.Str("name") != "" {
			out[fn.Str("name")] = fn.Str("cron")
		}
	}
	return out
}

// ScheduledFunctionNames lists the functions the Driftfile puts on a schedule,
// for the deploy summary.
func ScheduledFunctionNames(m *Manifest) []string {
	var names []string
	for _, fn := range m.Slice().Entries("name", "atomic", "functions") {
		if fn.Str("cron") != "" && fn.Str("name") != "" {
			names = append(names, fn.Str("name"))
		}
	}
	return names
}

// compile-time proof the deploy package still exposes the setter this file
// feeds; a rename there should break here rather than silently stop scheduling.
var _ = atomic_cmd.SetDeclaredSchedules
