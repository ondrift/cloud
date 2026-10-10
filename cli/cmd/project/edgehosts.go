// edgehosts.go — `edge.hosts`, fronting: a host this slice owns, served by a
// slice it links.
//
// Present, the list is sent to `POST /ops/slice/edge/hosts` and replaces the
// hosts this slice fronts; an empty list clears them. Absent, the slice keeps
// the ones it has. It runs after `edge.links` in the same apply, because a
// fronted host names a slice this one must already link.
//
// Whether a host is a verified custom domain of this slice, and whether the
// slice it names is linked, is the server's to decide. Its refusal names the
// command that fixes it, and reaches the reader as the server wrote it.
package project

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// frontedHost is one `edge.hosts` entry, in the shape the route takes.
type frontedHost struct {
	Host  string `json:"host"`
	Slice string `json:"slice"`
}

func (h frontedHost) String() string { return h.Host + " → " + h.Slice }

// declaredHosts reads `edge.hosts`, refusing an entry that lacks either half.
// Hosts are lowercased, as the slice stores them.
func declaredHosts(m *Manifest) ([]frontedHost, error) {
	raw, ok := m.Slice().Get("edge", "hosts").([]any)
	if !ok {
		return nil, fmt.Errorf("edge.hosts must be a list of {host, slice} entries")
	}
	hosts := make([]frontedHost, 0, len(raw))
	for i, entry := range raw {
		n, _ := asNode(entry)
		h := frontedHost{
			Host:  strings.ToLower(strings.TrimSpace(n.Str("host"))),
			Slice: strings.TrimSpace(n.Str("slice")),
		}
		if h.Host == "" || h.Slice == "" {
			return nil, fmt.Errorf(`edge.hosts[%d]: an entry needs both a host and a slice, such as "admin.example.com" = "admin"`, i)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// preflightEdgeHosts runs before the first deploy step, so an entry that cannot
// be sent refuses the file before any of it is applied.
func preflightEdgeHosts(m *Manifest) error {
	if !m.Slice().Has("edge", "hosts") {
		return nil
	}
	_, err := declaredHosts(m)
	return err
}

// applyEdgeHosts replaces the hosts this slice fronts with the declared ones.
// Absent, it does nothing.
func applyEdgeHosts(m *Manifest) error {
	if !m.Slice().Has("edge", "hosts") {
		return nil
	}
	hosts, err := declaredHosts(m)
	if err != nil {
		return err
	}
	if _, err := callSliceEdge(m, http.MethodPost, "hosts", "set the fronted hosts", map[string][]frontedHost{"hosts": hosts}); err != nil {
		return fmt.Errorf("edge.hosts: %w", err)
	}
	if len(hosts) == 0 {
		fmt.Printf("  %s fronted hosts cleared\n", common.Hint("·"))
		return nil
	}
	for _, h := range hosts {
		fmt.Printf("  %s fronted host: %s\n", common.Check(), h)
	}
	return nil
}

// hostLines renders each entry as `host → slice`, the form the plan compares.
func hostLines(hosts []frontedHost) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h.Host = strings.ToLower(h.Host)
		out = append(out, h.String())
	}
	return out
}
