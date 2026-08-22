package project

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE assertion for "apply stops creating slices", and it is about what the CLI
// DID rather than what it returned.
//
// A returned error proves nothing on its own: apply could refuse AND still have
// posted a create on the way there, and the refusal would read like a
// well-behaved gate while a slice had already been bought. So the recorder is
// the subject — zero calls to any slice-mutating endpoint.
func TestApply_RefusesAMissingSliceAndCreatesNothing(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// The slice does not exist. Everything else would be a bug to reach.
		if r.URL.Path == "/ops/slice/get" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	applyIn(t, "slice: ghost\ncanvas:\n  sites:\n    - ./site\n", "site")

	err := runApply(t)
	if err == nil {
		t.Fatal("applying into a slice that does not exist must be refused — the " +
			"slice form owns what a slice is, and this file cannot create one")
	}
	// The slice by name, and the command that makes one. Asserted as the whole
	// command rather than as a brand word: what owns a slice's shape has been
	// called more than one thing, and what the reader needs is something to run.
	if !strings.Contains(strings.ToLower(err.Error()), "ghost") {
		t.Errorf("the refusal should name the slice, got: %v", err)
	}
	if !strings.Contains(err.Error(), "drift slice create ghost") {
		t.Errorf("the refusal should name the command that creates a slice, got: %v", err)
	}

	// The point of the card. Any of these means apply still provisions.
	for _, forbidden := range []string{
		"POST /ops/slice/create",
		"POST /ops/slice/resize",
		"POST /ops/slice/price",
	} {
		if n := rec.count(forbidden); n != 0 {
			t.Errorf("apply issued %d × %q against a slice it had already refused", n, forbidden)
		}
	}
}

// The control, and it is what stops the test above passing for the wrong
// reason: with the slice PRESENT, apply gets past the gate and does its work.
// Without this, deleting the whole apply body would keep the refusal test green.
func TestApply_ProceedsWhenTheSliceExists(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})
	applyIn(t, "slice: demo\ncanvas:\n  sites:\n    - ./site\n", "site")

	// The deploy itself may still fail against a stub that answers `{}` to
	// everything; what matters is that it got PAST the existence gate and
	// uploaded the site.
	_ = runApply(t)

	if rec.count("GET /ops/slice/get") == 0 {
		t.Error("apply never read the live slice, so nothing gated it")
	}
	if rec.count("POST /ops/canvas") == 0 {
		t.Error("apply stopped before deploying, so the refusal test above would " +
			"pass even with the whole apply body removed")
	}
	// And it still creates nothing: a slice that exists is not resized either.
	if n := rec.count("POST /ops/slice/create"); n != 0 {
		t.Errorf("apply created a slice that already existed (%d calls)", n)
	}
}

// The readiness poll has to survive the create and grow branches that used to
// own it. Without it the triad fires at a runner that is still coming up and
// every one of Atomic, Backbone and Canvas fails with "runner unreachable",
// which reads as a platform fault and is really an ordering problem.
func TestApply_ChecksReadinessBeforeDeploying(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ops/slice/get":
			_, _ = w.Write([]byte(`{"name":"demo","tier":"hacker","config":{}}`))
		case "/ops/slice/status":
			_, _ = w.Write([]byte(`{"ready":true}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})
	applyIn(t, "slice: demo\ncanvas:\n  sites:\n    - ./site\n", "site")

	_ = runApply(t)

	if rec.count("GET /ops/slice/status") == 0 {
		t.Error("apply deployed without checking the slice was ready — the poll was " +
			"dropped with the branches that used to own it")
	}
}

// applyIn writes a Driftfile and its named directories into a temp dir and makes
// that the working directory, because `drift file apply` reads ./Driftfile.
func applyIn(t *testing.T, body string, dirs ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

// runApply drives the real command, so the flag wiring and the RunE body are
// both under test rather than a hand-called helper.
func runApply(t *testing.T) error {
	t.Helper()
	cmd := getApplyCmd()
	cmd.SetArgs(nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}
