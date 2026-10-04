package project

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Absent, nothing happens: no route is called and the plan has no section.
func TestEdgeAllow_AbsentCallsNothing(t *testing.T) {
	_, rec := newEdgeStub(t)
	m := edgeManifest(map[string]any{"egress": map[string]any{"mode": "open"}})
	if err := preflightEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the preflight: %v", err)
	}
	if err := applyEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeAllow(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.allow printed a plan section: %q", out.String())
	}
	if len(rec.hits) != 0 {
		t.Errorf("an absent edge.allow called %v", rec.hits)
	}
}

// Every entry is a CIDR, or the file is refused naming the entry.
func TestEdgeAllow_PreflightRefusesWhatIsNotACIDR(t *testing.T) {
	for _, c := range []struct {
		allow any
		want  string
	}{
		{[]any{"203.0.113.0/24", "203.0.113.5"},
			`edge.allow[1]: "203.0.113.5" is an address, not a range; write "203.0.113.5/32" to allow that one address`},
		{[]any{"2001:db8::1"},
			`edge.allow[0]: "2001:db8::1" is an address, not a range; write "2001:db8::1/128" to allow that one address`},
		{[]any{"300.0.0.0/8"},
			`edge.allow[0]: "300.0.0.0/8" is not a CIDR, such as 203.0.113.0/24 or 2001:db8::/32`},
		{[]any{"office"},
			`edge.allow[0]: "office" is not a CIDR, such as 203.0.113.0/24 or 2001:db8::/32`},
		{[]any{24},
			`edge.allow[0]: 24 is not a CIDR, such as 203.0.113.0/24 or 2001:db8::/32`},
		{"203.0.113.0/24",
			`edge.allow must be a list of CIDRs, such as ["203.0.113.0/24"]`},
	} {
		m := edgeManifest(map[string]any{"allow": c.allow})
		err := preflightEdgeAllow(m)
		if err == nil || err.Error() != c.want {
			t.Errorf("preflight of %v = %v, want %q", c.allow, err, c.want)
		}
	}

	m := edgeManifest(map[string]any{"allow": []any{"203.0.113.0/24", "2001:db8::/32"}})
	if err := preflightEdgeAllow(m); err != nil {
		t.Errorf("a valid allowlist was refused: %v", err)
	}
}

// A bare address is refused before anything reaches the slice.
func TestApply_RefusesABareAllowAddressBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  allow: [\"203.0.113.5\"]\n",
		`edge.allow[0]: "203.0.113.5" is an address, not a range; write "203.0.113.5/32" to allow that one address`)
}

// The declared list reaches the route as written.
func TestApply_PostsTheDeclaredAllowlist(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  allow: [\"203.0.113.0/24\", \"2001:db8::/32\"]\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := `{"cidrs":["203.0.113.0/24","2001:db8::/32"]}`
	if got := s.bodies("allow"); len(got) != 1 || got[0] != want {
		t.Errorf("posted %q, want one %s", got, want)
	}
}

// An empty list is a declaration too: it clears the allowlist, and is sent as
// an empty list rather than as null.
func TestApply_AnEmptyAllowlistClearsIt(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  allow: []\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.bodies("allow"); len(got) != 1 || got[0] != `{"cidrs":[]}` {
		t.Errorf("posted %q, want one {\"cidrs\":[]}", got)
	}
}

// A refusal from the server fails the apply and says what it refused.
func TestApply_SurfacesTheAllowRefusal(t *testing.T) {
	s, _ := newEdgeStub(t)
	s.refuse["allow"] = edgeReply{http.StatusBadRequest, `{"error":"cidrs: 257 entries, at most 256"}`}
	applyIn(t, "slice: demo\nedge:\n  allow: [\"203.0.113.0/24\"]\n")

	err := runApply(t)
	if err == nil || !strings.Contains(err.Error(), "edge.allow") ||
		!strings.Contains(err.Error(), "cidrs: 257 entries, at most 256") {
		t.Errorf("apply returned %v, want the server's refusal under edge.allow", err)
	}
}

// The plan shows what the declared list adds to and removes from the live one.
func TestPlan_AllowShowsLiveAgainstDeclared(t *testing.T) {
	s, rec := newEdgeStub(t)
	s.live["allow"] = `{"cidrs":["198.51.100.0/24","203.0.113.0/24"]}`
	m := edgeManifest(map[string]any{"allow": []any{"203.0.113.0/24", "2001:db8::/32"}})

	var out bytes.Buffer
	planEdgeAllow(m, &out)
	for _, want := range []string{
		"inbound allowlist (edge.allow)",
		"+ 2001:db8::/32",
		"- 198.51.100.0/24",
		"= 203.0.113.0/24",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out.String())
		}
	}
	if rec.count("POST /ops/slice/edge/allow") != 0 {
		t.Error("the plan posted the allowlist")
	}

	s.live["allow"] = `{"cidrs":["203.0.113.0/24"]}`
	out.Reset()
	planEdgeAllow(edgeManifest(map[string]any{"allow": []any{}}), &out)
	if !strings.Contains(out.String(), "every address reaches the slice") {
		t.Errorf("the plan for an empty list does not say it opens the slice:\n%s", out.String())
	}
}
