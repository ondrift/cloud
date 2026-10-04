package project

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// edgeStub is a slice that exists and is ready, and an api that records every
// call to the three `/ops/slice/edge/` routes this lane owns: the body each
// POST carried, the slice each request was addressed to, and the order the
// routes were reached in.
//
// A GET answers from `live`, and a POST answers from `refuse` when the route is
// named there; otherwise a POST answers 200 with what it was sent. Every other
// `/ops/` route answers `{}`, so the apply gets past the triad to the edge steps.
type edgeStub struct {
	mu     sync.Mutex
	posted map[string][]string
	order  []string
	slices []string
	live   map[string]string
	refuse map[string]edgeReply
}

type edgeReply struct {
	status int
	body   string
}

func newEdgeStub(t *testing.T) (*edgeStub, *recorder) {
	t.Helper()
	s := &edgeStub{posted: map[string][]string{}, live: map[string]string{}, refuse: map[string]edgeReply{}}
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
		case r.URL.Path == "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case strings.HasPrefix(r.URL.Path, "/ops/slice/edge/"):
			route := strings.TrimPrefix(r.URL.Path, "/ops/slice/edge/")
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.slices = append(s.slices, r.Header.Get("X-Slice"))
			if r.Method == http.MethodGet {
				live, ok := s.live[route]
				s.mu.Unlock()
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(live))
				return
			}
			s.posted[route] = append(s.posted[route], string(body))
			s.order = append(s.order, route)
			reply, refused := s.refuse[route]
			s.mu.Unlock()
			if refused {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(reply.status)
				_, _ = w.Write([]byte(reply.body))
				return
			}
			_, _ = w.Write(body)
		case strings.HasPrefix(r.URL.Path, "/ops/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })
	return s, rec
}

// bodies is what the apply posted to one edge route, in order.
func (s *edgeStub) bodies(route string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.posted[route]...)
}

// edgeManifest is a manifest for calling one key's plan section directly.
func edgeManifest(edge map[string]any) *Manifest {
	m := &Manifest{slice: Node{"slice": "demo", "edge": edge}}
	m.SetName("demo")
	return m
}

func TestRateLimitRPM(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{
		{"10/s", 600},
		{"600/min", 600},
		{"1/min", 1},
		{"60/h", 1},
		{"120/h", 2},
		{"7200/h", 120},
	} {
		got, err := rateLimitRPM(c.in)
		if err != nil || got != c.want {
			t.Errorf("rateLimitRPM(%v) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}

	for _, c := range []struct {
		in   any
		want []string
	}{
		{"30/h", []string{`"30/h" is below one request per minute`, `"1/min" (or "60/h")`}},
		{"0/min", []string{`"0/min" is below one request per minute`}},
		{"0/s", []string{`"0/s" is below one request per minute`}},
		{"90/h", []string{`"90/h" is 1.5 requests per minute`, `"60/h"`, `"120/h"`}},
		{"600", []string{`"600" is not a rate`, "N/s, N/min or N/h"}},
		{"10/day", []string{`"10/day" is not a rate`}},
		{600, []string{"600 is not a rate"}},
		{"99999999999999999999/s", []string{"is too large"}},
	} {
		_, err := rateLimitRPM(c.in)
		if err == nil {
			t.Errorf("rateLimitRPM(%v) was accepted", c.in)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("rateLimitRPM(%v) = %q, want it to say %q", c.in, err, w)
			}
		}
	}
}

// Absent, nothing happens and nothing is said: no route is called, by the
// preflight, the apply or the plan.
func TestEdgeRateLimit_AbsentCallsNothing(t *testing.T) {
	_, rec := newEdgeStub(t)
	m := edgeManifest(map[string]any{"egress": map[string]any{"mode": "open"}})
	if err := preflightEdgeRateLimit(m); err != nil {
		t.Errorf("an absent edge.rate_limit failed the preflight: %v", err)
	}
	if err := applyEdgeRateLimit(m); err != nil {
		t.Errorf("an absent edge.rate_limit failed the apply: %v", err)
	}
	var out bytes.Buffer
	planEdgeRateLimit(m, &out)
	if out.Len() != 0 {
		t.Errorf("an absent edge.rate_limit printed a plan section: %q", out.String())
	}
	if len(rec.hits) != 0 {
		t.Errorf("an absent edge.rate_limit called %v", rec.hits)
	}
}

// The declared rate reaches the route as requests per minute, addressed to the
// slice the file names, once.
func TestApply_PostsTheDeclaredRateLimitAsRequestsPerMinute(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  rate_limit: 10/s\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.bodies("rate-limit"); len(got) != 1 || got[0] != `{"rpm":600}` {
		t.Errorf("posted %q, want one {\"rpm\":600}", got)
	}
	for _, slice := range s.slices {
		if slice != "demo" {
			t.Errorf("an edge request was addressed to slice %q, want demo", slice)
		}
	}
}

// `atomic.rate_limit` was an ignored key. It is now the alias of
// `edge.rate_limit`, so an old file sets the slice's rate limit.
func TestApply_AtomicRateLimitReachesTheEdgeRoute(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\natomic:\n  rate_limit: 120/h\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.bodies("rate-limit"); len(got) != 1 || got[0] != `{"rpm":2}` {
		t.Errorf("posted %q, want one {\"rpm\":2}", got)
	}
}

// A file that declares none of the three keys calls none of their routes: an
// absent key leaves that part of the slice as it is.
func TestApply_AbsentEdgeKeysCallNoEdgeRoute(t *testing.T) {
	_, rec := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  egress:\n    mode: open\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.count("GET /ops/slice/get") == 0 {
		t.Fatal("the apply never reached the slice, so this proves nothing")
	}
	for _, hit := range rec.hits {
		if strings.Contains(hit, "/ops/slice/edge/") {
			t.Errorf("an absent edge key called %s", hit)
		}
	}
}

// The three keys are applied in deploy.go's order: rate limit, allow, hosts.
func TestApply_PostsRateLimitAllowAndHostsInOrder(t *testing.T) {
	s, _ := newEdgeStub(t)
	applyIn(t, "slice: demo\nedge:\n  rate_limit: 600/min\n  allow: [\"203.0.113.0/24\"]\n"+
		"  hosts:\n    - host: admin.example.com\n      slice: admin\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := strings.Join(s.order, ","); got != "rate-limit,allow,hosts" {
		t.Errorf("routes were posted in the order %q, want rate-limit,allow,hosts", got)
	}
}

// A rate below one request per minute cannot be held, and is refused before
// anything is deployed.
func TestApply_RefusesARateBelowOnePerMinuteBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice: demo\ncanvas: ./site\nedge:\n  rate_limit: 30/h\n",
		`edge.rate_limit: "30/h" is below one request per minute, the smallest rate a slice holds; `+
			`the smallest you can write is "1/min" (or "60/h")`)
}

// Above the tier's ceiling the server refuses, and the apply fails saying so
// with the ceiling the server published.
func TestApply_SurfacesTheRateLimitCeiling(t *testing.T) {
	s, _ := newEdgeStub(t)
	s.refuse["rate-limit"] = edgeReply{http.StatusUnprocessableEntity,
		`{"error":"600 requests per minute is above the free tier's ceiling","ceiling":60}`}
	applyIn(t, "slice: demo\nedge:\n  rate_limit: 600/min\n")

	err := runApply(t)
	if err == nil {
		t.Fatal("an apply the server refused reported success")
	}
	for _, want := range []string{
		"edge.rate_limit",
		"600 requests per minute is above the free tier's ceiling",
		"at most 60 requests per minute",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// The plan shows the live rate against the declared one, and posts nothing.
func TestPlan_RateLimitShowsLiveAgainstDeclared(t *testing.T) {
	s, rec := newEdgeStub(t)
	s.live["rate-limit"] = `{"rpm":300,"ceiling":null}`
	m := edgeManifest(map[string]any{"rate_limit": "10/s"})

	var out bytes.Buffer
	planEdgeRateLimit(m, &out)
	for _, want := range []string{
		"rate limit (edge.rate_limit)",
		"live      300 requests per minute",
		"declared  10/s = 600 requests per minute",
		"changes",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out.String())
		}
	}
	if rec.count("POST /ops/slice/edge/rate-limit") != 0 {
		t.Error("the plan posted the rate limit")
	}

	s.live["rate-limit"] = `{"rpm":600,"ceiling":60}`
	out.Reset()
	planEdgeRateLimit(m, &out)
	for _, want := range []string{"tier ceiling 60", "unchanged", "above the tier's ceiling of 60"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out.String())
		}
	}
}

// A plan writes nothing to any edge route, whatever the file declares.
func TestPlan_PostsNoEdgeRoute(t *testing.T) {
	s, rec := newEdgeStub(t)
	s.live["rate-limit"] = `{"rpm":300,"ceiling":null}`
	s.live["allow"] = `{"cidrs":[]}`
	s.live["hosts"] = `{"hosts":[]}`
	applyIn(t, "slice: demo\nedge:\n  rate_limit: 600/min\n  allow: [\"203.0.113.0/24\"]\n"+
		"  hosts:\n    - host: admin.example.com\n      slice: admin\n")

	if err := runPlanCmd(t); err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, route := range []string{"rate-limit", "allow", "hosts"} {
		if rec.count("GET /ops/slice/edge/"+route) != 1 {
			t.Errorf("the plan did not read the live %s", route)
		}
		if n := rec.count("POST /ops/slice/edge/" + route); n != 0 {
			t.Errorf("A PLAN POSTED %s %d time(s)", route, n)
		}
	}
}
