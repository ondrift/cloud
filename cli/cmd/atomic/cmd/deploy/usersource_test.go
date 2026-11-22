package atomic_cmd

// usersource_test.go: what the user-source archive of a backend holds.
//
// The archive is the portable copy of a developer's code. The slice stores one
// beside every function and every snapshot captures it, and a restore deploys it
// as the function's source. For a backend written in an interpreted language it
// must hold the source and the dependency manifests and not the tree a package
// manager installed beside them: the slice keeps its own installed tree across
// a deploy that brings none (DEPENDENCY_DIRS in the platform's slice
// atomic/deploy.rs), and a tree carried from a laptop replaces the one built for
// the slice. A backend written in a compiled language is archived whole.
//
// The deploy tests run a real deploy with the sink replaced, with the SDK served
// from memory and no container runtime on PATH, so they need neither the network
// nor Docker.

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// archiveEntries lists the names in the tar.gz at path, sorted.
func archiveEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s is not a gzip file: %v", path, err)
	}
	defer gz.Close()

	var names []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		names = append(names, hdr.Name)
	}
	sort.Strings(names)
	return names
}

// entriesUnder returns the entries that are dir itself or lie below it.
func entriesUnder(entries []string, dir string) []string {
	var under []string
	for _, e := range entries {
		if e == dir || strings.HasPrefix(e, dir+"/") {
			under = append(under, e)
		}
	}
	return under
}

// withParents is the entries an archive of a folder holding exactly these files
// lists: the files and every directory above them, sorted.
func withParents(files []string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			seen[d] = true
		}
	}
	var all []string
	for name := range seen {
		all = append(all, name)
	}
	sort.Strings(all)
	return all
}

// userSourceEntries makes the user-source archive of the backend folder dir, as
// a deploy of a backend written in language does, and lists its entries.
func userSourceEntries(t *testing.T, dir, language string) []string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	archive, err := createUserSourceArchive(dir, "usersource-test", language)
	if err != nil {
		t.Fatalf("createUserSourceArchive: %v", err)
	}
	defer os.Remove(archive)
	return archiveEntries(t, archive)
}

// interpretedBackend is a backend folder as `drift atomic fetch` leaves it on a
// developer's disk: the source and the dependency manifests they wrote, and the
// tree the package manager installed beside them. Every manifest declares the
// Drift SDK and nothing else, the shape a deploy builds without a container.
type interpretedBackend struct {
	language string
	// source is the file that declares the handler `hello`.
	source string
	body   string
	// manifests are written beside the source, by name.
	manifests map[string]string
	// installed is the tree the package manager left, relative to the folder.
	installed []string
}

var interpretedBackends = []interpretedBackend{
	{
		language: "node",
		source:   "handlers.js",
		body:     "function hello(req) {\n  return { ok: true };\n}\nmodule.exports = { hello };\n",
		manifests: map[string]string{
			"package.json":      `{"name":"hello","version":"1.0.0","private":true,"dependencies":{"@ondrift/sdk":"github:ondrift/cloud"}}`,
			"package-lock.json": `{"lockfileVersion":3}`,
		},
		installed: []string{"node_modules/lodash/index.js", "node_modules/lodash/package.json"},
	},
	{
		language: "python",
		source:   "handlers.py",
		body:     "def hello(req):\n    return {\"ok\": True}\n",
		manifests: map[string]string{
			"requirements.txt": "drift-sdk @ git+https://github.com/ondrift/cloud.git#subdirectory=sdk/python\n",
		},
		installed: []string{"vendor/requests/__init__.py", "vendor/requests/api.py"},
	},
	{
		language: "ruby",
		source:   "handlers.rb",
		body:     "def hello(req)\n  { ok: true }\nend\n",
		manifests: map[string]string{
			"Gemfile":      "source \"https://rubygems.org\"\n\ngem \"drift-sdk\", git: \"https://github.com/ondrift/cloud\", glob: \"sdk/ruby/*.gemspec\"\n",
			"Gemfile.lock": "GEM\n  specs:\n",
		},
		installed: []string{"vendor/bundle/bundler/setup.rb", "vendor/bundle/ruby/3.3.0/gems/rack-3.0.0/lib/rack.rb"},
	},
	{
		language: "php",
		source:   "handlers.php",
		body:     "<?php\nfunction hello($req) {\n    return ['ok' => true];\n}\n",
		manifests: map[string]string{
			"composer.json": `{"repositories":[{"type":"vcs","url":"https://github.com/ondrift/cloud"}],"require":{"php":">=8.1","ondrift/sdk":"*"}}`,
			"composer.lock": `{"packages":[]}`,
		},
		installed: []string{"vendor/autoload.php", "vendor/monolog/monolog/src/Logger.php"},
	},
}

// nestedVendorFile is a source file of the developer's own, in a directory that
// is named vendor and is not the installed tree.
func (b interpretedBackend) nestedVendorFile() string {
	return "src/vendor/lib" + filepath.Ext(b.source)
}

// writeSource lays out what the developer wrote.
func (b interpretedBackend) writeSource(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, b.source), b.body)
	for name, body := range b.manifests {
		writeFile(t, filepath.Join(dir, name), body)
	}
	writeFile(t, filepath.Join(dir, filepath.FromSlash(b.nestedVendorFile())), "the developer's own package\n")
}

// writeInstalled lays out what the package manager installed.
func (b interpretedBackend) writeInstalled(t *testing.T, dir string) {
	t.Helper()
	for _, f := range b.installed {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(f)), "installed by the package manager\n")
	}
}

// write lays out the whole folder.
func (b interpretedBackend) write(t *testing.T, dir string) {
	t.Helper()
	b.writeSource(t, dir)
	b.writeInstalled(t, dir)
}

// checkUserSource fails unless the entries of a user-source archive hold the
// developer's source and manifests and nothing of the installed tree.
func (b interpretedBackend) checkUserSource(t *testing.T, entries []string) {
	t.Helper()
	files := []string{b.source, b.nestedVendorFile()}
	for name := range b.manifests {
		files = append(files, name)
	}
	for _, f := range files {
		if !slices.Contains(entries, f) {
			t.Errorf("the user-source archive is missing %s, which the developer wrote; it holds %v", f, entries)
		}
	}
	for _, root := range []string{"node_modules", "vendor"} {
		if under := entriesUnder(entries, root); len(under) > 0 {
			t.Errorf("the user-source archive carries the installed tree: %v", under)
		}
	}
}

// checkDeployArchive fails unless the entries of the archive that runs on the
// slice hold what the build staged: the source, the generated wrapper, the
// manifests and the SDK the install step put in node_modules or vendor (a Ruby
// backend that declares only the SDK has its file at the root, for its wrapper
// loads it from there).
func (b interpretedBackend) checkDeployArchive(t *testing.T, entries []string) {
	t.Helper()
	files := []string{b.source, "app" + filepath.Ext(b.source)}
	for name := range b.manifests {
		files = append(files, name)
	}
	for _, item := range sdkVendorLayout[b.language] {
		files = append(files, item.to)
	}
	if b.language == "php" {
		files = append(files, "vendor/autoload.php")
	}
	for _, f := range files {
		if !slices.Contains(entries, f) {
			t.Errorf("the deploy archive is missing %s, which the build staged; it holds %v", f, entries)
		}
	}
}

// capturedDeploy is what one deployed function handed the sink: the entries of
// the archive that runs on the slice and of the user-source archive beside it.
type capturedDeploy struct {
	deploy     []string
	userSource []string
}

// captureDeploys replaces the sink for the test and lists each archive while it
// still exists: the deploy removes both once the sink returns.
func captureDeploys(t *testing.T) *[]capturedDeploy {
	t.Helper()
	var got []capturedDeploy
	prev := slotSink
	slotSink = func(a FuncArtifact) error {
		c := capturedDeploy{deploy: archiveEntries(t, a.SourcePath)}
		if a.UserSourcePath != "" {
			c.userSource = archiveEntries(t, a.UserSourcePath)
		}
		got = append(got, c)
		return nil
	}
	t.Cleanup(func() { slotSink = prev })
	return &got
}

// offlineDeploy readies a deploy that needs neither the network nor Docker: the
// SDK is served from memory and no container runtime is on PATH, so a manifest
// that asks for more than the SDK fails the test instead of starting a build.
func offlineDeploy(t *testing.T) {
	t.Helper()
	withStubbedSDK(t)
	noDockerPATH(t)
	t.Setenv("TMPDIR", t.TempDir())
	resetSDKDigestTag()
	t.Cleanup(resetSDKDigestTag)
}

// A backend folder holding source, manifests and an installed tree is archived
// as its source and its manifests: nothing under node_modules or vendor.
func TestUserSourceArchive_LeavesOutTheInstalledTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			dir := t.TempDir()
			b.write(t, dir)

			b.checkUserSource(t, userSourceEntries(t, dir, b.language))
		})
	}
}

// What is left out is the two directories the slice keeps, at the root of the
// folder, whichever language the backend is written in. The same names deeper in
// the tree are source the developer wrote.
func TestUserSourceArchive_LeavesOutOnlyTheTopLevelTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			dir := t.TempDir()
			b.writeSource(t, dir)
			writeFile(t, filepath.Join(dir, "node_modules", "left", "out.txt"), "the installed tree\n")
			writeFile(t, filepath.Join(dir, "vendor", "left", "out.txt"), "the installed tree\n")
			writeFile(t, filepath.Join(dir, "src", "node_modules", "kept.txt"), "the developer's own\n")

			entries := userSourceEntries(t, dir, b.language)

			b.checkUserSource(t, entries)
			if !slices.Contains(entries, "src/node_modules/kept.txt") {
				t.Errorf("a node_modules deeper in the tree is the developer's own and must be archived; the archive holds %v", entries)
			}
		})
	}
}

// A symbolic link named node_modules or vendor is the installed tree reached by
// another path, a tree shared across a monorepo or a package store. A link
// cannot be archived, so it is left out with what it points to, and the archive
// is still made.
func TestUserSourceArchive_ASymlinkedInstalledTreeIsLeftOut(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			dir, shared := t.TempDir(), t.TempDir()
			b.writeSource(t, dir)
			writeFile(t, filepath.Join(shared, "pkg", "index.js"), "installed elsewhere\n")
			for _, name := range []string{"node_modules", "vendor"} {
				if err := os.Symlink(shared, filepath.Join(dir, name)); err != nil {
					t.Skipf("symbolic links are not available here: %v", err)
				}
			}

			b.checkUserSource(t, userSourceEntries(t, dir, b.language))
		})
	}
}

// deployAsBackend runs a real DeployInterpretedBackend of the folder, the path
// `drift apply` takes for every backend written in an interpreted language, and
// returns what the sink received for its one function.
func (b interpretedBackend) deployAsBackend(t *testing.T) capturedDeploy {
	t.Helper()
	offlineDeploy(t)
	deployed := captureDeploys(t)
	dir := t.TempDir()
	b.write(t, dir)

	backends, err := BuildBackends([]FunctionSpec{{Name: "get:hello", Handler: "hello", Backend: "api", Dir: dir}})
	if err != nil {
		t.Fatalf("BuildBackends: %v", err)
	}
	if _, _, err := DeployInterpretedBackend(backends[0], "digest", nil); err != nil {
		t.Fatalf("DeployInterpretedBackend: %v", err)
	}
	if len(*deployed) != 1 {
		t.Fatalf("the sink received %d functions, want 1", len(*deployed))
	}
	return (*deployed)[0]
}

// deployAsFunction runs a real DeployFunction of the folder, the one-function
// build path, and returns what the sink received.
func (b interpretedBackend) deployAsFunction(t *testing.T) capturedDeploy {
	t.Helper()
	offlineDeploy(t)
	deployed := captureDeploys(t)
	dir := t.TempDir()
	b.write(t, dir)

	if err := DeployFunction(FunctionSpec{Name: "get:hello", Handler: "hello", Dir: dir}, true); err != nil {
		t.Fatalf("DeployFunction: %v", err)
	}
	if len(*deployed) != 1 {
		t.Fatalf("the sink received %d functions, want 1", len(*deployed))
	}
	return (*deployed)[0]
}

// A deploy of an interpreted backend stores the developer's source and manifests
// as its user-source archive and not the tree installed beside them.
func TestInterpretedBackendDeploy_UserSourceHoldsNoInstalledTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			b.checkUserSource(t, b.deployAsBackend(t).userSource)
		})
	}
}

// The one-function build path stores the same.
func TestSingleFunctionDeploy_UserSourceHoldsNoInstalledTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			b.checkUserSource(t, b.deployAsFunction(t).userSource)
		})
	}
}

// compiledBackend is the folder of a backend written in a compiled language.
type compiledBackend struct {
	language string
	// files are the files of the folder, relative to it. Each folder holds every
	// directory a rule for an installed tree could reach for: a vendor/, which Go
	// and Rust both use, a node_modules/ and a Rust target/.
	files []string
}

var compiledBackends = []compiledBackend{
	{
		language: "go",
		files:    []string{"main.go", "go.mod", "go.sum", "vendor/modules.txt", "vendor/mod/x.go", "node_modules/tool/index.js"},
	},
	{
		// The label older records and released binaries still use for Go.
		language: "native",
		files:    []string{"main.go", "go.mod", "go.sum", "vendor/modules.txt", "vendor/mod/x.go", "node_modules/tool/index.js"},
	},
	{
		language: "rust",
		files:    []string{"Cargo.toml", "Cargo.lock", "src/main.rs", "vendor/crate/lib.rs", "target/debug/x", "node_modules/tool/index.js"},
	},
}

// A backend written in a compiled language is archived whole: the notion of an
// installed tree that is left out belongs to the interpreted languages, so a Go
// backend's vendor/ and a Rust backend's target/ are archived with the rest.
// Hidden entries are the only ones left out, as they are in every archive.
func TestUserSourceArchive_ACompiledBackendKeepsEverything(t *testing.T) {
	for _, b := range compiledBackends {
		t.Run(b.language, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range b.files {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(f)), "x\n")
			}
			writeFile(t, filepath.Join(dir, ".env"), "SECRET=1\n")
			writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")

			got := userSourceEntries(t, dir, b.language)

			if want := withParents(b.files); !slices.Equal(got, want) {
				t.Errorf("the archive of a %s backend is not the folder as it is:\n got  %v\n want %v", b.language, got, want)
			}
		})
	}
}

// What is left out of an interpreted backend's archive is a directory: the
// installed tree is one. A file that happens to be named node_modules or vendor
// is source, and is archived.
func TestUserSourceArchive_AFileNamedLikeTheTreeIsSource(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			dir := t.TempDir()
			b.writeSource(t, dir)
			for _, name := range []string{"node_modules", "vendor"} {
				writeFile(t, filepath.Join(dir, name), "a file, not a directory\n")
			}

			entries := userSourceEntries(t, dir, b.language)

			for _, name := range []string{"node_modules", "vendor"} {
				if !slices.Contains(entries, name) {
					t.Errorf("a file named %s is source and must be archived; the archive holds %v", name, entries)
				}
			}
		})
	}
}

// The archive that runs on the slice is the staged tree, installed dependencies
// included: a deploy that brings its own node_modules or vendor replaces the
// slice's, and only a deploy that brings none leaves them as they were.
func TestInterpretedBackendDeploy_DeployArchiveStillCarriesTheInstalledTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			b.checkDeployArchive(t, b.deployAsBackend(t).deploy)
		})
	}
}

// The one-function build path ships the same.
func TestSingleFunctionDeploy_DeployArchiveStillCarriesTheInstalledTree(t *testing.T) {
	for _, b := range interpretedBackends {
		t.Run(b.language, func(t *testing.T) {
			b.checkDeployArchive(t, b.deployAsFunction(t).deploy)
		})
	}
}

// createTarGz is what every deploy path packs its staged tree with, so what it
// archives by default is what runs on the slice: every non-hidden entry,
// node_modules/ and vendor/ at the root and below included. A rule that leaves
// the installed tree out of the user-source archive must not be made in it.
func TestDeployArchive_StillCarriesTheInstalledTree(t *testing.T) {
	stage := t.TempDir()
	files := []string{
		"app.js", "handlers.js", "package.json", "package-lock.json",
		"node_modules/@ondrift/sdk/package.json", "node_modules/lodash/index.js",
		"vendor/drift.py", "vendor/php/drift.php", "vendor/bundle/bundler/setup.rb",
		"src/vendor/lib.js",
	}
	for _, f := range files {
		writeFile(t, filepath.Join(stage, filepath.FromSlash(f)), "x\n")
	}
	archive := filepath.Join(t.TempDir(), "deploy.tar.gz")

	if err := createTarGz(stage, archive); err != nil {
		t.Fatalf("createTarGz: %v", err)
	}

	if got, want := archiveEntries(t, archive), withParents(files); !slices.Equal(got, want) {
		t.Errorf("the deploy archive is not the staged tree:\n got  %v\n want %v", got, want)
	}
}
