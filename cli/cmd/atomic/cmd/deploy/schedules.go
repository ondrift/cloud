// schedules.go — a function's `cron` in the Driftfile, carried into the deploy
// artifact.
//
// # Why this goes on the artifact and not through the trigger API
//
// There are two ways to register a schedule with a slice, and only one of them
// is metered. A schedule posted to the api's `/ops/trigger/register` proxies
// straight through to the slice — the operator never sees it, so
// `enforceScheduleLimit` never runs. The deploy artifact's `Triggers` field goes
// through the operator, which counts the new schedules, checks them against the
// slice's envelope, and only then registers. The CLI has no command that posts
// a schedule to the first route.
//
// Scheduled jobs are priced (tier.CentsPerScheduledJob). A schedule the
// envelope never sees is a schedule nobody is billed for, so the metered path
// is the only correct one — which is why the Driftfile field is wired here
// rather than as a post-deploy reconcile pass.
//
// # Why a package-level registry
//
// The deploy functions are called from `drift apply`, which has read the
// Driftfile and publishes here before shipping anything.
//
// Threading a manifest through three deploy signatures to carry one optional
// map would put the Driftfile's whole vocabulary into paths that need one field
// of it. Same shape as the existing `atomicForce` flag, and for the same reason.

package atomic_cmd

import "sync"

var (
	scheduleMu        sync.RWMutex
	declaredSchedules map[string]string
)

// SetDeclaredSchedules records the functions' `cron` expressions from a Driftfile,
// keyed by function name. Called once by every deploy path before any function
// ships; a nil or empty map clears it.
//
// THE KEY IS THE FULL DECLARED NAME — the composite `method:route`, e.g.
// `get:cronprobe` — because that is what `normaliseFunctionIdentities` writes
// into every entry and therefore what `FunctionSpec.Name` holds. It is NOT the
// route: a lookup by route misses the map and drops the schedule without a
// word, which TestADeclaredCronSurvivesTheWholeDeployPath guards.
func SetDeclaredSchedules(byFunction map[string]string) {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	if len(byFunction) == 0 {
		declaredSchedules = nil
		return
	}
	declaredSchedules = make(map[string]string, len(byFunction))
	for k, v := range byFunction {
		if v != "" {
			declaredSchedules[k] = v
		}
	}
}

// DeclaredScheduleFor returns the cron expression declared for a function, or
// "" if the Driftfile declared none.
func DeclaredScheduleFor(functionName string) string {
	scheduleMu.RLock()
	defer scheduleMu.RUnlock()
	return declaredSchedules[functionName]
}

// scheduleTriggerFor returns the TriggerSpec for a function's declared cron, or
// nil when it has none.
//
// method matters: the slice's trigger registry looks a function up by
// (method, path), so a schedule registered under the wrong verb resolves to
// nothing and fires into a 404 forever — silently, because a trigger that
// cannot find its function still exists and still ticks. It is the function's
// OWN method because a function's `cron` is additive: same code, two triggers.
func scheduleTriggerFor(functionName, method string) []TriggerSpec {
	expr := DeclaredScheduleFor(functionName)
	if expr == "" {
		return nil
	}
	if method == "" {
		method = "POST"
	}
	return []TriggerSpec{{
		Type:     "schedule",
		Schedule: expr,
		Method:   method,
	}}
}
