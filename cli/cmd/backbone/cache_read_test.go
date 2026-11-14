package backbone

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ReadCacheValue says whether the slice holds a key and what it holds, and a key
// the slice does not hold is not an error.
func TestReadCacheValue(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("key")
		switch asked {
		case "greeting":
			_, _ = w.Write([]byte("hello\n"))
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"key not found"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	value, found, err := ReadCacheValue("greeting")
	if err != nil || !found || value != "hello\n" {
		t.Errorf("a key the slice holds reads %q, found=%v, error %v, want %q found", value, found, err, "hello\n")
	}
	if value, found, err = ReadCacheValue("gone"); err != nil || found || value != "" {
		t.Errorf("a key the slice does not hold reads %q, found=%v, error %v, want nothing found and no error", value, found, err)
	}
	if _, found, err = ReadCacheValue("broken"); err == nil || found {
		t.Errorf("a slice that answers 500 reads found=%v, error %v, want an error", found, err)
	}
}

// The key is a query value: a key with a separator in it reaches the slice whole.
func TestReadCacheValueSendsTheKeyWhole(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("key")
		_, _ = w.Write([]byte("v"))
	}))
	t.Cleanup(srv.Close)
	seedSession(t, srv)

	const key = "a&b=c d#e"
	if _, found, err := ReadCacheValue(key); err != nil || !found {
		t.Fatalf("read: found=%v, error %v", found, err)
	}
	if asked != key {
		t.Errorf("the slice was asked for the key %q, want %q", asked, key)
	}
}
