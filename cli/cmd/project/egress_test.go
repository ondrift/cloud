package project

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The egress refresh POST, and the header it has to carry.
//
// `/ops/atomic/egress/refresh` gates on `Content-Type: application/json` before
// it looks at anything else, and `common.DoRequest` sets no content type at all
// — so this POSTed with none and came back `415 Content-Type must be
// application/json` on every deploy whose egress block differed from the live
// one.
//
// It was invisible: applyEgress prints the error as a hint and returns nil, so
// `drift file apply` reports `Done!` on the line underneath. Nothing failed,
// nothing retried, and the allowlist was simply never refreshed.

func TestRefreshEgress_DeclaresItsContentType(t *testing.T) {
	var contentType string
	hits := 0
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/egress/refresh" {
			hits++
			contentType = r.Header.Get("Content-Type")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := refreshEgress("allowlist", []string{"api.stripe.com"}); err != nil {
		t.Fatalf("refreshEgress: %v", err)
	}
	if hits != 1 {
		t.Fatalf("the refresh endpoint was called %d times, want 1", hits)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want \"application/json\" — the route answers 415 without it", contentType)
	}
}

// THE DECLARATION IS IN THE BODY. This is the whole of what was missing: the
// operator read `config.atomic.egress` in eight places and nothing ever wrote
// it, so every slice rendered the open policy whatever its Driftfile said. A
// POST that carries no hosts is a refresh, not a declaration.
func TestRefreshEgress_SendsTheDeclaration(t *testing.T) {
	var body map[string]any
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/egress/refresh" {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := refreshEgress("allowlist", []string{"api.stripe.com", "api.github.com"}); err != nil {
		t.Fatalf("refreshEgress: %v", err)
	}
	if body["mode"] != "allowlist" {
		t.Errorf("mode = %v, want allowlist — without it the operator re-resolves the old posture", body["mode"])
	}
	hosts, _ := body["hosts"].([]any)
	if len(hosts) != 2 || hosts[0] != "api.stripe.com" {
		t.Errorf("hosts = %v, want both declared entries", body["hosts"])
	}
}

// MODE IS SENT EVEN WHEN IT IS "open", and an empty host list is an empty
// ARRAY rather than a missing key.
//
// The operator tells an absent mode from "open" and treats only the absent one
// as "re-resolve what you hold". So a tenant REMOVING their allowlist sends
// `{"mode":"open","hosts":[]}`; omitting either would leave the old allowlist
// in force and the slice locked down after the manifest said it should not be.
func TestRefreshEgress_RemovingAnAllowlistSaysSoExplicitly(t *testing.T) {
	var raw string
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/egress/refresh" {
			b, _ := io.ReadAll(r.Body)
			raw = string(b)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := refreshEgress("open", nil); err != nil {
		t.Fatalf("refreshEgress: %v", err)
	}
	if !strings.Contains(raw, `"mode":"open"`) {
		t.Errorf("body %q does not state open mode; the operator would keep the previous allowlist", raw)
	}
	if !strings.Contains(raw, `"hosts":[]`) {
		t.Errorf("body %q sends no host array; an absent one is not a cleared one", raw)
	}
}

// The declaration is read from `edge.egress`, where the key walker lands the
// retired `atomic.egress` too. A reader of the old spelling would see an open
// slice for every file written either way.
func TestDesiredEgress_ReadsEdgeEgress(t *testing.T) {
	m := &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{
		"mode": "Allowlist", "hosts": []any{"api.stripe.com"},
	}}}}
	mode, hosts, declared := desiredEgress(m)
	if !declared || mode != "allowlist" || len(hosts) != 1 || hosts[0] != "api.stripe.com" {
		t.Errorf("desiredEgress = (%q, %v, %v), want (allowlist, [api.stripe.com], true)", mode, hosts, declared)
	}

	if _, _, declared := desiredEgress(&Manifest{slice: Node{}}); declared {
		t.Error("no edge.egress was read as a declaration; an absent key leaves the slice's egress as it is")
	}

	m = &Manifest{slice: Node{"edge": map[string]any{"egress": map[string]any{}}}}
	if mode, hosts, declared := desiredEgress(m); !declared || mode != "open" || len(hosts) != 0 {
		t.Errorf("edge.egress with no mode = (%q, %v, %v), want (open, [], true)", mode, hosts, declared)
	}
}

// allowlistedSliceStub is a slice that exists, is ready, and holds an egress
// allowlist, so an apply that moves it back to open is visible as a refresh
// carrying `"mode":"open"`. It returns the recorder and the refresh bodies.
func allowlistedSliceStub(t *testing.T) (*recorder, *[]string) {
	t.Helper()
	var refreshes []string
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/ops/atomic/egress":
			_, _ = w.Write([]byte(`{"mode":"allowlist","declared_hosts":["api.stripe.com"]}`))
		case "/ops/atomic/egress/refresh":
			b, _ := io.ReadAll(r.Body)
			refreshes = append(refreshes, string(b))
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })
	return rec, &refreshes
}

// An absent `edge.egress` leaves the slice's egress as it is: the apply neither
// reads nor refreshes it. A file that does not mention egress must not move an
// allowlisted slice back to open.
func TestApplyEgress_AbsentLeavesTheLiveSettingAsItIs(t *testing.T) {
	for _, m := range []*Manifest{
		{slice: Node{"slice": "demo"}},
		edgeManifest(map[string]any{"rate_limit": "600/min"}),
	} {
		rec, refreshes := allowlistedSliceStub(t)
		if err := applyEgress(m); err != nil {
			t.Fatalf("applyEgress: %v", err)
		}
		if len(*refreshes) != 0 {
			t.Errorf("an absent edge.egress refreshed the slice's egress with %q", *refreshes)
		}
		if n := rec.count("GET /ops/atomic/egress"); n != 0 {
			t.Errorf("an absent edge.egress read the live egress %d time(s)", n)
		}
	}
}

// The same, through the real command: a Driftfile with no egress key, applied
// to an allowlisted slice, calls no egress route.
func TestApply_AbsentEgressCallsNoEgressRoute(t *testing.T) {
	rec, refreshes := allowlistedSliceStub(t)
	applyIn(t, "slice: demo\nedge:\n  rate_limit: 600/min\n")

	if err := runApply(t); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.count("GET /ops/slice/get") == 0 {
		t.Fatal("the apply never reached the slice, so this proves nothing")
	}
	if len(*refreshes) != 0 {
		t.Errorf("an absent edge.egress refreshed the slice's egress with %q", *refreshes)
	}
	for _, hit := range rec.hits {
		if strings.Contains(hit, "/ops/atomic/egress") {
			t.Errorf("an absent edge.egress called %s", hit)
		}
	}
}

// A present `mode: open` is a declaration, and moves an allowlisted slice back
// to open, under either spelling.
func TestApply_ADeclaredOpenEgressStillSetsOpen(t *testing.T) {
	for _, body := range []string{
		"slice: demo\nedge:\n  egress:\n    mode: open\n",
		"slice: demo\natomic:\n  egress:\n    mode: open\n",
		"slice: demo\nedge:\n  egress: {}\n",
	} {
		_, refreshes := allowlistedSliceStub(t)
		applyIn(t, body)

		if err := runApply(t); err != nil {
			t.Fatalf("apply of %q: %v", body, err)
		}
		if len(*refreshes) != 1 || !strings.Contains((*refreshes)[0], `"mode":"open"`) {
			t.Errorf("apply of %q refreshed with %q, want one refresh to open", body, *refreshes)
		}
	}
}

// A non-2xx must reach the caller. applyEgress downgrades it to a hint either
// way, but a refresh that silently reports success would leave the CLI printing
// "egress allowlist declared" for a call the platform refused.
func TestRefreshEgress_ReportsARefusal(t *testing.T) {
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/egress/refresh" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"error":"Content-Type must be application/json"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := refreshEgress("allowlist", []string{"api.stripe.com"}); err == nil {
		t.Error("a 415 was reported as success, so a refused refresh reads as an applied one")
	}
}
