package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestApplyEdgeAuth_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := applyEdgeAuth(m); err != nil {
		t.Errorf("an absent edge.auth failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeAuth(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.auth printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, so no release applies a file without the
// gate it states.
func TestApplyEdgeAuth_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"auth": map[string]any{
		"users": []any{map[string]any{"user": "alice", "password": "$SITE_PASS"}},
	}}}}
	err := applyEdgeAuth(m)
	if err == nil || err.Error() != "edge.auth is not supported by this CLI yet" {
		t.Errorf("err = %v, want the not-supported refusal", err)
	}
	var out bytes.Buffer
	planEdgeAuth(m, &out)
	if !strings.Contains(out.String(), "edge.auth is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}
