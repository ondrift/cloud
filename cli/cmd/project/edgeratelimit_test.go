package project

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
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

// edgeFreeDemo opens a Driftfile for the slice the edge stubs hold: the free
// one, which a file reaches by declaring a shape inside the free grant.
const edgeFreeDemo = "slice \"demo\" {}\n"

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
	return &Manifest{slice: Node{"slice": "demo", "edge": edge}}
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

// resizeStub is a free slice that exists and is ready, recording the body of
// every resize and every call to an `/ops/slice/edge/` route. Every other
// `/ops/` route answers `{}`.
type resizeStub struct {
	mu      sync.Mutex
	resizes []map[string]any
	edge    []string
}

func newResizeStub(t *testing.T) *resizeStub {
	t.Helper()
	s := &resizeStub{}
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
		case r.URL.Path == "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case r.URL.Path == "/ops/slice/resize":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.resizes = append(s.resizes, body)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/ops/slice/edge/"):
			s.mu.Lock()
			s.edge = append(s.edge, r.Method+" "+r.URL.Path)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/ops/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })
	return s
}

// rpm is the requests per minute the one resize carried.
func (s *resizeStub) rpm(t *testing.T) float64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resizes) != 1 {
		t.Fatalf("%d resizes, want exactly one", len(s.resizes))
	}
	cfg, _ := s.resizes[0]["config"].(map[string]any)
	atomic, _ := cfg["atomic"].(map[string]any)
	rpm, ok := atomic["MaxNumberOfRequestsPerMinute"].(float64)
	if !ok {
		t.Fatalf("the resize carried no requests-per-minute limit: %v", s.resizes[0])
	}
	return rpm
}

// The declared rate reaches the slice as requests per minute, in the shape the
// resize carries, and no edge route is called for it.
func TestApply_PostsTheDeclaredRateLimitAsRequestsPerMinute(t *testing.T) {
	s := newResizeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  rate_limit = \"1/s\"\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.rpm(t); got != 60 {
		t.Errorf("the resize carried %v requests per minute, want 60", got)
	}
	if len(s.edge) != 0 {
		t.Errorf("a rate limit called %v", s.edge)
	}
}

// A per-hour rate is converted to requests per minute.
func TestApply_PostsAnHourlyRateAsRequestsPerMinute(t *testing.T) {
	s := newResizeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  rate_limit = \"120/h\"\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.rpm(t); got != 2 {
		t.Errorf("the resize carried %v requests per minute, want 2", got)
	}
}

// edge.rate_limit and limits.requests_per_minute are one limit, and a file that
// writes both is refused before any route is called.
func TestApply_RefusesTheRateLimitWrittenTwiceBeforeAnyRoute(t *testing.T) {
	assertEdgeApplyRefuses(t,
		"slice \"demo\" {\n  limits {\n    requests_per_minute = 30\n  }\n}\n"+
			"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  rate_limit = \"30/min\"\n}\n",
		"edge.rate_limit and limits.requests_per_minute are one limit; write it in one place")
}

// assertEdgeApplyRefuses applies a Driftfile against a stub that fails the test
// on any `/ops/` request, and asserts the apply is refused with an error that
// says every one of wants.
func assertEdgeApplyRefuses(t *testing.T, body string, wants ...string) {
	t.Helper()
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ops/") {
			t.Errorf("apply called %s %s before refusing the file", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	withRealSchema(t)
	applyIn(t, body, "site")

	err := runApply(applyOptions{noEnvFile: true, answers: noAnswers(), out: os.Stdout, in: strings.NewReader("")})
	if err == nil {
		t.Fatal("a file that should be refused was applied")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
	for _, hit := range rec.hits {
		if strings.Contains(hit, " /ops/") {
			t.Errorf("a route was called before the refusal: %s", hit)
		}
	}
}

// A file that declares none of the three keys calls none of their routes: an
// absent key leaves that part of the slice as it is.
func TestApply_AbsentEdgeKeysCallNoEdgeRoute(t *testing.T) {
	_, rec := newEdgeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  egress {\n    mode = \"open\"\n  }\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
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

// The edge routes are applied in deploy.go's order: allow, hosts. The rate
// limit rides the resize that comes before them.
func TestApply_PostsAllowAndHostsInOrder(t *testing.T) {
	s, _ := newEdgeStub(t)
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  rate_limit = \"60/min\"\n  allow      = [\"203.0.113.0/24\"]\n"+
		"  hosts      = { \"admin.example.com\" = \"admin\" }\n}\n")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := strings.Join(s.order, ","); got != "allow,hosts" {
		t.Errorf("routes were posted in the order %q, want allow,hosts", got)
	}
	if n := len(s.bodies("rate-limit")); n != 0 {
		t.Errorf("the rate limit was posted to its edge route %d time(s)", n)
	}
}

// A rate below one request per minute cannot be held, and is refused before
// anything is deployed.
func TestApply_RefusesARateBelowOnePerMinuteBeforeAnyRoute(t *testing.T) {
	assertEdgeApplyRefuses(t,
		edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  rate_limit = \"30/h\"\n}\n",
		`"30/h" is below one request per minute, the smallest rate a slice holds; `+
			`the smallest you can write is "1/min" (or "60/h")`)
}

// A plan writes nothing to any edge route, whatever the file declares, and
// shows the declared rate against the live one.
func TestPlan_PostsNoEdgeRoute(t *testing.T) {
	s, rec := newEdgeStub(t)
	s.live["allow"] = `{"cidrs":[]}`
	s.live["hosts"] = `{"hosts":[]}`
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"edge {\n  rate_limit = \"60/min\"\n  allow      = [\"203.0.113.0/24\"]\n"+
		"  hosts      = { \"admin.example.com\" = \"admin\" }\n}\n")

	out, err := edgeStdout(t, func() error { return runPlanCmd(t) })
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, route := range []string{"allow", "hosts"} {
		if rec.count("GET /ops/slice/edge/"+route) != 1 {
			t.Errorf("the plan did not read the live %s", route)
		}
	}
	for _, route := range []string{"rate-limit", "allow", "hosts"} {
		if n := rec.count("POST /ops/slice/edge/" + route); n != 0 {
			t.Errorf("A PLAN POSTED %s %d time(s)", route, n)
		}
	}
	if !strings.Contains(out, "limits.requests_per_minute") || !strings.Contains(out, "0 → 60") {
		t.Errorf("the plan does not show the declared rate against the live one:\n%s", out)
	}
}
