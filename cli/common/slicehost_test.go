package common

import "testing"

// A slice's address is its account's name, a hyphen, the slice's name and the
// API's own host without its `api.` label. The cases are the shapes the API's
// address takes: the production gateway, a self-hosted one on a port, a local
// one on plain http, and a host that does not begin with `api.` at all.
func TestSliceHostAndURL_AreNamedAfterTheAccountOnTheAPIsOwnDomain(t *testing.T) {
	for _, tc := range []struct {
		api, host, url string
	}{
		{"https://api.ondrift.eu", "alice-shop.ondrift.eu", "https://alice-shop.ondrift.eu"},
		{"https://api.example.com:8443", "alice-shop.example.com:8443", "https://alice-shop.example.com:8443"},
		{"http://api.localhost:30036", "alice-shop.localhost:30036", "http://alice-shop.localhost:30036"},
		{"https://gateway.example.com", "alice-shop.gateway.example.com", "https://alice-shop.gateway.example.com"},
	} {
		prev := APIBaseURL
		APIBaseURL = tc.api
		host, url := SliceHost("alice", "shop"), SliceURL("alice", "shop")
		APIBaseURL = prev
		if host != tc.host {
			t.Errorf("API %s: SliceHost = %q, want %q", tc.api, host, tc.host)
		}
		if url != tc.url {
			t.Errorf("API %s: SliceURL = %q, want %q", tc.api, url, tc.url)
		}
	}
}

// The same slice name under two accounts is two addresses: the account is the
// part that changes when a slice changes hands.
func TestSliceHost_ChangesWithTheAccount(t *testing.T) {
	prev := APIBaseURL
	APIBaseURL = "https://api.ondrift.eu"
	t.Cleanup(func() { APIBaseURL = prev })

	if a, b := SliceHost("alice", "shop"), SliceHost("bob", "shop"); a == b {
		t.Errorf("alice and bob share the address %q", a)
	}
	if got := SliceHost("bob", "default"); got != "bob-default.ondrift.eu" {
		t.Errorf("the slice named default is at %q, want bob-default.ondrift.eu", got)
	}
}
