// run_ruby.go — Ruby path for the local dev runner. Generates the
// `app.rb` wrapper. Dispatched via `ruby app.rb` by the shared
// runInterpreted helper.
package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"
)

func (r *devRunner) generateRuby() error {
	funcName := r.handler
	sourceModule := strings.TrimSuffix(r.sourceFile, ".rb")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostRuby
	default:
		tmpl = wrapperGetRuby
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	// SDK comes from `bundle install` (git gem) in installDeps.
	return os.WriteFile(filepath.Join(r.workDir, "app.rb"), []byte(code), 0o644) // #nosec G306 -- build-time artefact on the user's machine
}
