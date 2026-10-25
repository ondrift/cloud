package project

import (
	"bytes"
	"net/http"
	"os"
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
}

// A slice cannot link to itself, and saying so before anything deploys beats
// the platform's 400 after the rest of the file is live.
func TestEdgeLinks_RefusesTheSliceItself(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  links = [\"billing\", \"demo\"]\n}\n",
		`edge.links names "demo", the slice this file is applied to; a slice cannot link to itself`)
}

// THE WHOLE TRUTH. Declared and missing is added, live and undeclared is
// removed, and a link on both sides is left alone: re-adding it would redeploy
// both slices for nothing.
func TestApply_LinksAddTheMissingAndRemoveTheUndeclared(t *testing.T) {
	var posted []string
	rec := stubSliceWithEdge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/slice/link" {
			return
		}
		if got := r.Header.Get("X-Slice"); got != "demo" {
			t.Errorf("%s /ops/slice/link acted on slice %q, want demo", r.Method, got)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[{"target":"docs"},{"target":"api"}]`))
		case http.MethodPost:
			var b bytes.Buffer
			_, _ = b.ReadFrom(r.Body)
			posted = append(posted, b.String())
			_, _ = w.Write([]byte(`{"target":"billing"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"removed":"` + r.URL.Query().Get("target") + `"}`))
		}
	})
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  links = [\"billing\", \"api\"]\n}\n", "site")

	if err := runApplyRemoving(t, "link docs"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(posted) != 1 || posted[0] != `{"target":"billing"}` {
		t.Errorf("link adds = %q, want exactly one for billing", posted)
	}
	if n := rec.count("DELETE /ops/slice/link"); n != 1 {
		t.Errorf("%d link removals, want exactly one (docs)", n)
	}
	if !rec.has("DELETE /ops/slice/link?target=docs") {
		t.Errorf("docs was not the link removed: %v", rec.queries)
	}
}

// An empty list is a declaration too: no links at all.
func TestApply_AnEmptyLinksListRemovesEveryLink(t *testing.T) {
	rec := stubSliceWithEdge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/slice/link" {
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"target":"docs"},{"target":"api"}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  links = []\n}\n", "site")

	if err := runApplyRemoving(t, "link docs", "link api"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := rec.count("DELETE /ops/slice/link"); n != 2 {
		t.Errorf("%d link removals, want 2", n)
	}
	if n := rec.count("POST /ops/slice/link"); n != 0 {
		t.Errorf("%d link adds, want none", n)
	}
}

// Absent, the live links are left alone: not read, not touched.
func TestApply_AbsentLinksCallNoLinkRoute(t *testing.T) {
	rec := stubSliceWithEdge(t, func(w http.ResponseWriter, r *http.Request) {})
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\n", "site")

	if err := applyAgreeingAtZero(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if n := rec.count(m + " /ops/slice/link"); n != 0 {
			t.Errorf("a file with no edge.links issued %d × %s /ops/slice/link", n, m)
		}
	}
}

// A target the platform refuses (not a slice of this owner) fails the apply
// with the platform's own reason.
func TestApply_LinksSurfaceTheRefusal(t *testing.T) {
	stubSliceWithEdge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/slice/link" {
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no slice named 'ghost' in your account (links are same-user only)"}`))
	})
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  links = [\"ghost\"]\n}\n", "site")

	err := applyAgreeingAtZero(t)
	if err == nil {
		t.Fatal("a refused link was reported as applied")
	}
	for _, want := range []string{"edge.links", "no slice named 'ghost' in your account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %q", err, want)
		}
	}
}

// The plan shows both directions, and changes nothing.
func TestPlan_LinksShowAddsAndRemoves(t *testing.T) {
	rec := stubSliceWithEdge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/link" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"target":"docs"},{"target":"api"}]`))
		}
	})
	withRealSchema(t)
	applyIn(t, edgeFreeDemo+"canvas {\n  site \"/\" { dir = \"site\" }\n}\nedge {\n  links = [\"billing\", \"api\"]\n}\n", "site")

	out, err := edgeStdout(t, func() error { return runPlanCmd(t) })
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, want := range []string{"+ link billing", "- link docs", "  link api"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not show %q:\n%s", want, out)
		}
	}
	for _, m := range []string{"POST", "DELETE"} {
		if n := rec.count(m + " /ops/slice/link"); n != 0 {
			t.Errorf("A PLAN ISSUED %d × %s /ops/slice/link", n, m)
		}
	}
}

// runApplyRemoving drives `drift apply` with the price agreed at 0 and each
// named removal agreed to, because a link the file does not name is a removal
// the apply asks about.
func runApplyRemoving(t *testing.T, removals ...string) error {
	t.Helper()
	a := noAnswers()
	a.price = 0
	for _, r := range removals {
		a.removes[r] = true
	}
	return runApply(applyOptions{noEnvFile: true, answers: a, out: os.Stdout, in: strings.NewReader("")})
}

// stubSliceWithEdge is a slice that exists and is ready, answering every route
// an apply walks with an empty success of the shape that route returns. The
// site-auth gate and the links are the test's: `edge` sees those requests
// first, and a request it does not answer gets an empty success.
func stubSliceWithEdge(t *testing.T, edge http.HandlerFunc) *edgeRecorder {
	t.Helper()
	er := &edgeRecorder{}
	er.recorder = stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		er.noteQuery(r)
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
			return
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
			return
		case "/ops/slice/auth", "/ops/slice/link":
			sw := &statusWriter{ResponseWriter: w}
			edge(sw, r)
			if !sw.wrote {
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	return er
}

// edgeRecorder adds the query string to what a recorder notes, because which
// link a DELETE removes travels there.
type edgeRecorder struct {
	*recorder
	queries []string
}

func (e *edgeRecorder) noteQuery(r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queries = append(e.queries, r.Method+" "+r.URL.RequestURI())
}

func (e *edgeRecorder) has(want string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, q := range e.queries {
		if q == want {
			return true
		}
	}
	return false
}

// statusWriter notes whether a handler answered at all.
type statusWriter struct {
	http.ResponseWriter
	wrote bool
}

func (s *statusWriter) WriteHeader(code int) {
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// edgeStdout runs fn with stdout captured, because the plan and the apply
// print there.
func edgeStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()
	ferr := fn()
	os.Stdout = prev
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, ferr
}
