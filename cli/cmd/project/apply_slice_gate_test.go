package project

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyIn writes a Driftfile and its named directories into a temp dir and makes
// that the working directory, because `drift apply` reads ./Driftfile.
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

// applyAgreeingAtZero drives `drift apply` on the Driftfile in the working
// directory, with nobody at a terminal and the price agreed at 0: what a stub
// that answers `{}` to /ops/slice/price quotes. A plan that asks anything else
// (a removal, a data loss, another price) is refused, as it would be in CI.
func applyAgreeingAtZero(t *testing.T) error {
	t.Helper()
	a := noAnswers()
	a.price = 0
	return runApply(applyOptions{noEnvFile: true, answers: a, out: os.Stdout, in: strings.NewReader("")})
}

// runPlanCmd drives `drift plan` through the real command, so the flag wiring
// is under test alongside the body.
func runPlanCmd(t *testing.T) error {
	t.Helper()
	cmd := GetPlanCmd()
	cmd.SetArgs([]string{"--no-env-file"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

// assertApplyRefusesBeforeAnyRoute applies a Driftfile through `drift apply`
// against a stub that fails the test on any `/ops/` request, and asserts the
// apply is refused with exactly `want`.
//
// The recorder is the subject: a refusal that arrives after the slice is
// created or resized reads the same as one that arrives first, and only the
// second leaves the slice as it was.
func assertApplyRefusesBeforeAnyRoute(t *testing.T, body, want string) {
	t.Helper()
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ops/") {
			t.Errorf("apply called %s %s before refusing the file", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	// After the stub, which gives the test a HOME of its own.
	withRealSchema(t)
	applyIn(t, body, "site")

	err := runApply(applyOptions{noEnvFile: true, answers: noAnswers(), out: os.Stdout, in: strings.NewReader("")})
	if err == nil || err.Error() != want {
		t.Fatalf("apply returned %v, want the refusal %q", err, want)
	}
	for _, hit := range rec.hits {
		if strings.Contains(hit, " /ops/") {
			t.Errorf("a route was called before the refusal: %s", hit)
		}
	}
}

// The preflight runs before the plan reads the slice. The control is a file
// the preflight accepts, which does reach the slice.
func TestApply_ThePreflightRunsBeforeAnyRoute(t *testing.T) {
	assertApplyRefusesBeforeAnyRoute(t,
		"slice \"demo\" { billing = \"monthly\" }\ncanvas {\n  site \"/\" { dir = \"site\" }\n  site \"/a\" { dir = \"site\" }\n  site \"/a/\" { dir = \"site\" }\n}\n",
		`canvas site collision — these sites share a slug and would overwrite each other on the slice:
  - "a" is served by 2 sites: site → /a, site → /a`)

	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	withRealSchema(t)
	applyIn(t, "slice \"demo\" { billing = \"monthly\" }\ncanvas {\n  site \"/\" { dir = \"site\" }\n}\n", "site")
	_ = runApply(applyOptions{noEnvFile: true, answers: noAnswers(), out: os.Stdout, in: strings.NewReader("")})
	if rec.count("GET /ops/slice/get") == 0 {
		t.Error("a file the preflight accepts never reached the slice, so the refusal above proves nothing")
	}
}
