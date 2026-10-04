package drift

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinkEnvName(t *testing.T) {
	cases := map[string]string{
		"c12":           "C12",
		"myapp-staging": "MYAPP_STAGING",
		"a1b2":          "A1B2",
	}
	for in, want := range cases {
		if got := linkEnvName(in); got != want {
			t.Errorf("linkEnvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCallerSlice(t *testing.T) {
	if got := CallerSlice(Request{Headers: map[string]string{"X-Drift-Slice": "app"}}); got != "app" {
		t.Errorf("canonical header: got %q", got)
	}
	// Case-insensitive (the runtime may lowercase header keys).
	if got := CallerSlice(Request{Headers: map[string]string{"x-drift-slice": "app"}}); got != "app" {
		t.Errorf("lowercase header: got %q", got)
	}
	if got := CallerSlice(Request{Headers: map[string]string{"other": "x"}}); got != "" {
		t.Errorf("absent header: want empty, got %q", got)
	}
}

func TestSliceResolveURL(t *testing.T) {
	// Not linked → error before any network I/O.
	if _, err := (SliceClient{name: "c12"}).resolveURL("/api/events"); err == nil {
		t.Error("resolveURL with no DRIFT_LINK_C12_URL: want error, got nil")
	}
	t.Setenv("DRIFT_LINK_C12_URL", "http://canvas.drift-slice-alice-c12.svc.cluster.local:8000")
	got, err := (SliceClient{name: "c12"}).resolveURL("/api/events")
	if err != nil {
		t.Fatalf("resolveURL: unexpected error %v", err)
	}
	if want := "http://canvas.drift-slice-alice-c12.svc.cluster.local:8000/api/events"; got != want {
		t.Errorf("resolveURL = %q, want %q", got, want)
	}
}

// Edge.Slice is the spelling a link is declared under, and Slice is its alias:
// both must read the same DRIFT_LINK_<NAME>_URL and build the same URL.
func TestEdgeSliceResolvesTheSameLinkAsSlice(t *testing.T) {
	t.Setenv("DRIFT_LINK_BILLING_API_URL", "http://canvas.drift-slice-alice-billing-api.svc.cluster.local:8000/")
	want := "http://canvas.drift-slice-alice-billing-api.svc.cluster.local:8000/api/charge"

	viaEdge, err := Edge.Slice("billing-api").resolveURL("/api/charge")
	if err != nil {
		t.Fatalf("Edge.Slice resolveURL: %v", err)
	}
	viaAlias, err := Slice("billing-api").resolveURL("/api/charge")
	if err != nil {
		t.Fatalf("Slice resolveURL: %v", err)
	}
	if viaEdge != want || viaAlias != want {
		t.Errorf("Edge.Slice = %q, Slice = %q; want both %q", viaEdge, viaAlias, want)
	}
}

// The not-linked error names the declaration that creates a link, and both
// spellings say the same thing.
func TestNotLinkedNamesEdgeLinks(t *testing.T) {
	want := `drift: slice "billing" is not linked: declare it under edge.links in the Driftfile (or run drift slice link add billing)`

	_, edgeErr := Edge.Slice("billing").Get("/x")
	_, aliasErr := Slice("billing").Get("/x")
	if edgeErr == nil || aliasErr == nil {
		t.Fatalf("an unlinked slice must be an error: Edge.Slice=%v, Slice=%v", edgeErr, aliasErr)
	}
	if edgeErr.Error() != want {
		t.Errorf("Edge.Slice error = %q, want %q", edgeErr.Error(), want)
	}
	if aliasErr.Error() != want {
		t.Errorf("Slice error = %q, want %q", aliasErr.Error(), want)
	}
}

// The egress refusal names the key a host is allowed under.
func TestEgressDeniedNamesEdgeEgressHosts(t *testing.T) {
	err := &EgressDeniedError{Host: "api.stripe.com", Err: errors.New("connection refused")}
	if !strings.Contains(err.Error(), "edge.egress.hosts") {
		t.Errorf("EgressDeniedError = %q, want it to name edge.egress.hosts", err.Error())
	}
}

// captureHeaders stands up a peer slice that records what reached it, and links
// this "slice" to it. Returns the recorded headers after fn has run.
func captureHeaders(t *testing.T, self string, callerHeaders map[string]string) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("DRIFT_LINK_PEER_URL", srv.URL)
	t.Setenv("DRIFT_SLICE", self)

	if _, err := (Slice("peer")).Request("POST", "/api/x", callerHeaders, []byte(`{}`)); err != nil {
		t.Fatalf("Request: %v", err)
	}
	return got
}

// A caller must not be able to relabel itself by passing the identity header.
// CallerSlice is not an authorisation input either way — see its doc — but
// handing callers a documented way to impersonate a peer was strictly worse.
func TestSliceRequestIdentityCannotBeOverwritten(t *testing.T) {
	got := captureHeaders(t, "billing", map[string]string{
		"X-Drift-Slice": "operator",
		"X-Other":       "kept",
	})
	if v := got.Get("X-Drift-Slice"); v != "billing" {
		t.Errorf("X-Drift-Slice = %q, want %q — a caller relabelled itself", v, "billing")
	}
	if v := got.Get("X-Other"); v != "kept" {
		t.Errorf("unrelated caller headers must survive: X-Other = %q", v)
	}
}

// A caller's header in another letter case is the same header on the wire, so
// it must not ride along beside the identity and relabel the call.
func TestSliceRequestIdentityCannotBeOverwrittenInAnyCase(t *testing.T) {
	got := captureHeaders(t, "billing", map[string]string{"x-drift-slice": "operator"})
	if v := got.Values("X-Drift-Slice"); len(v) != 1 || v[0] != "billing" {
		t.Errorf("X-Drift-Slice = %q, want exactly [billing]", v)
	}
}

// With no identity from the runtime — the normal case, since DRIFT_SLICE is
// deliberately withheld from functions — the header is ABSENT rather than
// empty. An empty header asserts an identity of "", which reads as a real
// answer to anything doing a presence check.
func TestSliceRequestOmitsIdentityWhenThereIsNone(t *testing.T) {
	got := captureHeaders(t, "", nil)
	if _, present := got["X-Drift-Slice"]; present {
		t.Errorf("X-Drift-Slice was sent as %q; want the header absent", got.Get("X-Drift-Slice"))
	}
}
