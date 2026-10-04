package project

import (
	"bytes"
	"strings"
	"testing"
)

const wantRateLimitNotice = "edge.rate_limit is not applied by this CLI yet; " +
	"the slice keeps its current rate limit (drift slice resize sets it)."

// Absent, nothing happens and nothing is said.
func TestEdgeRateLimit_AbsentSaysNothing(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{"mode": "open"}}}}
	if err := preflightEdgeRateLimit(m); err != nil {
		t.Errorf("an absent edge.rate_limit failed the preflight: %v", err)
	}
	if err := applyEdgeRateLimit(m); err != nil {
		t.Errorf("an absent edge.rate_limit failed the apply: %v", err)
	}
	var out bytes.Buffer
	reportEdgeRateLimit(m, &out)
	planEdgeRateLimit(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.rate_limit printed something: %q", out.String())
	}
}

// Present, it does NOT fail: it is where the retired `atomic.rate_limit` lands,
// which was ignored, and an old file must keep applying. It says once that the
// value is not applied and where the rate limit is set.
func TestEdgeRateLimit_PresentIsNamedAndNotRefused(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"rate_limit": "600/min"}}}
	if err := preflightEdgeRateLimit(m); err != nil {
		t.Errorf("a declared rate limit failed the preflight, so an old file stops applying: %v", err)
	}
	if err := applyEdgeRateLimit(m); err != nil {
		t.Errorf("a declared rate limit failed the apply, so an old file stops applying: %v", err)
	}

	var applied bytes.Buffer
	reportEdgeRateLimit(m, &applied)
	if n := strings.Count(applied.String(), wantRateLimitNotice); n != 1 {
		t.Errorf("the apply notice appeared %d times, want 1: %q", n, applied.String())
	}

	var planned bytes.Buffer
	planEdgeRateLimit(m, &planned)
	if !strings.Contains(planned.String(), "! "+wantRateLimitNotice) {
		t.Errorf("the plan did not carry the notice as a ! line: %q", planned.String())
	}
}
