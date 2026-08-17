package slice

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The three verbs that used to build a slice's shape from a manifest must not
// post one any more.
//
// Asserted against the REQUESTS, not the return value. Each of these still
// fails here — there is no terminal in a test, so the handoff refuses — and an
// error tells you nothing about whether a shape went out on the way to it. The
// old paths posted to /ops/slice/create and /ops/slice/resize; the point of the
// card is that nothing does.
func TestFromDriftfile_NoVerbPostsASliceShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"create --from", func() error { return runVerb("create", "--from", "Driftfile") }},
		{"resize --from", func() error { return runVerb("resize", "--from", "Driftfile") }},
		{"shrink", func() error { return runVerb("shrink") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := stubEverything(t)
			driftfileIn(t, "slice: demo\n")
			withNonTerminalStdin(t)

			_ = tc.run()

			// /ops/slice/price is the load-bearing one. create and resize are
			// only reached once a diff says the shape CHANGED, so a fixture
			// whose manifest happens to match the live slice never posts one
			// and the assertion passes on the old code too — measured, not
			// assumed. Pricing is unconditional on the old path: it prices the
			// manifest before it has any idea whether anything differs.
			for _, forbidden := range []string{
				"POST /ops/slice/price",
				"POST /ops/slice/create",
				"POST /ops/slice/resize",
			} {
				if seen.count(forbidden) != 0 {
					t.Errorf("%s issued %q — it is still reading a shape out of the "+
						"manifest, which is what this card removes", tc.name, forbidden)
				}
			}
		})
	}
}

// Each verb announces itself once, through the shared registry, so the set of
// deprecations can be listed without invoking any of them.
//
// Asserted against Deprecations() because there is no user-facing listing
// command to check instead.
func TestFromDriftfile_EachVerbRegistersItsDeprecation(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)

	stubEverything(t)
	driftfileIn(t, "slice: demo\n")
	withNonTerminalStdin(t)

	_ = runVerb("create", "--from", "Driftfile")
	_ = runVerb("resize", "--from", "Driftfile")
	_ = runVerb("shrink")

	got := map[string]bool{}
	for _, d := range common.Deprecations() {
		got[d.Old] = true
		if d.RemoveAfter == "" {
			t.Errorf("%q has no RemoveAfter — a shim with no stated exit becomes a "+
				"permanent second surface", d.Old)
		}
	}
	for _, want := range []string{
		"drift slice create --from",
		"drift slice resize --from",
		"drift slice shrink",
	} {
		if !got[want] {
			t.Errorf("%q is not in the deprecation registry, so it cannot be listed "+
				"without running it", want)
		}
	}
}

// A flag left at its default was not passed, and its owner has nothing to be
// told. Without this the notice fires on every plain `drift slice resize`.
func TestDeprecateFlag_SaysNothingWhenTheFlagIsUntouched(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices strings.Builder
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	stubEverything(t)
	withNonTerminalStdin(t)
	_ = runVerb("resize", "some-slice")

	if strings.Contains(notices.String(), "--from") {
		t.Errorf("a run that never passed --from was told it is deprecated:\n%s",
			notices.String())
	}
}

// ─── helpers ────────────────────────────────────────────────────────

type calls struct {
	mu   sync.Mutex
	hits []string
}

func (c *calls) note(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = append(c.hits, s)
}

func (c *calls) count(want string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, h := range c.hits {
		if h == want {
			n++
		}
	}
	return n
}

// stubEverything answers every api and configurator path, and records what was
// asked. A generous stub is right here: the assertion is about a call that must
// NOT happen, so anything the code does reach should succeed rather than fail
// for an unrelated reason and hide the one being tested.
func stubEverything(t *testing.T) *calls {
	t.Helper()
	seen := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r.Method + " " + r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	prevAPI, prevCfg := common.APIBaseURL, common.ConfiguratorBaseURL
	common.APIBaseURL, common.ConfiguratorBaseURL = srv.URL, srv.URL
	t.Cleanup(func() { common.APIBaseURL, common.ConfiguratorBaseURL = prevAPI, prevCfg })

	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("access-token", "refresh-token"); err != nil {
		t.Fatal(err)
	}
	return seen
}

func driftfileIn(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

// runVerb builds the real `drift slice` tree and runs one verb through it, so
// the flag wiring is under test rather than a hand-called helper.
func runVerb(args ...string) error {
	cmd := GetCmd()
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}
