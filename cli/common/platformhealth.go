package common

// platformhealth.go — answering "is this my fault?" before the user assumes it is.
//
// A 5xx or an unreachable API reads as your own code being wrong, and the user's
// next move is to go looking for a bug they did not write. The platform already
// publishes per-component health for the status page; consulting it at the moment
// of failure turns "something went wrong" into "Routing is degraded — this is not
// your code", which is the difference between a wasted afternoon and a coffee.
//
// # Only on failure, and never at the cost of the failure
//
// This runs on 5xx and on unreachable, and nowhere else: the happy path pays
// nothing. The check is best-effort with a short timeout, and every way it can go
// wrong ends in silence — an unreachable status page during an outage is the
// LIKELY case, and a diagnostic that fails loudly while diagnosing a failure has
// made the screen worse rather than better.
//
// It is deliberately a different host from the API. Asking the thing that just
// failed whether it is healthy answers nothing.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// StatusURL is where per-component health is published. A variable so a test can
// point it at a stub; there is no reason for a user to change it.
var StatusURL = "https://status.ondrift.eu/api/status"

// statusTimeout is short on purpose. The user is already waiting on a failed
// command, and a slow answer here is worse than none.
const statusTimeout = 3 * time.Second

type componentStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// DegradedComponents returns the names of components that are not operational.
//
// Empty means either "everything is fine" or "we could not tell", and callers
// must treat those the same: the only safe use of this is to ADD information
// when it is available, never to claim health it did not confirm.
func DegradedComponents() []string {
	client := &http.Client{Timeout: statusTimeout}
	resp, err := client.Get(StatusURL)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var components []componentStatus
	if derr := json.NewDecoder(resp.Body).Decode(&components); derr != nil {
		return nil
	}

	var degraded []string
	for _, c := range components {
		if strings.EqualFold(c.Status, "operational") || c.Name == "" {
			continue
		}
		degraded = append(degraded, c.Name)
	}
	return degraded
}

// platformFaultNote renders the line that tells a user this is not theirs, or ""
// when nothing can be said.
//
// "" is the common case and the important one. A component list that came back
// clean means the platform believes it is healthy, and saying so beside a 5xx
// would be asserting something this cannot know — the failure is real whatever
// the status page thinks. Silence leaves the original message intact.
func platformFaultNote(degraded []string) string {
	if len(degraded) == 0 {
		return ""
	}
	if len(degraded) == 1 {
		return "  " + degraded[0] + " is not operational right now — this is the platform, not your code."
	}
	return "  " + strings.Join(degraded, ", ") + " are not operational right now — this is the platform, not your code."
}
