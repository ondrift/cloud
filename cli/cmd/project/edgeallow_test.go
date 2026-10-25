package project

import (
	"net/http"
	"strings"
	"testing"
)

// Absent, nothing happens: no route is called.
func TestEdgeAllow_AbsentCallsNothing(t *testing.T) {
	_, rec := newEdgeStub(t)
	m := edgeManifest(map[string]any{"egress": map[string]any{"mode": "open"}})
	if err := preflightEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the preflight: %v", err)
	}
	if err := applyEdgeAllow(m); err != nil {
		t.Errorf("an absent edge.allow failed the apply: %v", err)
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
	assertEdgeApplyRefuses(t,
		edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  allow = [\"203.0.113.5\"]\n}\n",
		"edge.allow", "203.0.113.5")
}

// The declared list reaches the route as written.
func TestApply_PostsTheDeclaredAllowlist(t *testing.T) {
	s, _ := newEdgeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  allow = [\"203.0.113.0/24\",\"2001:db8::/32\"]\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
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
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  allow = []\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
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
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  allow = [\"203.0.113.0/24\"]\n}\n")

	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), "edge.allow") ||
		!strings.Contains(err.Error(), "cidrs: 257 entries, at most 256") {
		t.Errorf("apply returned %v, want the server's refusal under edge.allow", err)
	}
}
