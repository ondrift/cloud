package file

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// withStaleSchema gives HOME a schema one format behind this binary: the
// fixture of the format it implements, relabelled 1.19.0 and without
// `atomic.elements`, which 1.21.0 added. It returns the current fixture's bytes.
func withStaleSchema(t *testing.T) []byte {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(migrateFixtures, "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(current, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = "1.19.0"
	section := doc["definitions"].(map[string]any)["atomicSection"].(map[string]any)
	delete(section["properties"].(map[string]any), "elements")
	stale, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	return current
}

// runLint drives the real command against path, with the platform at api.
func runLint(t *testing.T, api, path string) (stderr string, err error) {
	t.Helper()
	prev := common.APIBaseURL
	common.APIBaseURL = api
	t.Cleanup(func() { common.APIBaseURL = prev })
	common.ResetDriftfileSchemaRefresh()
	t.Cleanup(common.ResetDriftfileSchemaRefresh)

	var errOut bytes.Buffer
	cmd := getLintCmd()
	cmd.SetArgs([]string{path})
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return errOut.String(), err
}

// A cache older than this binary was validated against in silence: a nested
// Driftfile, legal in the format this binary implements, was refused with
// "additional properties 'elements' not allowed" and nothing said the schema
// was two formats old. Offline, the refusal now arrives with both versions and
// the command that refreshes the schema.
func TestLint_AStaleSchemaOfflineIsNamedWithItsRemedy(t *testing.T) {
	withStaleSchema(t)
	path, _ := driftfileFrom(t, "elements_nested.yaml")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // a platform that cannot be reached

	stderr, err := runLint(t, down.URL, path)
	if err == nil || !strings.Contains(err.Error(), "elements") {
		t.Fatalf("against a 1.19.0 schema the nested file is refused for `elements`, got %v", err)
	}
	for _, want := range []string{common.ImplementedDriftfileFormat, "1.19.0", "drift slice list"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal must come with a warning naming %q; stderr was:\n%s", want, stderr)
		}
	}
}

// Online, a cache older than this binary is refreshed before the file is
// validated, and the file the stale copy refused is valid.
func TestLint_AStaleSchemaIsRefreshedWhenThePlatformAnswers(t *testing.T) {
	current := withStaleSchema(t)
	path, _ := driftfileFrom(t, "elements_nested.yaml")
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/driftfile/schema" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(current)
	}))
	t.Cleanup(platform.Close)

	stderr, err := runLint(t, platform.URL, path)
	if err != nil {
		t.Fatalf("with the platform reachable the stale schema must be refreshed and the file pass, got:\n%v\nstderr:\n%s", err, stderr)
	}
	if v := common.DriftfileSchemaVersion(); v != common.ImplementedDriftfileFormat {
		t.Errorf("the cache holds %q after the refresh, want %q", v, common.ImplementedDriftfileFormat)
	}
	if stderr != "" {
		t.Errorf("a refreshed, current schema warns of nothing, got:\n%s", stderr)
	}
}

// The control: a current cache is linted without touching the network.
func TestLint_ACurrentSchemaReachesNoNetwork(t *testing.T) {
	withServedSchema(t)
	path, _ := driftfileFrom(t, "elements_nested.yaml")
	calls := 0
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	t.Cleanup(platform.Close)

	if _, err := runLint(t, platform.URL, path); err != nil {
		t.Fatalf("the nested file is valid in the current format: %v", err)
	}
	if calls != 0 {
		t.Errorf("lint of a current cache made %d network call(s), want none", calls)
	}
}
