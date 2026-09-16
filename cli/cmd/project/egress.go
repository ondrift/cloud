// egress.go — Driftfile reconcile of the per-slice outbound egress
// allowlist. Compares `slice.atomic.egress` from the Driftfile
// against the live mode + declared hosts on the slice, and triggers
// a refresh if anything changed. The operator does the DNS
// resolution and pushes the rendered IP/port list into the slice's
// NetworkPolicy via charter — the CLI's only job is to decide
// whether a refresh is needed.
//
// Reconcile semantics:
//
//   - Driftfile has no egress block (or `mode` is empty / "open"),
//     and live is also open → no-op.
//   - Driftfile is open, live is allowlist → POST refresh; operator
//     re-renders the chart with `mode: open` and the slice goes
//     back to "any public host."
//   - Driftfile is allowlist, live is open OR list differs → POST
//     refresh; operator re-resolves and pushes the new IP set.
//   - Driftfile is allowlist, live matches exactly → no-op.
package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// liveEgressView is the JSON shape returned by GET /ops/atomic/egress.
type liveEgressView struct {
	Mode          string   `json:"mode"`
	DeclaredHosts []string `json:"declared_hosts"`
}

func applyEgress(m *Manifest) error {
	declaredMode, declaredHosts := desiredEgress(m)

	live, err := fetchLiveEgress()
	if err != nil {
		// The endpoint may not be available against an older operator.
		// Surface the warning and skip — egress is additive; not
		// applying it shouldn't abort the rest of the deploy.
		fmt.Printf("  %s egress reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}

	if egressInSync(declaredMode, declaredHosts, live) {
		// Don't print anything when there's nothing to say — matches
		// the rest of the apply* helpers' quiet path.
		return nil
	}

	// THE DECLARATION TRAVELS WITH THE REFRESH. The platform stores what arrives
	// here and renders it into the slice's NetworkPolicy; a refusal (a wildcard
	// host, an unknown mode) comes back as a 400 naming the entry.
	if err := refreshEgress(declaredMode, declaredHosts); err != nil {
		// Loud, and it fails the apply. This used to print a hint and return nil,
		// which was right while the declaration reached nothing — there was no
		// control to lose. Now there is: swallowing the error would leave the
		// slice on its previous posture while the deploy reported Done, which is
		// the shape of every "reports success while doing nothing" defect this
		// codebase keeps finding.
		return fmt.Errorf("egress: %w", err)
	}

	// The ✓ the old comment here promised. It is earned now: the declaration is
	// stored, resolved and rendered before this returns.
	if declaredMode == "allowlist" {
		fmt.Printf("  %s egress allowlist applied (%d host%s)\n",
			common.Check(), len(declaredHosts), pluralS(len(declaredHosts)))
	} else {
		fmt.Printf("  %s egress mode open\n", common.Hint("·"))
	}
	return nil
}

// desiredEgress folds the Driftfile's egress block into the
// (mode, hosts) tuple the rest of this file works in. Default
// (block absent or mode unset) is "open" with no hosts.
func desiredEgress(m *Manifest) (mode string, hosts []string) {
	if m.Slice().Sub("atomic", "egress") == nil {
		return "open", nil
	}
	mode = strings.ToLower(strings.TrimSpace(m.Slice().Str("atomic", "egress", "mode")))
	if mode == "" {
		mode = "open"
	}
	hosts = append(hosts, m.Slice().Strings("atomic", "egress", "hosts")...)
	return mode, hosts
}

// egressInSync compares the desired mode + host list against the
// live view. Hosts are compared as sorted, lowercased, trimmed
// strings — the user can re-order their Driftfile entries without
// triggering a refresh. Mode "" is normalised to "open" on the
// live side.
func egressInSync(mode string, hosts []string, live liveEgressView) bool {
	liveMode := strings.ToLower(strings.TrimSpace(live.Mode))
	if liveMode == "" {
		liveMode = "open"
	}
	if mode != liveMode {
		return false
	}
	if mode == "open" {
		return true // hosts are irrelevant when mode is open
	}
	return normaliseHosts(hosts) == normaliseHosts(live.DeclaredHosts)
}

func normaliseHosts(in []string) string {
	cp := make([]string, 0, len(in))
	for _, h := range in {
		s := strings.ToLower(strings.TrimSpace(h))
		if s != "" {
			cp = append(cp, s)
		}
	}
	sort.Strings(cp)
	return strings.Join(cp, "\x00")
}

func fetchLiveEgress() (liveEgressView, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/egress", nil)
	if err != nil {
		return liveEgressView{}, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "fetch egress")
	if err != nil {
		return liveEgressView{}, err
	}
	var v liveEgressView
	if err := json.Unmarshal(body, &v); err != nil {
		return liveEgressView{}, err
	}
	return v, nil
}

// refreshEgress sends the Driftfile's declaration and asks the operator to
// resolve and apply it.
//
// IT MUST DECLARE A CONTENT TYPE. The route gates on
// `Content-Type: application/json` before it looks at anything else, and
// `common.DoRequest` sets none — so this once POSTed without one and came back
// `415 Content-Type must be application/json`, on every deploy whose egress
// block differed from the live one. The failure was invisible, because the
// caller printed it as a hint and returned nil.
//
// `mode` IS ALWAYS SENT, including "open". The operator tells an absent mode
// from `"open"` and treats only the absent one as "re-resolve what you hold" —
// so omitting it when a tenant removes their allowlist would leave the old one
// in force, and the slice would stay locked down after the manifest said it
// should not be.
func refreshEgress(mode string, hosts []string) error {
	if hosts == nil {
		hosts = []string{}
	}
	body, err := json.Marshal(map[string]any{"mode": mode, "hosts": hosts})
	if err != nil {
		return err
	}
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/atomic/egress/refresh", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "refresh egress")
		return e
	}
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
