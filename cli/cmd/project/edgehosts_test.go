package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestApplyEdgeHosts_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := applyEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeHosts(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.hosts printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, so no release applies a file without the
// fronting it states.
func TestApplyEdgeHosts_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"hosts": []any{
		map[string]any{"host": "admin.example.com", "slice": "admin"},
	}}}}
	err := applyEdgeHosts(m)
	if err == nil || err.Error() != "edge.hosts is not supported by this CLI yet" {
		t.Errorf("err = %v, want the not-supported refusal", err)
	}
	var out bytes.Buffer
	planEdgeHosts(m, &out)
	if !strings.Contains(out.String(), "edge.hosts is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}
