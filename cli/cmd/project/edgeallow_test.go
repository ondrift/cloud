package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestEdgeAllow_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the preflight: %v", err)
	}
	if err := applyEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeAllow(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.allow printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, and the plan says so.
func TestEdgeAllow_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"allow": []any{"203.0.113.0/24"}}}}
	for name, err := range map[string]error{"preflight": preflightEdgeAllow(m), "apply": applyEdgeAllow(m)} {
		if err == nil || err.Error() != "edge.allow is not supported by this CLI yet" {
			t.Errorf("%s: err = %v, want the not-supported refusal", name, err)
		}
	}
	var out bytes.Buffer
	planEdgeAllow(m, &out)
	if !strings.Contains(out.String(), "edge.allow is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}

// The refusal comes before anything reaches the slice.
func TestApply_RefusesEdgeAllowBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  allow: [\"203.0.113.0/24\"]\n",
		"edge.allow is not supported by this CLI yet")
}
