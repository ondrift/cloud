package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: a file that does not mention the key must apply
// exactly as it did.
func TestEdgeSiteAuth_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeSiteAuth(m); err != nil {
		t.Errorf("an absent edge.site_auth failed the preflight: %v", err)
	}
	if err := applyEdgeSiteAuth(m); err != nil {
		t.Errorf("an absent edge.site_auth failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeSiteAuth(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.site_auth printed a plan line: %q", out.String())
	}
}

// Present, it is refused by name, and the plan says so.
func TestEdgeSiteAuth_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"site_auth": map[string]any{
		"users": []any{map[string]any{"user": "alice", "password": "$SITE_PASS"}},
	}}}}
	for name, err := range map[string]error{"preflight": preflightEdgeSiteAuth(m), "apply": applyEdgeSiteAuth(m)} {
		if err == nil || err.Error() != "edge.site_auth is not supported by this CLI yet" {
			t.Errorf("%s: err = %v, want the not-supported refusal", name, err)
		}
	}
	var out bytes.Buffer
	planEdgeSiteAuth(m, &out)
	if !strings.Contains(out.String(), "edge.site_auth is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}

// The refusal comes before anything reaches the slice, so a gate this CLI
// cannot apply never leaves a half-applied file live behind it.
func TestApply_RefusesEdgeSiteAuthBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  site_auth:\n    users:\n      - user: alice\n        password: $SITE_PASS\n",
		"edge.site_auth is not supported by this CLI yet")
}
