// new.go — `drift atomic new [name]`. Scaffolds a new Atomic function in
// any of the six supported languages, for an HTTP or a queue trigger, with
// the current handler signature the deploy/run wrappers expect:
//
//	GET   handler(req)        -> (status, message, payload)
//	POST… handler(body, req)  -> (status, message, payload)   (also queue)
//	Go is the exception: it returns a 4th `headers` value and, for the
//	body shape, defines a RequestBody struct the wrapper unmarshals into.
//
// Flags make it fully non-interactive (`-l python -m post`); anything not
// supplied is asked for with a survey prompt. The function's manifest
// declares the SDK unversioned (the CLI stays SDK-agnostic); `drift atomic
// fetch`/`run`/`deploy` resolve it.
package atomic_cmd_new

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
	"github.com/ondrift/cloud/cli/common"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
)

//go:embed languages/golang_get.txt
var tplGoGet string

//go:embed languages/golang_post.txt
var tplGoPost string

//go:embed languages/python_get.txt
var tplPyGet string

//go:embed languages/python_post.txt
var tplPyPost string

//go:embed languages/node_get.txt
var tplNodeGet string

//go:embed languages/node_post.txt
var tplNodePost string

//go:embed languages/ruby_get.txt
var tplRubyGet string

//go:embed languages/ruby_post.txt
var tplRubyPost string

//go:embed languages/php_get.txt
var tplPhpGet string

//go:embed languages/php_post.txt
var tplPhpPost string

//go:embed languages/rust_get.txt
var tplRustGet string

//go:embed languages/rust_post.txt
var tplRustPost string

// name/route/queue validation — mirrors the operator + runner regex so a
// scaffolded function is always deployable.
var nameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

var langExt = map[string]string{
	"go": "go", "python": "py", "node": "js", "ruby": "rb", "php": "php", "rust": "rs",
}

// AuthOptions is the set of HTTP gates a scaffolded function can declare. It is
// what the interactive `Auth:` prompt offers and what the -a/--auth help names,
// so the menu and the help cannot drift apart from each other.
//
// They can still drift from the enforcement, which is ValidateAuth, and that is
// the failure worth guarding: a menu entry its own validation rejects is a dead
// end reached by using the command exactly as designed. A test in package main
// holds both and checks every offered option against the validation.
//
// The set is two because two is what the platform implements: the Driftfile
// schema's auth enum and the slice runtime's gate name the same pair, and a
// third value here would scaffold a function nothing can serve.
var AuthOptions = []string{"none", "apikey"}

// ValidateAuth reports whether an auth mode can actually be enforced. Separate
// from AuthOptions on purpose: this also judges what arrives on -a/--auth,
// which is any string at all.
func ValidateAuth(auth string) error {
	switch auth {
	case "none", "apikey":
		return nil
	default:
		return fmt.Errorf("invalid auth %q (none|apikey)", auth)
	}
}

// New is the `drift atomic new` command.
func New() *cobra.Command {
	var lang, method, queue, auth, backend string
	c := &cobra.Command{
		Use:     "new [name]",
		Short:   "Add a function to a backend",
		GroupID: "development",
		Args:    cobra.MaximumNArgs(1),
		Example: "  drift atomic new\n" +
			"  drift atomic new send-email -l python -m post\n" +
			"  drift atomic new process-jobs -l go -q jobs\n" +
			"  drift atomic new train -l python -m post --backend ml",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runNew(name, lang, method, queue, auth, backend)
		},
	}
	c.Flags().StringVarP(&lang, "lang", "l", "", "language: go|python|node|ruby|php|rust")
	c.Flags().StringVarP(&method, "method", "m", "", "HTTP method: get|post|put|delete|patch")
	c.Flags().StringVarP(&queue, "queue", "q", "", "queue name (creates a queue-triggered function)")
	c.Flags().StringVarP(&auth, "auth", "a", "", "auth for HTTP: "+strings.Join(AuthOptions, "|")+" (default none)")
	c.Flags().StringVarP(&backend, "backend", "e", "", "backend to add it to (default: the flat top-level backend)")
	return c
}

func runNew(name, lang, method, queue, auth, backend string) error {
	// ---- name ----
	if name == "" {
		if err := survey.AskOne(&survey.Input{Message: "Function name (e.g. send-email):"}, &name,
			survey.WithValidator(survey.Required)); err != nil {
			return err
		}
	}
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q — use lowercase letters, digits and hyphens (e.g. send-email)", name)
	}

	// ---- language ----
	if lang == "" {
		if err := survey.AskOne(&survey.Select{
			Message: "Language:",
			Options: []string{"go", "python", "node", "ruby", "php", "rust"},
			VimMode: true,
		}, &lang); err != nil {
			return err
		}
	}
	lang = strings.ToLower(lang)
	if _, ok := langExt[lang]; !ok {
		return fmt.Errorf("unsupported language %q (go|python|node|ruby|php|rust)", lang)
	}

	// ---- trigger ----
	if method != "" && queue != "" {
		return fmt.Errorf("choose either --method (HTTP) or --queue, not both")
	}
	isQueue := queue != ""
	if method == "" && queue == "" {
		trig := "HTTP"
		if err := survey.AskOne(&survey.Select{
			Message: "Trigger:", Options: []string{"HTTP", "Queue"}, Default: "HTTP", VimMode: true,
		}, &trig); err != nil {
			return err
		}
		if trig == "Queue" {
			isQueue = true
			q := name
			if err := survey.AskOne(&survey.Input{Message: "Queue name:", Default: name}, &q); err != nil {
				return err
			}
			queue = strings.TrimSpace(q)
			if queue == "" {
				queue = name
			}
		} else {
			if err := survey.AskOne(&survey.Select{
				Message: "HTTP method:",
				Options: []string{"POST", "GET", "PUT", "DELETE", "PATCH"},
				VimMode: true,
			}, &method); err != nil {
				return err
			}
			if auth == "" {
				if err := survey.AskOne(&survey.Select{
					Message: "Auth:", Options: AuthOptions, VimMode: true,
				}, &auth); err != nil {
					return err
				}
			}
		}
	}

	// ---- resolve the trigger and the handler shape ----
	//
	// There is no `declaredName` any more. The identity used to be assembled here
	// as `method:route` — the retired single-key spelling — purely so it could be
	// printed for the user to paste. The current grammar is the pair itself, so
	// the two halves are printed as they already are.
	var funcMethod, shape string
	if isQueue {
		if !nameRe.MatchString(queue) {
			return fmt.Errorf("invalid queue name %q", queue)
		}
		funcMethod = "queue"
		shape = "post" // queue messages carry a body: handler(body, req)
		auth = "none"  // a queue handler has no URL, so there is no gate to set
	} else {
		method = strings.ToLower(method)
		switch method {
		case "get", "post", "put", "delete", "patch":
		default:
			return fmt.Errorf("invalid method %q (get|post|put|delete|patch)", method)
		}
		if auth == "" {
			auth = "none"
		}
		if err := ValidateAuth(auth); err != nil {
			return err
		}
		funcMethod = method
		if method == "get" {
			shape = "get"
		} else {
			shape = "post"
		}
	}

	// ---- assemble + write ----
	// The source carries no Drift-specific marking. What this function is — its
	// trigger, its gate, the memory it books — is declared in the Driftfile,
	// and the handler name is the only thing binding the two.
	funcName := HandlerName(funcMethod, name, lang)
	source := strings.NewReplacer("{{FUNC}}", funcName).Replace(sourceTemplate(lang, shape))

	// ---- resolve the target backend + drop a flat handler file into it ----
	// A backend is a single-language backend: a folder of flat source files
	// sharing one manifest. The default backend is atomic/ itself; --backend
	// targets a named subfolder. Adding a function never creates a per-function
	// folder — it's just another file alongside its siblings.
	atomicDir := resolveAtomicDir()
	backendDir, backendID := atomicDir, defaultBackend
	if backend != "" {
		if !nameRe.MatchString(backend) {
			return fmt.Errorf("invalid backend name %q — use lowercase letters, digits and hyphens", backend)
		}
		backendDir, backendID = filepath.Join(atomicDir, backend), backend
	}

	// One language per backend — a different language is, by definition, a
	// different backend.
	if existing := backendLang(backendDir); existing != "" && existing != lang {
		return fmt.Errorf("the %s backend is %s — a different language is a different backend; "+
			"pass --backend <name> to start a new one", backendID, existing)
	}
	if err := os.MkdirAll(backendDir, 0o750); err != nil {
		return fmt.Errorf("create backend dir: %w", err)
	}

	srcFile := filepath.Join(backendDir, sourceBase(lang, name)+"."+langExt[lang])
	if exists(srcFile) {
		return fmt.Errorf("%s already exists", srcFile)
	}
	if err := os.WriteFile(srcFile, []byte(source), 0o600); err != nil {
		return fmt.Errorf("write source: %w", err)
	}

	// Manifest / .env / .gitignore: one per backend, written only when absent so
	// adding a function never clobbers an existing backend's shared files.
	created := []string{srcFile}
	manifestName, manifestBody := Manifest(lang, backendID)
	if mp := filepath.Join(backendDir, manifestName); !exists(mp) {
		if err := os.WriteFile(mp, []byte(manifestBody), 0o600); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}
		created = append(created, mp)
	}
	if ep := filepath.Join(backendDir, ".env"); !exists(ep) {
		_ = os.WriteFile(ep, []byte(dotEnv), 0o600) // #nosec G306 -- local-dev secrets file
	}
	if gp := filepath.Join(atomicDir, ".gitignore"); !exists(gp) {
		_ = os.WriteFile(gp, []byte(gitignore), 0o600) // #nosec G306
	}

	trigger := "HTTP " + strings.ToUpper(method)
	if isQueue {
		trigger = "queue " + queue
	}
	fmt.Printf("✅ Added %s to the %s backend  (%s, %s)\n", filepath.Base(srcFile), backendID, lang, trigger)
	for _, f := range created {
		fmt.Printf("   %s\n", f)
	}

	// The function does not exist until the Driftfile says so: the file is what
	// the slice is contracted to run, and a function nobody declared is one the
	// platform cannot size, price or admit work against.
	//
	// ALREADY DECLARED IS A DIFFERENT MESSAGE, and this is the case `drift file
	// new` creates on purpose. That scaffolder writes a Driftfile declaring
	// `function "GET /hello"` and prints the command that produces its source —
	// which is this one. Telling the user who followed that hint to "declare it
	// in your Driftfile" sends them to paste a duplicate of a block already
	// three lines above, and a second function on one trigger is a compile error
	// rather than a no-op.
	//
	// So the two commands point at each other rather than each assuming the
	// other half of the round trip: file new names this command, and this
	// command recognises the block that command wrote.
	if declaredRoute(funcMethod, name, queue, isQueue) {
		fmt.Printf("\nAlready declared in your Driftfile — nothing to add.\n")
		printNext()
		return nil
	}

	route := name
	if isQueue {
		route = queue
	}
	snippet := declarationSnippet(route, funcMethod, funcName, auth, backendID)
	fmt.Printf("\n%s\n\n", snippet.intro)
	for _, line := range strings.Split(strings.TrimSuffix(snippet.body, "\n"), "\n") {
		fmt.Printf("    %s\n", line)
	}
	printNext()
	return nil
}

// printNext names what follows declaring a function.
func printNext() {
	fmt.Printf("\nThen:\n")
	fmt.Printf("\tdrift plan            # what applying it creates, and what it costs\n")
	fmt.Printf("\tdrift apply           # ship it\n")
	fmt.Printf("\tdrift size            # book what it has actually cost, once it has served traffic\n")
}

// snippet is the Driftfile declaration `drift atomic new` prints: the sentence
// saying where it goes, and the HCL to paste there.
type snippet struct {
	intro string
	body  string
}

// snippetMemory is what a new function books until it is measured: above the
// floor a compiled language needs, so the snippet is not refused by the
// language it was scaffolded in. `drift size` replaces it with what the slice
// measured.
const snippetMemory = "32MiB"

// declarationSnippet is the block declaring a new Function, inside the backend it
// was added to. A Function is declared inside the backend its handler lives in,
// so the snippet is that backend's block holding the function.
func declarationSnippet(route, method, handler, auth, backend string) snippet {
	if backend == "" {
		backend = defaultBackend
	}
	var b strings.Builder
	b.WriteString("atomic {\n")
	fmt.Fprintf(&b, "  backend %q {\n", backend)
	fmt.Fprintf(&b, "    function %q {\n", functionLabel(method, route))
	fmt.Fprintf(&b, "      handler = %q\n", handler)
	fmt.Fprintf(&b, "      memory  = %q\n", snippetMemory)
	if auth != "" && auth != "none" {
		b.WriteString("      auth {\n")
		fmt.Fprintf(&b, "        api_key = env(%q)\n", apiKeyVariable(route))
		b.WriteString("      }\n")
	}
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return snippet{
		intro: fmt.Sprintf("Declare it in your Driftfile, inside backend %q of the atomic block (add the "+
			"blocks if they are not there yet, or just the function block if they are):", backend),
		body: b.String(),
	}
}

// apiKeyVariable is the environment variable a new function's API key is read
// from: its route in capitals, `admin/reset` → ADMIN_RESET_API_KEY.
func apiKeyVariable(route string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(route) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.TrimSuffix(b.String(), "_") + "_API_KEY"
}

// HandlerName is the callable the scaffolder writes and names in the Driftfile
// block it prints. It is a STARTING POINT — the Driftfile binds the two, so
// renaming the function only means editing `handler` beside it.
//
// Exported because `drift file new` scaffolds a Driftfile declaring a handler
// and prints the `drift atomic new` command that writes it. The two must name
// the SAME callable, and the only way to guarantee that is for the Driftfile
// side to derive it here rather than restate it. A literal "GetHello" in the
// other file agrees with this one until the day the derivation changes.
func HandlerName(method, route, lang string) string {
	route = strings.ReplaceAll(route, ":", "")
	route = strings.ReplaceAll(route, "/", "-")

	switch lang {
	case "python", "ruby", "php", "rust":
		return strings.ReplaceAll(method+"_"+route, "-", "_")
	case "node":
		parts := strings.Split(method+"-"+route, "-")
		for i := 1; i < len(parts); i++ {
			parts[i] = common.CapitalizeFirst(strings.ToLower(parts[i]))
		}
		return strings.Join(parts, "")
	default: // Go: PascalCase, and exported so the entry point can import it
		out := common.CapitalizeFirst(strings.ToLower(method))
		for _, seg := range strings.Split(route, "-") {
			out += common.CapitalizeFirst(strings.ToLower(seg))
		}
		return out
	}
}

// defaultBackend is the implicit flat backend (atomic/*.<lang>) — mirrors the
// deploy package's DefaultBackendName without importing it.
const defaultBackend = "default"

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// resolveAtomicDir picks where to drop the new function, relative to the working
// directory: "." when we're already inside an atomic/ folder, else "atomic"
// (created on first use — a fresh project just gets its first function).
func resolveAtomicDir() string {
	if cwd, err := os.Getwd(); err == nil && filepath.Base(cwd) == "atomic" {
		return "."
	}
	return "atomic"
}

// backendLang reports the single language of the source already in dir (an
// backend is one language), or "" if it holds none yet.
func backendLang(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if l := atomic_common.LanguageFromExt(filepath.Ext(e.Name())); l != "" {
			return l
		}
	}
	return ""
}

// commentPrefix returns the line-comment marker for the language.
func commentPrefix(lang string) string {
	switch lang {
	case "python", "ruby":
		return "#"
	default:
		return "//"
	}
}

// sourceBase is the source file's base name (no extension). Python and Rust
// turn the file into a module/mod, which can't contain hyphens, so they get
// an underscore-safe name; the others use the function name verbatim.
func sourceBase(lang, name string) string {
	if lang == "python" || lang == "rust" {
		return strings.ReplaceAll(name, "-", "_")
	}
	return name
}

func sourceTemplate(lang, shape string) string {
	get := shape == "get"
	switch lang {
	case "go":
		if get {
			return tplGoGet
		}
		return tplGoPost
	case "python":
		if get {
			return tplPyGet
		}
		return tplPyPost
	case "node":
		if get {
			return tplNodeGet
		}
		return tplNodePost
	case "ruby":
		if get {
			return tplRubyGet
		}
		return tplRubyPost
	case "php":
		if get {
			return tplPhpGet
		}
		return tplPhpPost
	case "rust":
		if get {
			return tplRustGet
		}
		return tplRustPost
	}
	return ""
}

// Manifest returns the (filename, contents) of the function's dependency
// manifest. The SDK is declared unversioned so builds always track latest;
// Go's go.mod stays bare (the build/fetch path runs `go get …@latest`).
//
// Exported so the build path can be tested against what this actually writes
// rather than against a copy of it. The Rust build decides whether to supply a
// musl C compiler by reading the Cargo.toml, and a test that restated the
// scaffold instead of reading it would keep passing after the two diverged —
// which is the only way that decision can go wrong.
func Manifest(lang, name string) (string, string) {
	switch lang {
	case "go":
		return "go.mod", fmt.Sprintf("module atomic/%s\n\ngo 1.26.2\n", name)
	// THE REPOSITORY IS `ondrift/cloud`. The SDK is a directory inside it, so
	// each language reaches it by that language's own means — pip's
	// `#subdirectory=`, bundler's `glob:`, composer's and npm's root manifests,
	// and cargo's lookup of a named package anywhere in the clone.
	//
	// Three of these named `github.com/ondrift/cloud/sdk`, which is not an
	// address: a GitHub repository is owner/repo, and that is three segments. It
	// 404s, so a scaffolded Python, Ruby or PHP project shipped a dependency
	// nothing could install. The fourth named `ondrift/sdk`, a repository that no
	// longer exists.
	//
	// RUST CARRIED THE SAME BAD ADDRESS THROUGH THAT PASS, in rustCargoToml
	// below and in both cargo_template.toml copies, so every scaffolded Rust
	// function failed to build. Cargo reports it as `unexpected http status
	// code: 404` from inside a clone, naming neither the URL nor the dependency,
	// which is why it read as a toolchain problem for as long as it did.
	case "python":
		return "requirements.txt", "drift-sdk @ git+https://github.com/ondrift/cloud.git#subdirectory=sdk/python\n"
	case "node":
		// No `#semver:*`: npm matches that against semver-shaped tags, and this
		// repository namespaces its releases `sdk/v0.9.0` beside `cli/v0.61.0`,
		// none of which parse as one. It would silently resolve to the default
		// branch anyway, so the branch is what it asks for.
		return "package.json", fmt.Sprintf("{\n"+
			"  \"name\": \"atomic-%s\",\n"+
			"  \"version\": \"1.0.0\",\n"+
			"  \"private\": true,\n"+
			"  \"dependencies\": {\n"+
			"    \"@ondrift/sdk\": \"github:ondrift/cloud\"\n"+
			"  }\n}\n", name)
	case "ruby":
		return "Gemfile", "source \"https://rubygems.org\"\n\n" +
			"gem \"drift-sdk\", git: \"https://github.com/ondrift/cloud\", glob: \"sdk/ruby/*.gemspec\"\n"
	case "php":
		return "composer.json", "{\n" +
			"  \"repositories\": [\n" +
			"    { \"type\": \"vcs\", \"url\": \"https://github.com/ondrift/cloud\" }\n" +
			"  ],\n" +
			"  \"require\": {\n" +
			"    \"ondrift/sdk\": \"*\"\n" +
			"  }\n}\n"
	case "rust":
		return "Cargo.toml", rustCargoToml
	}
	return "", ""
}

const rustCargoToml = `[package]
name = "atomic-function"
version = "0.1.0"
edition = "2021"

[[bin]]
name = "atomic-function"
path = "src/main.rs"

[dependencies]
# Calling an https:// URL from http_request()? Add the "tls" feature:
#
#   drift-sdk = { git = "https://github.com/ondrift/cloud", features = ["tls"] }
#
# It is off here because it pulls in ring, which is C and assembly: the build
# then needs a musl C compiler, and it costs minutes rather than seconds. Drift
# installs that compiler for you the moment this line says "tls" — you do not
# need to find one — so the only cost of turning it on is the build time.
#
# Plain http:// to the loopback Backbone needs none of this, which is why a new
# function starts without it. Without the feature an https:// call comes back as
# status 0 with the reason in the body, rather than failing at build time.
drift-sdk = { git = "https://github.com/ondrift/cloud" }
serde_json = "1"

[profile.release]
opt-level = "z"
lto = true
strip = true
panic = "abort"
`

const dotEnv = "# Secrets for local development — loaded automatically by 'drift atomic run'.\n" +
	"# In production use 'drift backbone secret set KEY=VALUE' and read them via the SDK.\n" +
	"#\n# Example:\n# API_KEY=your-api-key-here\n"

const gitignore = ".env\nnode_modules/\nvendor/\ntarget/\n"
