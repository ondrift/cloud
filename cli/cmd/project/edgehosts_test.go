package project

import (
	"net/http"
	"strings"
	"testing"
)

// Absent, nothing happens: no route is called.
func TestEdgeHosts_AbsentCallsNothing(t *testing.T) {
	_, rec := newEdgeStub(t)
	m := edgeManifest(map[string]any{"egress": map[string]any{"mode": "open"}})
	if err := preflightEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the preflight: %v", err)
	}
	if err := applyEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the apply: %v", err)
	}
	if len(rec.hits) != 0 {
		t.Errorf("an absent edge.hosts called %v", rec.hits)
	}
}

// An entry that cannot be sent is refused naming the entry.
func TestEdgeHosts_PreflightRefusesAnIncompleteEntry(t *testing.T) {
	for _, c := range []struct {
		hosts any
		want  string
	}{
		{[]any{map[string]any{"host": "admin.example.com"}},
			`edge.hosts[0]: an entry needs both a host and a slice, such as "admin.example.com" = "admin"`},
		{[]any{map[string]any{"host": "a.example.com", "slice": "a"}, map[string]any{"slice": "b"}},
			`edge.hosts[1]: an entry needs both a host and a slice, such as "admin.example.com" = "admin"`},
		{[]any{"admin.example.com"},
			`edge.hosts[0]: an entry needs both a host and a slice, such as "admin.example.com" = "admin"`},
		{"admin.example.com",
			`edge.hosts must be a list of {host, slice} entries`},
	} {
		err := preflightEdgeHosts(edgeManifest(map[string]any{"hosts": c.hosts}))
		if err == nil || err.Error() != c.want {
			t.Errorf("preflight of %v = %v, want %q", c.hosts, err, c.want)
		}
	}
}

// An incomplete entry is refused before anything reaches the slice.
func TestApply_RefusesAnIncompleteHostBeforeAnyRoute(t *testing.T) {
	assertEdgeApplyRefuses(t,
		edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  hosts = { \"admin.example.com\" = \"\" }\n}\n",
		"edge.hosts", "slice")
}

// The declared hosts reach the route, the host lowercased.
func TestApply_PostsTheDeclaredHosts(t *testing.T) {
	s, _ := newEdgeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  hosts = { \"Admin.Example.com\" = \"admin\" }\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := `{"hosts":[{"host":"admin.example.com","slice":"admin"}]}`
	if got := s.bodies("hosts"); len(got) != 1 || got[0] != want {
		t.Errorf("posted %q, want one %s", got, want)
	}
}

// An empty list clears the fronted hosts, and is sent as an empty list.
func TestApply_AnEmptyHostsListClearsIt(t *testing.T) {
	s, _ := newEdgeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  hosts = {}\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.bodies("hosts"); len(got) != 1 || got[0] != `{"hosts":[]}` {
		t.Errorf("posted %q, want one {\"hosts\":[]}", got)
	}
}

// The server's refusal names the command that fixes it, so it reaches the
// reader as the server wrote it.
func TestApply_SurfacesTheHostsRefusalVerbatim(t *testing.T) {
	s, _ := newEdgeStub(t)
	refusal := "admin.example.com is not a verified custom domain of this slice: " +
		"run `drift slice domain add admin.example.com`, then `drift slice domain verify admin.example.com`"
	s.refuse["hosts"] = edgeReply{http.StatusUnprocessableEntity, `{"error":"` + refusal + `"}`}
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  hosts = { \"admin.example.com\" = \"admin\" }\n}\n")

	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), "edge.hosts") || !strings.Contains(err.Error(), refusal) {
		t.Errorf("apply returned %v, want the server's refusal verbatim under edge.hosts", err)
	}
}
