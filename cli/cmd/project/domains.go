// domains.go — Driftfile reconcile of the `domain` blocks in `edge`. Adds
// any host that's declared in the file but missing on the slice, as a
// wildcard when its block says `wildcard = true`. A host the file does not
// name is left attached and reported instead (see unnamed.go): a project
// declares the hosts it wants, and two projects can share a slice.
// After add, the user still has to flip DNS (the instructions are printed) and run
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

// liveDomain is one row of the platform's GET /ops/slice/domain.
type liveDomain struct {
	Host string `json:"host"`
	// Status is where the domain stands: pending, failed, verified or live.
	Status string `json:"status"`
	// StatusDetail is the platform's reason for the status, as for a domain whose
	// DNS record it did not find.
	StatusDetail string `json:"status_detail"`
	// Wildcard is whether every subdomain of Host routes to the slice. The
	// platform leaves it out when it is off.
	Wildcard bool `json:"wildcard"`
}

// declaredDomain is one host the Driftfile declares, and whether its block asks
// for a wildcard.
type declaredDomain struct {
	Host     string
	Wildcard bool
}

// declaredDomains is the hosts the Driftfile declares, in the file's order and
// lowercased, because that is how the slice stores a host. A host the file
// declares twice is one.
func declaredDomains(m *Manifest) []declaredDomain {
	var out []declaredDomain
	seen := map[string]bool{}
	for _, d := range m.Slice().Entries("host", "edge", "domains") {
		host := strings.ToLower(strings.TrimSpace(d.Str("host")))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		wildcard, _ := d["wildcard"].(bool)
		out = append(out, declaredDomain{Host: host, Wildcard: wildcard})
	}
	return out
}

// applyDomains attaches every host the Driftfile declares, reading
// the live set to skip the ones already there. The function is
// best-effort: failures are surfaced but don't abort the deploy —
// the rest of the slice (atomic, backbone, canvas) is more important
// than the cosmetic step of attaching a hostname.
func applyDomains(m *Manifest) error {
	declared := declaredDomains(m)

	live, err := fetchLiveDomains()
	if err != nil {
		fmt.Printf("  %s domain reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}

	liveByHost := map[string]liveDomain{}
	for _, d := range live {
		liveByHost[strings.ToLower(d.Host)] = d
	}

	added := 0

	for _, d := range declared {
		if _, ok := liveByHost[d.Host]; ok {
			continue
		}
		instructions, err := addDomain(d.Host, d.Wildcard)
		if err != nil {
			fmt.Printf("  %s add domain %s: %v\n", common.Hint("·"), d.Host, err)
			continue
		}
		added++
		fmt.Printf("  %s domain added: %s (verify with `drift slice domain verify %s` once DNS is live)\n",
			common.Check(), d.Host, d.Host)
		for _, line := range instructions {
			fmt.Printf("      %s\n", line)
		}
	}

	if added == 0 && len(declared) > 0 {
		fmt.Printf("  %s domains in sync (%d declared)\n", common.Hint("·"), len(declared))
	}
	return nil
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

// addDomain attaches one host and returns the DNS instructions the platform
// answers with, which are the only place the TXT record to create is spelled
// out.
func addDomain(host string, wildcard bool) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"host": host, "verify": "dns-txt", "wildcard": wildcard})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/slice/domain", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := common.CheckResponse(resp, "add domain")
	if err != nil {
		return nil, err
	}
	var d struct {
		Instructions []string `json:"instructions"`
	}
	_ = json.Unmarshal(respBody, &d)
	return d.Instructions, nil
}
