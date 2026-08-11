// build_node.go — Node.js build path. Stages .js files into a temp
// directory, generates the wrapper, and runs `npm install` (when a
// package.json is present) so the user's declared dependencies — the
// Drift SDK among them — resolve into node_modules. The CLI is
// SDK-agnostic: it installs whatever the manifest declares, never an
// injected copy. The install runs in a node image under the SLICE's
// --platform, so platform-specific optional deps (sharp, argon2, …)
// resolve to the binaries the runner can actually execute.
// Returns a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
)

// c names the callable and the file declaring it, both resolved from the
// Driftfile entry this function is being built for.
func buildNode(absFolder, method, name string, c atomic_common.Callable) (string, error) {
	funcName := c.Handler
	sourceModule := strings.TrimSuffix(filepath.Base(c.SourceFile), ".js")

	// Create a staging directory.
	stageDir, err := stageTempDir("drift-node-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Copy all .js files from the function directory.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(absFolder, e.Name())) // #nosec G304 -- path is built from a CLI-validated argument or a regex-validated name plus a controlled base directory; never untrusted input.
		if err != nil {
			return "", fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, e.Name()), data, 0o644); err != nil { // #nosec G306 G703 -- the path is the CLI's stageDir on the user's machine; mode 0644 is intentional for a build-time artefact.
			return "", fmt.Errorf("write %s: %w", e.Name(), err)
		}
	}

	// Generate wrapper app.js (requires('@ondrift/sdk')).
	if err := generateNodeWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Install the user's declared dependencies (the Drift SDK is whatever
	// the function's package.json declares) when a package.json is present.
	// The CLI is SDK-agnostic — it installs the manifest.
	if err := installNodeDeps(absFolder, stageDir); err != nil {
		return "", err
	}

	// Create tar.gz archive in a unique temp file to avoid races when
	// multiple CLI instances deploy functions with the same name concurrently.
	archiveFile, err := os.CreateTemp("", fmt.Sprintf("drift-node-%s-*.tar.gz", safeTmpName(name)))
	if err != nil {
		return "", fmt.Errorf("create archive temp: %w", err)
	}
	archivePath := archiveFile.Name()
	archiveFile.Close()                                        // #nosec G104 -- best-effort close of the temp handle; createTarGz re-creates the file
	if err := createTarGz(stageDir, archivePath); err != nil { // #nosec G104
		return "", fmt.Errorf("create archive: %w", err)
	}

	return archivePath, nil
}
