package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestEdgeHosts_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the preflight: %v", err)
	}
	if err := applyEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeHosts(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.hosts printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, and the plan says so.
func TestEdgeHosts_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"hosts": []any{
		map[string]any{"host": "admin.example.com", "slice": "admin"},
	}}}}
	for name, err := range map[string]error{"preflight": preflightEdgeHosts(m), "apply": applyEdgeHosts(m)} {
		if err == nil || err.Error() != "edge.hosts is not supported by this CLI yet" {
			t.Errorf("%s: err = %v, want the not-supported refusal", name, err)
		}
	}
	var out bytes.Buffer
	planEdgeHosts(m, &out)
	if !strings.Contains(out.String(), "edge.hosts is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}

// The refusal comes before anything reaches the slice.
func TestApply_RefusesEdgeHostsBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  hosts:\n    - host: admin.example.com\n      slice: admin\n",
		"edge.hosts is not supported by this CLI yet")
}
