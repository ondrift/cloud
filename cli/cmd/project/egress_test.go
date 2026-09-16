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
