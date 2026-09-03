// build_go.go — Go-language build path for `drift atomic deploy`.
// Compiles the user's Go source and produces a static linux binary at
// `<dir>/app`. The Drift SDK is pulled at its latest tag via `go get`
// (the root module is named to dodge the legacy nested-module pseudo-
// versions — see atomic_common.DriftGoModule); no version is ever pinned.
// Other deps come from the user's go.mod. The build runs in a private
// tempdir copy so the user's working tree is never modified.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
)

// buildGo builds one function on its own: stage the folder, then compile a
// binary bound to c.Handler. The element path (buildGoElementStage +
// buildGoEntrypoint) is the multi-function generalization, which stages once
// and links per function.
func buildGo(absFolder, method, name string, c atomic_common.Callable) (string, error) {
	funcName := c.Handler
	buildDir, err := buildGoElementStage(absFolder, name)
	if err != nil {
		return "", err
	}
	// Deliberately no RemoveAll(buildDir) on success: the returned binary
	// lives inside it and the caller's `defer os.Remove` cleans the binary
	// after upload; the empty parent leaks until the CLI process exits.
	bin, err := buildGoEntrypoint(buildDir, funcName, method, "app")
	if err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", err
	}
	return bin, nil
}

// buildGoElementStage copies a Go Element's package into a fresh host-side
// tempdir and resolves its dependencies ONCE (go mod init/get/tidy). The
// caller then compiles one binary per function into it via buildGoEntrypoint
// and is responsible for RemoveAll(buildDir). Each call gets its own tempdir,
// so parallel deploys never race; the user's source is never modified.
func buildGoElementStage(srcDir, name string) (string, error) {
	buildDir, err := stageTempDir("drift-go-build-")
	if err != nil {
		return "", fmt.Errorf("create build tempdir: %w", err)
	}
	if err := copyGoSourceFiles(srcDir, buildDir); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("copy build context: %w", err)
	}

	// Ensure a module exists (user functions may ship without a go.mod).
	if _, statErr := os.Stat(filepath.Join(buildDir, "go.mod")); statErr != nil {
		if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"mod", "init", "atomic/" + safeTmpName(name)}}); err != nil {
			os.RemoveAll(buildDir) // #nosec G104
			return "", fmt.Errorf("go mod init error: %w\n%s", err, string(out))
		}
	}

	// Pull the published SDK at its latest tag. The root module must be named
	// explicitly (see atomic_common.DriftGoModule) to dodge the legacy nested
	// `…/sdk/go` pseudo-version module. No version is pinned — @latest tracks
	// new tags, so a new SDK release never touches the CLI.
	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"get", atomic_common.DriftGoModule + "@latest"}}); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go get %s@latest error: %w\n%s", atomic_common.DriftGoModule, err, string(out))
	}

	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"mod", "tidy"}}); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go mod tidy error: %w\n%s", err, string(out))
	}

	return buildDir, nil
}

// buildGoEntrypoint generates a main bound to funcName in the already-staged
// buildDir and compiles one static linux binary named binBase, returning its
// path. The staged (tidied) package is reused, so per-function cost is just
// the compile + link — Go's build cache makes every call after the first cheap.
func buildGoEntrypoint(buildDir, funcName, method, binBase string) (string, error) {
	if err := generateMain(buildDir, funcName, method); err != nil {
		return "", fmt.Errorf("failed to generate main.go: %w", err)
	}
	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"build", "-o", binBase}, env: map[string]string{"GOOS": "linux", "CGO_ENABLED": "0"}}); err != nil {
		return "", fmt.Errorf("go build error (%s): %w\n%s%s", funcName, err, string(out),
			handlerShapeHint(funcName, string(out)))
	}
	return filepath.Join(buildDir, binBase), nil
}

// buildGoEntrypointIsolated compiles one function's entrypoint in its OWN fresh
// build dir (a copy of the staged package + a main bound to funcName), so it is
// safe to call CONCURRENTLY for an element's functions. They share the module
// cache (deps already resolved by buildGoElementStage) and Go's content-
// addressed build cache (the package compiles once; the rest only re-link), so
// parallelism turns the per-function link into the only marginal cost. Returns
// the binary path and the fn dir; the caller must RemoveAll(fnDir).
func buildGoEntrypointIsolated(stageDir, funcName, method, binBase string) (bin, fnDir string, err error) {
	fnDir, err = stageTempDir("drift-go-fn-")
	if err != nil {
		return "", "", fmt.Errorf("create fn build dir: %w", err)
	}
	if err = copyGoSourceFiles(stageDir, fnDir); err != nil {
		os.RemoveAll(fnDir) // #nosec G104
		return "", "", fmt.Errorf("copy staged package: %w", err)
	}
	bin, err = buildGoEntrypoint(fnDir, funcName, method, binBase)
	if err != nil {
		os.RemoveAll(fnDir) // #nosec G104
		return "", "", err
	}
	return bin, fnDir, nil
}

// handlerShapeHint turns the compiler's account of a handler-signature mismatch
// into something the author can act on, or returns "" when that is not what
// failed.
//
// THE ERROR ARRIVES IN A FILE NOBODY WROTE. The entry point is generated from a
// template and calls the handler in the shape a deployed function has —
// `status, message, payload, headers := Name(req)` — so a handler of any other
// shape fails as `assignment mismatch: 4 variables but Name returns 1 value`, at
// a line in a `main.go` the author has never seen and cannot open.
//
// The shape it collides with most is the SDK's OWN: `func(drift.Request)
// drift.Response` is what `drift.Run` takes, it is public documented surface,
// and it is what every handler in Drift's platform repo is written as. Someone
// arriving from `drift.Run` has no way to learn from that message that a second
// convention exists, let alone which one this path wants.
//
// Matching on the compiler text rather than on the source, because reading the
// user's signature is exactly what this package declines to do — `parse.go`
// finds a callable by name and says outright that nothing here reads intent out
// of source. This adds no parsing: it explains a failure that already happened.
func handlerShapeHint(funcName, buildOutput string) string {
	// The compiler's own wording for "this call returns a different number of
	// values than the caller destructures". Anything else is a real build error
	// in the user's code and must not be dressed up as a signature problem.
	if !strings.Contains(buildOutput, "assignment mismatch") || !strings.Contains(buildOutput, funcName) {
		return ""
	}
	return fmt.Sprintf(`
── the handler's shape ──────────────────────────────────────────────────────

%[1]s does not match what a deployed function is called with. A Go handler this
path can bind looks like one of:

    func %[1]s(req drift.Request) (int, string, any, map[string]string)
    func %[1]s(body map[string]any, req drift.Request) (int, string, any, map[string]string)

returning status, message, payload and headers.

IF YOURS RETURNS A drift.Response — the shape drift.Run takes — it is not wrong,
it is a different convention, and this path binds the one above. Wrap it and
name the wrapper in the Driftfile's `+"`handler:`"+`:

    func %[1]s(req drift.Request) (int, string, any, map[string]string) {
        r := yourHandler(req)
        return r.Status, r.Message, r.Payload, r.Headers
    }

The two carry the same four values, so the wrapper is the unpacking and nothing
else.
`, funcName)
}

// copyGoSourceFiles copies the top-level Go source files plus
// go.mod / go.sum from src to dst. Subdirectories are not copied —
// the build context for a single Atomic function is always flat
// (one Go package, one go.mod). Artefacts left behind by prior builds
// (app binary, main.go) live at the top level and are deliberately
// excluded so the tempdir starts clean.
func copyGoSourceFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read source dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Allowlist: only Go source + module files. Skips main.go,
		// app, and anything else a prior build may have left behind.
		if name == "main.go" || name == "app" {
			continue
		}
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" {
			continue
		}
		// #nosec G304 -- src is a CLI-validated absolute path; name comes from filesystem readdir, not user input
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
