// run_go.go — Go language path for the local dev runner.
// Compiles `go build -o app ./...` from the user's workspace
// (with the embedded SDK vendored into a side directory and a
// `replace` directive injected into a copy of go.mod), then
// spawns the binary as the dev process. Re-runs on every save.
package atomic_cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
)

// generateGo writes the dev server's main.go around the handler, read the way
// the deploy reads it: the body type its declaration names, and the request's
// context passed first when its first parameter is a context.Context.
func (r *devRunner) generateGo() error {
	return renderGoServer(r.workDir, r.handler, r.method)
}

// renderGoServer writes the dev server's main.go for funcName into dir, whose
// Go sources declare it.
func renderGoServer(dir, funcName, method string) error {
	bodyType, takesContext := atomic_cmd.GoHandlerShape(dir, funcName)
	ctxArg := ""
	if takesContext {
		ctxArg = "ctx, "
	}
	replacer := strings.NewReplacer(
		"{{FUNC}}", funcName,
		"{{BODYTYPE}}", bodyType,
		"{{CTXARG}}", ctxArg,
	)
	var code string
	switch method {
	case "post", "put", "delete", "patch":
		code = replacer.Replace(defaultGolangServerPost)
	default:
		code = replacer.Replace(defaultGolangServerGet)
	}

	return os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600)
}

func (r *devRunner) buildAndRunGo() error {
	// Pull the published SDK at its latest tag — root module named
	// explicitly to dodge the legacy nested-module pseudo-versions (see
	// atomic_common.DriftGoModule). No version pinned.
	if err := runCmd(r.workDir, "go", "get", atomic_common.DriftGoModule+"@latest"); err != nil {
		log.Printf("go get drift sdk warning: %v", err)
	}
	if err := runCmd(r.workDir, "go", "mod", "tidy"); err != nil {
		// Not fatal; the project may already be tidy, but log it.
		log.Printf("go mod tidy warning: %v", err)
	}

	// Build for the local OS/arch (we're running locally)
	buildArgs := []string{"build", "-o", "app"}
	if err := runCmd(r.workDir, "go", buildArgs...); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// Stop any existing process
	r.stopProcess()

	// Start the process
	cmd := exec.Command(filepath.Join(r.workDir, "app")) // #nosec G204 — path is a controlled temp workspace
	cmd.Dir = r.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = r.processEnv()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start app: %w", err)
	}

	r.procLock.Lock()
	r.proc = cmd
	r.procLock.Unlock()

	go func() {
		_ = cmd.Wait()
	}()

	fmt.Printf("✅ Server started (PID %d)\n", cmd.Process.Pid)
	return nil
}
