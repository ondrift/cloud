package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestApplyEdgeAllow_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := applyEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeAllow(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.allow printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, so no release applies a file without the
// allowlist it states.
func TestApplyEdgeAllow_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"allow": []any{"203.0.113.0/24"}}}}
	err := applyEdgeAllow(m)
	if err == nil || err.Error() != "edge.allow is not supported by this CLI yet" {
		t.Errorf("err = %v, want the not-supported refusal", err)
	}
	var out bytes.Buffer
	planEdgeAllow(m, &out)
	if !strings.Contains(out.String(), "edge.allow is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}
