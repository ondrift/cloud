package project

import (
	"bytes"
	"strings"
	"testing"
)

// Absent, nothing happens: no `edge.links` key means the live links are left
// alone.
func TestEdgeLinks_AbsentDoesNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeLinks(m); err != nil {
		t.Errorf("an absent edge.links failed the preflight: %v", err)
	}
	if err := applyEdgeLinks(m); err != nil {
		t.Errorf("an absent edge.links failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeLinks(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.links printed a plan line: %q", out.String())
	}
}

// Present — even as an empty list, which declares "no links" — it is refused by
// name, and the plan says so.
func TestEdgeLinks_PresentIsRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"links": []any{}}}}
	for name, err := range map[string]error{"preflight": preflightEdgeLinks(m), "apply": applyEdgeLinks(m)} {
		if err == nil || err.Error() != "edge.links is not supported by this CLI yet" {
			t.Errorf("%s: err = %v, want the not-supported refusal", name, err)
		}
	}
	var out bytes.Buffer
	planEdgeLinks(m, &out)
	if !strings.Contains(out.String(), "edge.links is not supported by this CLI yet") {
		t.Errorf("the plan did not say the key is unsupported: %q", out.String())
	}
}

// The refusal comes before anything reaches the slice.
func TestApply_RefusesEdgeLinksBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  links: [billing]\n",
		"edge.links is not supported by this CLI yet")
}
