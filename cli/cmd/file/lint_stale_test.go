package file

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// schemaAt is the platform's schema relabelled as one version, and, when
// stale, without `atomic.backends`, which a 1.19.0 schema did not have.
func schemaAt(t *testing.T, version string, stale bool) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(realSchema(t), &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = version
	if stale {
		section := doc["definitions"].(map[string]any)["atomicSection"].(map[string]any)
		delete(section["properties"].(map[string]any), "backends")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// withStaleSchema gives HOME a schema one format behind this binary, and
// returns the current one.
func withStaleSchema(t *testing.T) []byte {
	t.Helper()
	withSchemaBytes(t, schemaAt(t, "1.19.0", true))
	return schemaAt(t, common.ImplementedDriftfileFormat, false)
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

// A cache older than this binary was validated against in silence: a file
// legal in the format this binary implements was refused with "additional
// properties 'backends' not allowed" and nothing said the schema was formats
// old. Offline, the refusal now arrives with both versions and the command
// that refreshes the schema.
func TestLint_AStaleSchemaOfflineIsNamedWithItsRemedy(t *testing.T) {
	withStaleSchema(t)
	path := lintFixture(t, "GetPing")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // a platform that cannot be reached

	stderr, err := runLint(t, down.URL, path)
	if err == nil || !strings.Contains(err.Error(), "backends") {
		t.Fatalf("against a 1.19.0 schema the file is refused for `backends`, got %v", err)
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
	path := lintFixture(t, "GetPing")
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
	withSchemaBytes(t, schemaAt(t, common.ImplementedDriftfileFormat, false))
	path := lintFixture(t, "GetPing")
	calls := 0
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	t.Cleanup(platform.Close)

	if _, err := runLint(t, platform.URL, path); err != nil {
		t.Fatalf("the file is valid in the current format: %v", err)
	}
	if calls != 0 {
		t.Errorf("lint of a current cache made %d network call(s), want none", calls)
	}
}
