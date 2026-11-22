package atomic_cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// decodeMeta parses the multipart "metadata" field operatorSink sends.
func decodeMeta(t *testing.T, raw string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("metadata is not JSON: %v (%q)", err, raw)
	}
	return fields
}

// A Go build declares `probe` to the operator only when the resolved SDK's
// genmain carries the start check (probe.Requested), never from the bare
// presence of the genmain package: an SDK released between concurrent
// Functions and the probe has genmain, serves buffered requests, and still
// predates the probe.
func TestResolvedGoSDK_ProbeFollowsTheSourceNotTheDirectory(t *testing.T) {
	cache := t.TempDir()
	withProbe := filepath.Join(cache, "github.com", "ondrift", "cloud", "sdk@v0.13.0", "go", "genmain")
	withoutProbe := filepath.Join(cache, "github.com", "ondrift", "cloud", "sdk@v0.11.0", "go", "genmain")
	for _, dir := range []string{withProbe, withoutProbe} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(withProbe, "genmain.go"), []byte(
		"package genmain\n\nfunc Serve() {\n\tcase probe.Requested(drift.ProbeProtocol):\n\t\tprobe.Answer(nil, 1)\n}\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withoutProbe, "genmain.go"), []byte(
		"package genmain\n\nfunc Serve() {\n\t// no probe case in this release\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		version string
		want    goSDK
	}{
		"a genmain that answers the probe":   {"v0.13.0", goSDK{version: "v0.13.0", genmain: true, probe: true}},
		"a genmain before the probe existed": {"v0.11.0", goSDK{version: "v0.11.0", genmain: true, probe: false}},
		"no genmain at all, so no probe":     {"v0.10.0", goSDK{version: "v0.10.0"}},
	} {
		t.Run(name, func(t *testing.T) {
			stage := t.TempDir()
			gomod := "module x\n\nrequire github.com/ondrift/cloud/sdk " + tc.version + "\n"
			if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte(gomod), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := resolvedGoSDK(stage, cache); got != tc.want {
				t.Errorf("resolvedGoSDK = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// operatorSink declares `probe: 1` in the metadata only when the artifact's
// Probe is true, and omits the key entirely otherwise, so a build whose SDK
// predates the probe deploys exactly as it always did.
func TestOperatorSink_DeclaresProbeOnlyWhenTheBuildSaysItAnswers(t *testing.T) {
	for name, probe := range map[string]bool{"an SDK that answers the probe": true, "an older SDK": false} {
		t.Run(name, func(t *testing.T) {
			var gotFields map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ops/atomic" {
					http.NotFound(w, r)
					return
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				gotFields = decodeMeta(t, r.FormValue("metadata"))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			previous := common.APIBaseURL
			common.APIBaseURL = srv.URL
			defer func() { common.APIBaseURL = previous }()
			t.Setenv("HOME", t.TempDir())
			if err := common.SaveSession("access-token", "refresh-token"); err != nil {
				t.Fatal(err)
			}

			bin := filepath.Join(t.TempDir(), "app")
			if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := operatorSink(FuncArtifact{
				Name: "hello", Method: "get", Language: "go", Backend: "default",
				SourcePath: bin, Probe: probe,
			}); err != nil {
				t.Fatalf("operatorSink: %v", err)
			}

			_, declared := gotFields["probe"]
			if declared != probe {
				t.Errorf("probe declared = %v, want %v, fields = %v", declared, probe, gotFields)
			}
			if declared && gotFields["probe"] != float64(1) {
				t.Errorf(`probe = %v, want 1`, gotFields["probe"])
			}
		})
	}
}
