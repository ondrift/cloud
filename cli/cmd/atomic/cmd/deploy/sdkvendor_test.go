package atomic_cmd

// sdkvendor_test.go — the no-Docker fast path.
//
// The gate these tests defend (HQ CLI-STANDARDUSAGE-34XGSA): "a Node/Python
// function importing only the Drift SDK deploys on a machine with no Docker."
// The install tests below run with an empty PATH, so `docker` genuinely cannot
// be found — the same condition a first-time user without Rancher Desktop is
// in. They fail against the pre-fix code with the ensureDocker error.
//
// No test here touches the network: the two package variables that reach
// GitHub are stubbed with an in-memory tarball shaped exactly like the one
// codeload serves (a single `sdk-<version>/` wrapper directory).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tag carries the `sdk/` prefix the monorepo namespaces its SDK releases
// with, because the CLI is tagged out of the same repository.
const stubSDKTag = "sdk/v4.1.2"

// stubSDKFiles mirrors the MONOREPO's layout: the SDK is a directory beside the
// CLI, so every path is under `sdk/`. It used to have a repository to itself,
// where these sat at the root — a stub still shaped that way would pass against
// a vendor layout that could no longer find a single file in the real tarball.
var stubSDKFiles = map[string]string{
	"sdk/package.json":    `{"name":"@ondrift/sdk","version":"4.1.2","main":"node/index.js"}`,
	"sdk/node/index.js":   "module.exports = { run: () => {} };\n",
	"sdk/python/drift.py": "def run(h):\n    pass\n",
	"sdk/ruby/drift.rb":   "module Drift; end\n",
	"sdk/php/drift.php":   "<?php\n",
	"README.md":           "# Drift\n",
	"cli/main.go":         "package main\n",
}

// buildStubTarball produces a gzipped tar with GitHub's `<repo>-<ref>/` prefix.
func buildStubTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range stubSDKFiles {
		hdr := &tar.Header{
			Name:     "cloud-4.1.2/" + name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withStubbedSDK redirects the cache into the test's own tempdir and replaces
// the network surface with the in-memory tarball. Returns a pointer to a
// counter of how many times the tarball was actually fetched.
func withStubbedSDK(t *testing.T) *int {
	t.Helper()
	// os.UserCacheDir reads $XDG_CACHE_HOME on Linux and $HOME on macOS.
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)

	tarball := buildStubTarball(t)
	fetches := 0

	origResolve, origFetch := sdkResolveTag, sdkFetchTarball
	t.Cleanup(func() { sdkResolveTag, sdkFetchTarball = origResolve, origFetch })

	sdkResolveTag = func() (string, error) { return stubSDKTag, nil }
	sdkFetchTarball = func(tag string) (io.ReadCloser, error) {
		fetches++
		return io.NopCloser(bytes.NewReader(tarball)), nil
	}
	return &fetches
}

// noDockerPATH empties PATH so exec.LookPath("docker") cannot succeed. This is
// what makes these tests a real proof rather than an assertion about which
// branch was taken.
func noDockerPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── The gate ───────────────────────────────────────────────────────────────

// TestInstallNodeDeps_SDKOnlyNeedsNoDocker is the card's "Done when" for Node.
func TestInstallNodeDeps_SDKOnlyNeedsNoDocker(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	// Exactly what `drift atomic new` scaffolds.
	writeFile(t, filepath.Join(elem, "package.json"),
		`{"name":"atomic-hello","version":"1.0.0","private":true,`+
			`"dependencies":{"@ondrift/sdk":"github:ondrift/cloud"}}`)

	if err := installNodeDeps(elem, stage); err != nil {
		t.Fatalf("installNodeDeps failed with no Docker on PATH: %v", err)
	}
	// The wrapper does require('@ondrift/sdk') — that must resolve.
	for _, want := range []string{
		"node_modules/@ondrift/sdk/package.json",
		"node_modules/@ondrift/sdk/node/index.js",
	} {
		if _, err := os.Stat(filepath.Join(stage, filepath.FromSlash(want))); err != nil {
			t.Errorf("expected vendored %s: %v", want, err)
		}
	}
}

// TestInstallPythonDeps_SDKOnlyNeedsNoDocker is the card's "Done when" for
// Python. The wrapper prepends vendor/ to sys.path, so vendor/drift.py is what
// makes `import drift` resolve.
func TestInstallPythonDeps_SDKOnlyNeedsNoDocker(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "requirements.txt"),
		"drift-sdk @ git+https://github.com/ondrift/cloud/sdk.git#subdirectory=python\n")

	if err := installPythonDeps(elem, stage); err != nil {
		t.Fatalf("installPythonDeps failed with no Docker on PATH: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "vendor", "drift.py")); err != nil {
		t.Errorf("expected vendored vendor/drift.py: %v", err)
	}
}

// TestInstallRubyDeps_SDKOnlyNeedsNoDocker — the Ruby wrapper does
// $LOAD_PATH.unshift(__dir__) then require 'drift', so drift.rb goes at the
// stage root rather than under vendor/.
func TestInstallRubyDeps_SDKOnlyNeedsNoDocker(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "Gemfile"),
		"source \"https://rubygems.org\"\n\ngem \"drift-sdk\", git: \"https://github.com/ondrift/cloud/sdk\", branch: \"master\"\n")

	if err := installRubyDeps(elem, stage); err != nil {
		t.Fatalf("installRubyDeps failed with no Docker on PATH: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "drift.rb")); err != nil {
		t.Errorf("expected vendored drift.rb at the stage root: %v", err)
	}
}

// TestInstallPHPDeps_SDKOnlyNeedsNoDocker — the PHP wrapper require_once's
// vendor/autoload.php, so the fast path has to synthesise the one-file
// equivalent of what composer would have generated.
func TestInstallPHPDeps_SDKOnlyNeedsNoDocker(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "composer.json"),
		`{"repositories":[{"type":"vcs","url":"https://github.com/ondrift/cloud/sdk"}],`+
			`"require":{"php":">=8.1","ondrift/sdk":"*"}}`)

	if err := installPHPDeps(elem, stage); err != nil {
		t.Fatalf("installPHPDeps failed with no Docker on PATH: %v", err)
	}
	autoload, err := os.ReadFile(filepath.Join(stage, "vendor", "autoload.php"))
	if err != nil {
		t.Fatalf("expected vendor/autoload.php: %v", err)
	}
	if !strings.Contains(string(autoload), "drift.php") {
		t.Errorf("autoload.php should require the SDK, got: %q", autoload)
	}
	if _, err := os.Stat(filepath.Join(stage, "vendor", "php", "drift.php")); err != nil {
		t.Errorf("expected vendored vendor/php/drift.php: %v", err)
	}
}

// ─── An element that declares nothing still needs the SDK ───────────────────
//
// The generated wrapper requires the SDK unconditionally, so a bare element —
// one source file and no manifest — needs it vendored exactly as much as one
// that declares it. Every *SDKOnly helper reports a missing manifest as
// SDK-only for this reason. Nothing declared is the shape `drift atomic deploy`
// produces for a single-file function, and the deploy reports success either
// way, so the gap surfaces only as a 500 at first invocation.

func TestInstallNodeDeps_NoManifestStillVendorsTheSDK(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "nodecheck.js"),
		"const drift = require('@ondrift/sdk');\nmodule.exports = { handle: () => {} };\n")

	if err := installNodeDeps(elem, stage); err != nil {
		t.Fatalf("installNodeDeps failed for a manifest-less element: %v", err)
	}
	// app.js does require('@ondrift/sdk'); without this the function deploys
	// clean and 500s with "Cannot find module '@ondrift/sdk'".
	if _, err := os.Stat(filepath.Join(stage, "node_modules", "@ondrift", "sdk", "node", "index.js")); err != nil {
		t.Errorf("expected the SDK vendored with no package.json present: %v", err)
	}
}

func TestInstallPythonDeps_NoManifestStillVendorsTheSDK(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "health.py"), "import drift\n\n\ndef handle(req):\n    return None\n")

	if err := installPythonDeps(elem, stage); err != nil {
		t.Fatalf("installPythonDeps failed for a manifest-less element: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "vendor", "drift.py")); err != nil {
		t.Errorf("expected the SDK vendored with no requirements.txt present: %v", err)
	}
}

func TestInstallRubyDeps_NoManifestStillVendorsTheSDK(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "health.rb"), "require 'drift'\n\ndef handle(req)\nend\n")

	if err := installRubyDeps(elem, stage); err != nil {
		t.Fatalf("installRubyDeps failed for a manifest-less element: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "drift.rb")); err != nil {
		t.Errorf("expected the SDK vendored with no Gemfile present: %v", err)
	}
}

func TestInstallPHPDeps_NoManifestStillVendorsTheSDK(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "health.php"), "<?php\nfunction handle($req) {}\n")

	if err := installPHPDeps(elem, stage); err != nil {
		t.Fatalf("installPHPDeps failed for a manifest-less element: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "vendor", "php", "drift.php")); err != nil {
		t.Errorf("expected the SDK vendored with no composer.json present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "vendor", "autoload.php")); err != nil {
		t.Errorf("expected vendor/autoload.php with no composer.json present: %v", err)
	}
}

// The control: staging a manifest that IS present must survive the reordering.
// Without this, moving the vendor step could silently stop copying package.json
// into the artifact and every assertion above would still pass.
func TestInstallNodeDeps_StagesThePackageJsonItWasGiven(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "package.json"),
		`{"name":"atomic-hello","version":"1.0.0","private":true,`+
			`"dependencies":{"@ondrift/sdk":"github:ondrift/cloud"}}`)
	writeFile(t, filepath.Join(elem, "package-lock.json"), `{"lockfileVersion":3}`)

	if err := installNodeDeps(elem, stage); err != nil {
		t.Fatalf("installNodeDeps: %v", err)
	}
	for _, want := range []string{"package.json", "package-lock.json"} {
		if _, err := os.Stat(filepath.Join(stage, want)); err != nil {
			t.Errorf("expected %s staged into the artifact: %v", want, err)
		}
	}
}

// ─── The controls: the container path must still be the container path ──────

// TestInstallNodeDeps_RealDependencyStillUsesContainer proves the fast path
// didn't quietly become "never use Docker". One non-SDK dependency is exactly
// the native-extension case the container exists for, so it must still be
// routed there — and with no docker on PATH that surfaces as the ensureDocker
// error, which is the evidence the container path was chosen.
func TestInstallNodeDeps_RealDependencyStillUsesContainer(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "package.json"),
		`{"dependencies":{"@ondrift/sdk":"github:ondrift/cloud","sharp":"^0.33.0"}}`)

	err := installNodeDeps(elem, stage)
	if err == nil {
		t.Fatal("expected the container path (and therefore a docker error) for a non-SDK dependency")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected a docker error, got: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(stage, "node_modules")); serr == nil {
		t.Error("nothing should have been vendored for a non-SDK dependency set")
	}
}

// TestForceContainerBuildEscapeHatch confirms the opt-out works, so anyone who
// needs the container semantics can always get them back.
func TestForceContainerBuildEscapeHatch(t *testing.T) {
	withStubbedSDK(t)
	noDockerPATH(t)
	t.Setenv("DRIFT_FORCE_CONTAINER_BUILD", "1")

	elem, stage := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(elem, "requirements.txt"), "drift-sdk\n")

	err := installPythonDeps(elem, stage)
	if err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected the container path when forced, got: %v", err)
	}
}

// ─── Detection ──────────────────────────────────────────────────────────────

func TestSDKOnlyDetection(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		manifest string
		body     string
		want     bool
	}{
		{"node: scaffolded SDK only", "node", "package.json",
			`{"dependencies":{"@ondrift/sdk":"github:ondrift/cloud"}}`, true},
		{"node: no dependencies at all", "node", "package.json",
			`{"name":"x","dependencies":{}}`, true},
		{"node: devDependencies are ignored", "node", "package.json",
			`{"dependencies":{"@ondrift/sdk":"*"},"devDependencies":{"jest":"^29"}}`, true},
		{"node: a second dependency", "node", "package.json",
			`{"dependencies":{"@ondrift/sdk":"*","express":"^4"}}`, false},
		{"node: optionalDependencies disqualify", "node", "package.json",
			`{"dependencies":{"@ondrift/sdk":"*"},"optionalDependencies":{"sharp":"^0.33"}}`, false},
		{"node: a different single dependency", "node", "package.json",
			`{"dependencies":{"express":"^4"}}`, false},

		{"python: scaffolded git URL", "python", "requirements.txt",
			"drift-sdk @ git+https://github.com/ondrift/cloud/sdk.git#subdirectory=python\n", true},
		{"python: comments and blanks", "python", "requirements.txt",
			"# the drift sdk\n\ndrift-sdk\n", true},
		{"python: pinned version", "python", "requirements.txt", "drift-sdk==4.1.2\n", true},
		{"python: a second package", "python", "requirements.txt",
			"drift-sdk\nrequests==2.31.0\n", false},
		{"python: a -r include", "python", "requirements.txt",
			"-r other.txt\ndrift-sdk\n", false},

		{"ruby: scaffolded Gemfile", "ruby", "Gemfile",
			"source \"https://rubygems.org\"\n\ngem \"drift-sdk\", git: \"https://github.com/ondrift/cloud/sdk\"\n", true},
		{"ruby: a second gem", "ruby", "Gemfile",
			"source \"https://rubygems.org\"\ngem \"drift-sdk\"\ngem \"nokogiri\"\n", false},
		{"ruby: gemspec directive", "ruby", "Gemfile",
			"source \"https://rubygems.org\"\ngemspec\n", false},

		{"php: scaffolded composer.json", "php", "composer.json",
			`{"require":{"php":">=8.1","ondrift/sdk":"*"}}`, true},
		{"php: ext- constraints are not packages", "php", "composer.json",
			`{"require":{"php":">=8.1","ext-json":"*","ondrift/sdk":"*"}}`, true},
		{"php: a second package", "php", "composer.json",
			`{"require":{"ondrift/sdk":"*","guzzlehttp/guzzle":"^7"}}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, c.manifest), c.body)
			got, err := sdkOnlyManifest(c.lang, dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("sdkOnlyManifest(%s) = %v, want %v", c.lang, got, c.want)
			}
		})
	}
}

// ─── Cache + archive handling ───────────────────────────────────────────────

// TestSDKCacheFetchesOncePerTag — an element with many functions, and repeated
// deploys, must not re-download the SDK each time.
func TestSDKCacheFetchesOncePerTag(t *testing.T) {
	fetches := withStubbedSDK(t)

	for i := 0; i < 3; i++ {
		if err := vendorDriftSDK("python", t.TempDir()); err != nil {
			t.Fatalf("vendor %d: %v", i, err)
		}
	}
	if *fetches != 1 {
		t.Errorf("fetched %d times, want exactly 1 (cache keyed by tag)", *fetches)
	}
}

// TestExtractSDKTarGz_RejectsPathEscape — the archive arrives over the network,
// so a `..` entry must be refused rather than written outside the target.
func TestExtractSDKTarGz_RejectsPathEscape(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "pwned\n"
	hdr := &tar.Header{Name: "sdk-4.1.2/../../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	tw.Close() // #nosec G104
	gz.Close() // #nosec G104

	dst := t.TempDir()
	err := extractSDKTarGz(bytes.NewReader(buf.Bytes()), dst)
	if err == nil {
		t.Fatal("expected a path-escape refusal")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected an escape error, got: %v", err)
	}
}

// TestResolveLatestSDKTag_HonoursPin covers the reproducible-build / air-gap
// path, which is also the only tag resolution that touches no network.
func TestResolveLatestSDKTag_HonoursPin(t *testing.T) {
	t.Setenv("DRIFT_SDK_TAG", "v4.0.0")
	got, err := resolveLatestSDKTag()
	if err != nil {
		t.Fatal(err)
	}
	if got != "v4.0.0" {
		t.Errorf("tag = %q, want v4.0.0", got)
	}
}

// THE PREFIX HAS TO SURVIVE THE ROUND TRIP, and this is the one assertion that
// says so. The SDK shares a repository with the CLI, so its releases are tagged
// `sdk/vX.Y.Z`; the resolver strips that to compare versions and puts it back to
// name the tag. Strip without re-adding and the download URL becomes
// `refs/tags/v0.9.0` — a tag that does not exist, so resolution reports success
// and the fetch 404s a step later.
//
// The refs are the shape `git/matching-refs/tags/sdk/` really returns; check it
// with `gh api repos/ondrift/cloud/git/matching-refs/tags/sdk/`.
func TestSDKTagResolution_StripsAndRestoresTheNamespace(t *testing.T) {
	refs := []string{
		"refs/tags/sdk/v0.8.1",
		"refs/tags/sdk/v0.10.0",
		"refs/tags/sdk/v0.9.0",
	}
	bare := sdkTagsFromRefs(refs)
	for _, b := range bare {
		if strings.Contains(b, "/") {
			t.Fatalf("%q still carries a prefix — the version comparison would see a path", b)
		}
	}

	got := sdkTagPrefix + pickLatestSemverTag(bare)
	// 0.10.0 over 0.9.0 is also the case a lexical sort gets wrong.
	if got != "sdk/v0.10.0" {
		t.Errorf("resolved tag = %q, want sdk/v0.10.0", got)
	}
}

// TestPickLatestSemverTag pins the selection rule. The first case is the real
// tag list; check it against the live API with
// `gh api repos/ondrift/cloud/git/matching-refs/tags/sdk/`. The rest are the
// ways a naive "take the first element" or a lexical sort gets it wrong.
func TestPickLatestSemverTag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"the real SDK tag list",
			[]string{"v4.1.2", "v4.1.1", "v4.1.0", "v4.0.1", "v4.0.0", "v3.0.0", "v2.0.0", "v1.0.7"}, "v4.1.2"},
		{"order is not trusted",
			[]string{"v1.0.7", "v4.1.2", "v2.0.0"}, "v4.1.2"},
		{"numeric, not lexical — 10 beats 9",
			[]string{"v4.9.0", "v4.10.0"}, "v4.10.0"},
		{"patch ordering",
			[]string{"v4.1.9", "v4.1.10"}, "v4.1.10"},
		{"pre-releases are skipped",
			[]string{"v5.0.0-rc1", "v4.1.2"}, "v4.1.2"},
		{"unparseable tags are ignored",
			[]string{"latest", "master", "v4.1.2"}, "v4.1.2"},
		{"no version tags at all",
			[]string{"latest", "master"}, ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickLatestSemverTag(c.in); got != c.want {
				t.Errorf("pickLatestSemverTag(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSDKVendorLayout_MatchesTheRealSDK is the half TestPickLatestSemverTag
// and the install tests above cannot cover: every test in this file proves
// the installer correctly copies from wherever sdkVendorLayout SAYS the SDK
// files are, using stubSDKFiles — a map written by hand, in this same
// sitting, to match sdkVendorLayout. Neither can catch sdkVendorLayout itself
// going stale against the SDK this monorepo actually ships.
//
// So this reads past the stub, straight off disk. cli/ and sdk/ are siblings
// under cloud/public (this file's own comment: "the tarball's root holds
// cli/, sdk/ and a README"), which is why no network fetch is needed — the
// real tarball IS this checkout, at deploy time, and `from` is relative to
// exactly the root a checkout of this repo already has.
func TestSDKVendorLayout_MatchesTheRealSDK(t *testing.T) {
	// cli/cmd/atomic/cmd/deploy -> cli/cmd/atomic/cmd -> cli/cmd/atomic ->
	// cli/cmd -> cli -> repo root. Five levels; anchored here explicitly so a
	// future reorganisation of cloud/public updates it deliberately rather
	// than the path silently stopping resolving and this test skipping the
	// question it exists to ask.
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	// Sanity-check the anchor itself before trusting any os.Stat below —
	// a wrong repoRoot would otherwise report every `from` as missing for a
	// reason that has nothing to do with sdkVendorLayout.
	if _, err := os.Stat(filepath.Join(repoRoot, "sdk")); err != nil {
		t.Fatalf("repo root resolved to %s, which has no sdk/ directory (%v) — "+
			"cli/cmd/atomic/cmd/deploy's depth from the repo root changed; update the five ..'s above",
			repoRoot, err)
	}

	for lang, entries := range sdkVendorLayout {
		for _, e := range entries {
			path := filepath.Join(repoRoot, e.from)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("sdkVendorLayout[%q] names %q, which does not exist at %s: %v — "+
					"the SDK moved and this fast path would silently vendor nothing at that path",
					lang, e.from, path, err)
			}
		}
	}
}
