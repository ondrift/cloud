package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestApplyEdgeRateLimit_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := applyEdgeRateLimit(m); err != nil {
		t.Errorf("an absent edge.rate_limit failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeRateLimit(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.rate_limit printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, so no release applies a file without the
// limit it states.
func TestApplyEdgeRateLimit_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"rate_limit": "600/min"}}}
	err := applyEdgeRateLimit(m)
	if err == nil || err.Error() != "edge.rate_limit is not supported by this CLI yet" {
		t.Errorf("err = %v, want the not-supported refusal", err)
	}
	var out bytes.Buffer
	planEdgeRateLimit(m, &out)
	if !strings.Contains(out.String(), "edge.rate_limit is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}
