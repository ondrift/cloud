// edgeallow.go — `edge.allow`, the inbound CIDR allowlist.
//
// Present, the list is sent to `POST /ops/slice/edge/allow` and replaces the
// slice's allowlist; an empty list clears it, so every address reaches the
// slice. Absent, the slice keeps the allowlist it has.
//
// Every entry is checked as a CIDR before anything deploys. A bare address is
// the commonest mistake, and is refused with the range that allows it alone.
package project

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

// declaredAllow reads `edge.allow` and checks every entry is a CIDR. It returns
// the entries as written.
func declaredAllow(m *Manifest) ([]string, error) {
	raw, ok := m.Slice().Get("edge", "allow").([]any)
	if !ok {
		return nil, fmt.Errorf(`edge.allow must be a list of CIDRs, such as ["203.0.113.0/24"]`)
	}
	cidrs := make([]string, 0, len(raw))
	for i, entry := range raw {
		s, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("edge.allow[%d]: %v is not a CIDR, such as 203.0.113.0/24 or 2001:db8::/32", i, entry)
		}
		if _, _, err := net.ParseCIDR(s); err != nil {
			if ip := net.ParseIP(s); ip != nil {
				single := s + "/128"
				if ip.To4() != nil {
					single = s + "/32"
				}
				return nil, fmt.Errorf("edge.allow[%d]: %q is an address, not a range; write %q to allow that one address", i, s, single)
			}
			return nil, fmt.Errorf("edge.allow[%d]: %q is not a CIDR, such as 203.0.113.0/24 or 2001:db8::/32", i, s)
		}
		cidrs = append(cidrs, s)
	}
	return cidrs, nil
}

// preflightEdgeAllow runs before the first deploy step, so an entry that is not
// a CIDR refuses the file before any of it is applied.
func preflightEdgeAllow(m *Manifest) error {
	if !m.Slice().Has("edge", "allow") {
		return nil
	}
	_, err := declaredAllow(m)
	return err
}

// applyEdgeAllow replaces the slice's allowlist with the declared one. Absent,
// it does nothing.
func applyEdgeAllow(m *Manifest) error {
	if !m.Slice().Has("edge", "allow") {
		return nil
	}
	cidrs, err := declaredAllow(m)
	if err != nil {
		return err
	}
	if _, err := callSliceEdge(m, http.MethodPost, "allow", "set the inbound allowlist", map[string][]string{"cidrs": cidrs}); err != nil {
		return fmt.Errorf("edge.allow: %w", err)
	}
	if len(cidrs) == 0 {
		fmt.Printf("  %s inbound allowlist cleared: every address reaches the slice\n", common.Hint("·"))
		return nil
	}
	fmt.Printf("  %s inbound allowlist: %d range%s\n", common.Check(), len(cidrs), pluralS(len(cidrs)))
	return nil
}

// planEdgeAllow is the `--plan` section for `edge.allow`: what the declared list
// adds to and removes from the live one, compared in canonical form.
func planEdgeAllow(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "allow") {
		return
	}
	fmt.Fprintf(out, "\n  inbound allowlist (edge.allow)\n")

	cidrs, err := declaredAllow(m)
	if err != nil {
		fmt.Fprintf(out, "    %s %v\n", common.Hint("!"), err)
		return
	}

	var live struct {
		CIDRs []string `json:"cidrs"`
	}
	body, lerr := callSliceEdge(m, http.MethodGet, "allow", "read the inbound allowlist", nil)
	if lerr == nil {
		lerr = json.Unmarshal(body, &live)
	}
	planListDiff(out, canonicalCIDRs(live.CIDRs), canonicalCIDRs(cidrs), lerr)
	if len(cidrs) == 0 {
		fmt.Fprintf(out, "    %s the list is empty: every address reaches the slice\n", common.Hint("·"))
	}
}

// canonicalCIDRs writes each range as its network, so 203.0.113.7/24 and
// 203.0.113.0/24 compare as the one range they are. An entry that does not
// parse is kept as written.
func canonicalCIDRs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n.String())
			continue
		}
		out = append(out, s)
	}
	return out
}
