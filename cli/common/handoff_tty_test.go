package common

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Aliased: this package declares a const `atomic` (the brand orange in
	// style.go), which shadows the stdlib name.
	syncatomic "sync/atomic"
)

// A shell with no browser is refused before anything is minted.
//
// Two assertions, and the second is the one that matters. Returning an error is
// not enough on its own: the old path also ended in an error, just minutes later
// and after taking a single-use session with it. A session nobody can redeem
// still occupies the slice's handoff slot until it expires, so a headless run
// could make the next run — from a real terminal — meet a conflict it did not
// cause.
func TestBrowserHandoff_RefusesWithNoTerminalAndMintsNothing(t *testing.T) {
	var hits syncatomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	previous := ConfiguratorBaseURL
	ConfiguratorBaseURL = srv.URL
	t.Cleanup(func() { ConfiguratorBaseURL = previous })

	// A real session, in a scratch HOME. Without one the mint fails at auth
	// before it leaves the machine, so "zero configurator requests" would hold
	// with the guard removed and assert nothing at all.
	t.Setenv("HOME", t.TempDir())
	if err := SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatal(err)
	}

	withNonTerminalStdin(t)

	start := time.Now()
	_, err := RunBrowserHandoff("resize slice", "demo", ModeResize, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a handoff with no terminal attached must be refused — it prints a URL " +
			"nobody can open and then waits for a form nobody is filling in")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the refusal minted something anyway (%d configurator requests) — a "+
			"session nobody can redeem still holds the slice's handoff slot", n)
	}
	// The card's bound. The point is that it fails at once rather than on a
	// poll deadline, so a generous ceiling still catches the regression.
	if elapsed > time.Second {
		t.Errorf("refusing took %s, want well under a second", elapsed)
	}

	// The message has to say what to DO. "No terminal" alone leaves someone in
	// CI with no next step.
	for _, want := range []string{"demo", "browser", srv.URL} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, got: %v", want, err)
		}
	}
}

// withNonTerminalStdin replaces os.Stdin with a regular file, which is what CI
// and `drift slice resize < /dev/null` both look like.
func withNonTerminalStdin(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = prev
		_ = f.Close()
	})
}
