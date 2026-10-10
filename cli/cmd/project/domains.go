// domains.go — Driftfile reconcile of the `domain` blocks in `edge`. Adds
// any host that's declared in the file but missing on the slice, as a
// wildcard when its block says `wildcard = true`. A host the file does not
// name is left attached and reported instead (see unnamed.go): a project
// declares the hosts it wants, and two projects can share a slice.
// After add, the user still has to flip DNS and run
// `drift slice domain verify <host>` — that step requires the
// CNAME / TXT to be live and is not something we can do
// automatically from the deploy path.
package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

type liveDomain struct {
	Host   string `json:"host"`
	Status string `json:"status"`
}

// applyDomains attaches every host the Driftfile declares, reading
// the live set to skip the ones already there and to return the ones
// this manifest does not name. The function is
// best-effort: failures are surfaced but don't abort the deploy —
// the rest of the slice (atomic, backbone, canvas) is more important
// than the cosmetic step of attaching a hostname.
func applyDomains(m *Manifest) ([]string, error) {
	// declared maps each host the file names to whether it is a wildcard.
	declared := map[string]bool{}
	for _, d := range m.Slice().Entries("host", "edge", "domains") {
		host := strings.ToLower(strings.TrimSpace(d.Str("host")))
		if host == "" {
			continue
		}
		wildcard, _ := d["wildcard"].(bool)
		declared[host] = wildcard
	}

	live, err := fetchLiveDomains()
	if err != nil {
		fmt.Printf("  %s domain reconcile skipped: %v\n", common.Hint("·"), err)
		return nil, nil
	}

	liveByHost := map[string]liveDomain{}
	for _, d := range live {
		liveByHost[strings.ToLower(d.Host)] = d
	}

	added := 0

	for host, wildcard := range declared {
		if _, ok := liveByHost[host]; ok {
			continue
		}
		if err := addDomain(host, wildcard); err != nil {
			fmt.Printf("  %s add domain %s: %v\n", common.Hint("·"), host, err)
			continue
		}
		added++
		fmt.Printf("  %s domain added: %s (verify with `drift slice domain verify %s` once DNS is live)\n",
			common.Check(), host, host)
	}

	if added == 0 && len(declared) > 0 {
		fmt.Printf("  %s domains in sync (%d declared)\n", common.Hint("·"), len(declared))
	}

	// Both sides are lowercased, because that is how a host is declared here and
	// how the slice stores one: a manifest naming SHOP.example.com declares the
	// live shop.example.com, and reporting it as unnamed would be a lie.
	liveHosts := make([]string, 0, len(live))
	for _, d := range live {
		liveHosts = append(liveHosts, strings.ToLower(strings.TrimSpace(d.Host)))
	}
	declaredHosts := make(map[string]bool, len(declared))
	for host := range declared {
		declaredHosts[host] = true
	}
	return unnamedIn(liveHosts, declaredHosts), nil
}

func fetchLiveDomains() ([]liveDomain, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/domain", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list domains")
	if err != nil {
		return nil, err
	}
	var out []liveDomain
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// addDomain attaches one host, sending the same fields `drift slice domain
// add` does.
func addDomain(host string, wildcard bool) error {
	body, _ := json.Marshal(map[string]any{"host": host, "verify": "dns-txt", "wildcard": wildcard})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/slice/domain", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "add domain")
		return e
	}
	return nil
}
