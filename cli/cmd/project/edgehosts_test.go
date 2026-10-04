package project

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// Absent, nothing happens: no route is called and the plan has no section.
func TestEdgeHosts_AbsentCallsNothing(t *testing.T) {
	_, rec := newEdgeStub(t)
	m := edgeManifest(map[string]any{"egress": map[string]any{"mode": "open"}})
	if err := preflightEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the preflight: %v", err)
	}
	if err := applyEdgeHosts(m); err != nil {
		t.Errorf("an absent edge.hosts failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeHosts(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.hosts printed a plan section: %q", out.String())
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
			`edge.hosts[0]: an entry needs both "host" and "slice", such as {host: admin.example.com, slice: admin}`},
		{[]any{map[string]any{"host": "a.example.com", "slice": "a"}, map[string]any{"slice": "b"}},
			`edge.hosts[1]: an entry needs both "host" and "slice", such as {host: admin.example.com, slice: admin}`},
		{[]any{"admin.example.com"},
			`edge.hosts[0]: an entry needs both "host" and "slice", such as {host: admin.example.com, slice: admin}`},
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
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  hosts:\n    - host: admin.example.com\n",
		`edge.hosts[0]: an entry needs both "host" and "slice", such as {host: admin.example.com, slice: admin}`)
}

// The declared hosts reach the route, the host lowercased.
func TestApply_PostsTheDeclaredHosts(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  hosts:\n    - host: Admin.Example.com\n      slice: admin\n")

	if err := runApply(t); err != nil {
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
	applyIn(t, "slice: demo\nedge:\n  hosts: []\n")

	if err := runApply(t); err != nil {
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
	applyIn(t, "slice: demo\nedge:\n  hosts:\n    - host: admin.example.com\n      slice: admin\n")

	err := runApply(t)
	if err == nil || !strings.Contains(err.Error(), "edge.hosts") || !strings.Contains(err.Error(), refusal) {
		t.Errorf("apply returned %v, want the server's refusal verbatim under edge.hosts", err)
	}
}

// The plan shows what the declared hosts add to and remove from the live ones.
func TestPlan_HostsShowsLiveAgainstDeclared(t *testing.T) {
	s, rec := newEdgeStub(t)
	s.live["hosts"] = `{"hosts":[{"host":"old.example.com","slice":"legacy"},{"host":"admin.example.com","slice":"admin"}]}`
	m := edgeManifest(map[string]any{"hosts": []any{
		map[string]any{"host": "admin.example.com", "slice": "admin"},
		map[string]any{"host": "docs.example.com", "slice": "docs"},
	}})

	var out bytes.Buffer
	planEdgeHosts(m, &out)
	for _, want := range []string{
		"fronted hosts (edge.hosts)",
		"+ docs.example.com → docs",
		"- old.example.com → legacy",
		"= admin.example.com → admin",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out.String())
		}
	}
	if rec.count("POST /ops/slice/edge/hosts") != 0 {
		t.Error("the plan posted the hosts")
	}
}
