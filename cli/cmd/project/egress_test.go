package project

import (
	"net/http"
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

	if err := refreshEgress(); err != nil {
		t.Fatalf("refreshEgress: %v", err)
	}
	if hits != 1 {
		t.Fatalf("the refresh endpoint was called %d times, want 1", hits)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want \"application/json\" — the route answers 415 without it", contentType)
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

	if err := refreshEgress(); err == nil {
		t.Error("a 415 was reported as success, so a refused refresh reads as an applied one")
	}
}
