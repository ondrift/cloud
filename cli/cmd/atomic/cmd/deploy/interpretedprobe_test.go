package atomic_cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// interpretedSDKAnswersProbe reads the installed SDK file for lang's own
// version constant, never from a flag the install step sets: a build that
// vendors or installs an SDK before the probe existed must declare nothing.
func TestInterpretedSDKAnswersProbe_PerLanguage(t *testing.T) {
	for name, tc := range map[string]struct {
		lang, to, content string
	}{
		"python, the fast vendor path": {"python", "vendor/drift.py", "PROBE_PROTOCOL = 1\n"},
		"node, the fast vendor path":   {"node", "node_modules/@ondrift/sdk/node/index.js", "const PROBE_PROTOCOL = 1;\n"},
		"ruby, the fast vendor path":   {"ruby", "drift.rb", "PROBE_PROTOCOL = 1\n"},
		"php, the fast vendor path":    {"php", "vendor/php/drift.php", "const PROBE_PROTOCOL = 1;\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stageDir := t.TempDir()
			path := filepath.Join(stageDir, filepath.FromSlash(tc.to))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if !interpretedSDKAnswersProbe(stageDir, tc.lang) {
				t.Errorf("an SDK carrying %s is not read as answering the probe", probeConstant)
			}
		})
	}
}

// An SDK from before the probe existed — the vendored file present, but with
// no PROBE_PROTOCOL constant — is read as not answering it.
func TestInterpretedSDKAnswersProbe_AnOlderSDKDoesNotAnswer(t *testing.T) {
	for name, tc := range map[string]struct{ lang, to string }{
		"python": {"python", "vendor/drift.py"},
		"node":   {"node", "node_modules/@ondrift/sdk/node/index.js"},
		"ruby":   {"ruby", "drift.rb"},
		"php":    {"php", "vendor/php/drift.php"},
	} {
		t.Run(name, func(t *testing.T) {
			stageDir := t.TempDir()
			path := filepath.Join(stageDir, filepath.FromSlash(tc.to))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("# an SDK from before the probe existed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if interpretedSDKAnswersProbe(stageDir, tc.lang) {
				t.Errorf("an SDK with no %s must not be read as answering the probe", probeConstant)
			}
		})
	}
}

// A package manager's own install layout (not the fast vendor path's) is
// still found, because the real package is the same single file under its
// own name: the search falls back to it.
func TestInterpretedSDKAnswersProbe_FindsARealPackageManagerInstall(t *testing.T) {
	stageDir := t.TempDir()
	// bundler's own layout, nothing like sdkVendorLayout's "drift.rb" at the root.
	gemPath := filepath.Join(stageDir, "vendor", "bundle", "ruby", "3.2.0", "gems", "drift-sdk-0.11.0", "lib", "drift.rb")
	if err := os.MkdirAll(filepath.Dir(gemPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gemPath, []byte("module Drift\n  PROBE_PROTOCOL = 1\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !interpretedSDKAnswersProbe(stageDir, "ruby") {
		t.Error("a real gem install, laid out nothing like the fast vendor path, must still be found")
	}
}

// No SDK file at all (a backend with no dependency on it reached, or a stage
// this call never populated) answers false rather than panicking.
func TestInterpretedSDKAnswersProbe_NoFileAtAll(t *testing.T) {
	if interpretedSDKAnswersProbe(t.TempDir(), "python") {
		t.Error("an empty stage dir cannot answer the probe")
	}
}
