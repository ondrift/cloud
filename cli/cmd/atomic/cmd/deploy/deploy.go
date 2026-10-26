// deploy.go — builds and uploads one Atomic function, for `drift apply`. Six
// per-language build paths share one wrapper-
// injection scheme + one multipart-upload tail:
//
//  1. Detect language from source files in the directory.
//  2. Generate the language-specific wrapper that bridges the runner's
//     JSON wire protocol to user code (the `generate*Wrapper` helpers
//     at the top of this file).
//  3. Build:
//     - Go     → `go build` to a static linux binary
//     - Rust   → `cargo build --target aarch64-unknown-linux-musl`
//     - Python → bundle source + dependencies (pip install --target)
//     - Node   → npm install --production
//     - Ruby   → tar source as-is (interpreted at runtime)
//     - PHP    → composer install --no-dev (if composer.json present)
//  4. Tar the build artefact, POST as multipart to /ops/atomic with
//     function metadata (name, route, secrets, triggers).
//
// A future refactor could split the per-language build paths into
// `build_go.go` / `build_python.go` / etc. for readability — someone
// reviewing "what does the CLI run on my machine when I deploy a Python
// function?" should be able to grep one file. It's mechanical
// (move-only, no logic changes).
package atomic_cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
	"github.com/ondrift/cloud/cli/common"
)

//go:embed default/server_post_native.txt
var defaultNativeServerPost string

//go:embed default/server_get_native.txt
var defaultNativeServerGet string

//go:embed default/server_post_mux.txt
var defaultMuxServerPost string

//go:embed default/server_get_mux.txt
var defaultMuxServerGet string

//go:embed default/field_native.txt
var defaultNativeField string

//go:embed default/wrapper_post_python.txt
var wrapperPostPython string

//go:embed default/wrapper_get_python.txt
var wrapperGetPython string

//go:embed default/wrapper_post_node.txt
var wrapperPostNode string

//go:embed default/wrapper_get_node.txt
var wrapperGetNode string

//go:embed default/wrapper_post_ruby.txt
var wrapperPostRuby string

//go:embed default/wrapper_get_ruby.txt
var wrapperGetRuby string

//go:embed default/wrapper_post_php.txt
var wrapperPostPHP string

//go:embed default/wrapper_get_php.txt
var wrapperGetPHP string

//go:embed default/wrapper_post_rust.txt
var wrapperPostRust string

//go:embed default/wrapper_get_rust.txt
var wrapperGetRust string

//go:embed default/cargo_template.toml
var cargoTemplate string

// safeTmpName turns a route path that may contain slashes or `:param`
// segments into a string safe for use as a tempfile/dir pattern.
// `os.CreateTemp` rejects path separators in its pattern argument.
func safeTmpName(name string) string {
	out := strings.ReplaceAll(name, "/", "__")
	out = strings.ReplaceAll(out, ":", "")
	return out
}

// goEntry is what one Go slot's generated main is rendered from: the facts of
// its declaration that decide the template and the call.
type goEntry struct {
	name, handler, method, stream string
	concurrent                    bool
}

// goEntryOf is the entry for one declared slot.
func goEntryOf(spec FunctionSpec) goEntry {
	method, _ := spec.Wire()
	return goEntry{
		name: spec.Name, handler: spec.Handler, method: method,
		stream: spec.Stream, concurrent: spec.IsConcurrent(),
	}
}

// generateMain writes the main.go that wraps the user's Go handler, chosen by
// pulseMainTemplate. For body-shaped triggers it binds `var body <T>` to the
// handler's actual body type (GoHandlerShape) rather than a hardcoded name, so
// several POST handlers can coexist in one element — each with its own body
// struct — without colliding on a shared `RequestBody`; and it passes the
// call's context to a handler whose first parameter is a context.Context.
//
// A Field gets an entry point of its own: no request is read, the handler runs
// until the slice asks it to stop, and the exit status says whether it stopped
// because it was asked to. See default/field_native.txt.
//
// A combination no template can serve is refused here, before anything
// compiles.
func generateMain(dir string, e goEntry, sdk goSDK) error {
	var code string
	if e.method == FieldMethod {
		code = strings.NewReplacer("{{FUNC}}", e.handler).Replace(defaultNativeField)
	} else {
		bodyType, takesContext := GoHandlerShape(dir, e.handler)
		tpl, err := pulseMainTemplate(e, takesContext, sdk)
		if err != nil {
			return err
		}
		ctxArg := ""
		if takesContext {
			ctxArg = "ctx, "
		}
		code = strings.NewReplacer("{{FUNC}}", e.handler, "{{BODYTYPE}}", bodyType, "{{CTXARG}}", ctxArg).Replace(tpl)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600); err != nil {
		return fmt.Errorf("failed to generate main.go: %w", err)
	}
	return nil
}

// isBodyMethod reports whether a Pulse of this method is handed a body: the
// methods whose handler takes `body T` before the request.
func isBodyMethod(method string) bool {
	switch method {
	case "post", "put", "delete", "patch", QueueMethod:
		return true
	}
	return false
}

// pulseMainTemplate chooses the template a Pulse's main is rendered from.
//
// A buffered Pulse is served by the SDK's genmain package (default/
// server_*_mux.txt): concurrently when it is declared concurrent and the
// slice asks for it, one call at a time otherwise, byte for byte as the native
// templates answer. Either way the protocol keeps the process's real stdout
// and fd 1 is pointed at stderr, so what a handler prints reaches the
// function's log instead of the line the slice reads as its response; and a
// handler taking a context.Context is given the call's.
//
// The native templates (default/server_*_native.txt) serve the rest:
//
//   - a streaming Pulse, whose stdout IS the stream it writes to the client;
//   - a build whose resolved SDK predates genmain (goSDK.genmain).
//
// Neither can call a handler with a context or serve calls concurrently, so a
// handler taking a context.Context, or a concurrent Pulse, is refused there
// rather than compiled into an error in a file the author never wrote.
func pulseMainTemplate(e goEntry, takesContext bool, sdk goSDK) (string, error) {
	native, mux := defaultNativeServerGet, defaultMuxServerGet
	if isBodyMethod(e.method) {
		native, mux = defaultNativeServerPost, defaultMuxServerPost
	}
	switch {
	case e.stream != "":
		if takesContext {
			return "", fmt.Errorf("pulse %q streams its reply (stream: %s), and its handler %s takes a context.Context: "+
				"a streaming Pulse writes its stream on stdout and is not given a context in this release. "+
				"Drop the context.Context parameter, or the stream", e.name, e.stream, e.handler)
		}
		return native, nil
	case !sdk.genmain:
		sdkName := "the Drift Go SDK this build resolved"
		if sdk.version != "" {
			sdkName += " (" + sdk.version + ")"
		}
		if e.concurrent {
			return "", fmt.Errorf("pulse %q is declared concurrent, and %s predates concurrent Pulses. "+
				"Deploy it again once a newer SDK is published, or set concurrent to false", e.name, sdkName)
		}
		if takesContext {
			return "", fmt.Errorf("pulse %q has a handler, %s, that takes a context.Context, and %s predates handlers "+
				"that take one. Deploy it again once a newer SDK is published, or drop the context.Context parameter",
				e.name, e.handler, sdkName)
		}
		return native, nil
	}
	return mux, nil
}

// GoHandlerShape reads funcName's signature from the Go sources in dir and
// reports its body type and whether its first parameter is a context.Context.
//
// The body type is the type of the first parameter after any context, e.g.
// `LoginBody` from `func PostLogin(body LoginBody, req drift.Request)` or from
// `func PostLogin(ctx context.Context, body LoginBody, req drift.Request)`. This
// lets each POST handler in an element declare its own body struct; the
// generated main unmarshals into exactly that type. It falls back to
// "RequestBody" (the historical name) when the signature can't be read, so
// single-function and legacy deploys are byte-for-byte unchanged.
//
// main.go is skipped: it is the generated file, or a user's own that the build
// leaves out.
func GoHandlerShape(dir, funcName string) (bodyType string, takesContext bool) {
	const fallback = "RequestBody"
	types := handlerParamTypes(dir, funcName)
	if len(types) > 0 && types[0] == "context.Context" {
		takesContext = true
		types = types[1:]
	}
	if len(types) > 0 && types[0] != "" {
		return types[0], takesContext
	}
	return fallback, takesContext
}

// handlerParamTypes returns the parameter types of funcName as its declaration
// in dir writes them, or nil when no file declares it. A parameter it cannot
// read is "". Read the same way the body type always was: the first file whose
// text declares `func funcName(`, up to the first closing parenthesis.
func handlerParamTypes(dir, funcName string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	decl := regexp.MustCompile(`func\s+` + regexp.QuoteMeta(funcName) + `\s*\(([^)]*)\)`)
	param := regexp.MustCompile(`^\s*[A-Za-z_]\w*\s+(\S.*?)\s*$`)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "main.go" || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- staged build dir, filesystem-sourced name
		if rerr != nil {
			continue
		}
		m := decl.FindSubmatch(data)
		if m == nil {
			continue
		}
		var types []string
		for _, p := range strings.Split(string(m[1]), ",") {
			t := ""
			if pm := param.FindStringSubmatch(p); pm != nil {
				t = strings.Join(strings.Fields(pm[1]), " ")
			}
			types = append(types, t)
		}
		return types
	}
	return nil
}

// generatePythonWrapper writes app.py that wraps the user's Python function.
func generatePythonWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostPython)
	default:
		code = replacer.Replace(wrapperGetPython)
	}
	return os.WriteFile(filepath.Join(dir, "app.py"), []byte(code), 0o600)
}

// generateNodeWrapper writes app.js that wraps the user's Node function.
func generateNodeWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostNode)
	default:
		code = replacer.Replace(wrapperGetNode)
	}
	return os.WriteFile(filepath.Join(dir, "app.js"), []byte(code), 0o600)
}

// generateRubyWrapper writes app.rb that wraps the user's Ruby function.
func generateRubyWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostRuby)
	default:
		code = replacer.Replace(wrapperGetRuby)
	}
	return os.WriteFile(filepath.Join(dir, "app.rb"), []byte(code), 0o600)
}

// generatePHPWrapper writes app.php that wraps the user's PHP function.
func generatePHPWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostPHP)
	default:
		code = replacer.Replace(wrapperGetPHP)
	}
	return os.WriteFile(filepath.Join(dir, "app.php"), []byte(code), 0o600)
}

// generateRustWrapper writes src/main.rs that wraps the user's Rust function.
func generateRustWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostRust)
	default:
		code = replacer.Replace(wrapperGetRust)
	}
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(srcDir, "main.rs"), []byte(code), 0o600)
}

// createTarGz creates a .tar.gz archive of the given directory, writing it to destPath.
// Only regular files and directories are included. Hidden files (.git etc) are skipped.
func createTarGz(srcDir, destPath string) error {
	f, err := os.Create(destPath) // #nosec G304 — CLI creates temp archive
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip hidden directories/files.
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
			// #nosec G122 -- snapshot/runner dir is operator-controlled (created by the slice/operator inside its own staging dirs); never user-writable, no symlink TOCTOU risk.
		}
		src, err := os.Open(path) // #nosec G122 G304 -- false-positive: see the cross-repo audit baseline; this site has been reviewed.
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}

// TriggerSpec is the minimal trigger definition declared in source comments.
type TriggerSpec struct {
	Type     string `json:"type"`               // "queue" | "webhook" | "schedule"
	Source   string `json:"source"`             // queue name or webhook path
	Schedule string `json:"schedule,omitempty"` // Go duration string e.g. "5m" (schedule triggers)
	PollMS   int    `json:"poll_ms,omitempty"`  // polling interval ms (queue triggers)
	MaxRetry int    `json:"max_retry,omitempty"`
	// Method tells the runner which HTTP method the function is registered
	// under, so the trigger registry's lookup matches. Empty defaults to
	// "POST" — a `// drift:trigger queue X` comment on a function that also
	// answers over HTTP. It is "queue" for a function the Driftfile names
	// `queue:<name>`: those register under a synthetic method that no HTTP
	// request can match, keeping them externally unreachable.
	Method string `json:"method,omitempty"`
}

// sourceFiles returns all source files (.go, .py, .js, .rb, .php, .rs) in dir.
func sourceFiles(dir string) []string {
	var files []string
	for _, ext := range []string{"*.go", "*.py", "*.js", "*.rb", "*.php", "*.rs"} {
		matches, _ := filepath.Glob(filepath.Join(dir, ext))
		files = append(files, matches...)
	}
	return files
}

// extractTriggerLine extracts the content after "drift:trigger " from a comment line.
// Supports both // and # comment prefixes.
func extractTriggerLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"// drift:trigger ", "# drift:trigger "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

// extractScheduleLine extracts the content after "drift:schedule " from a comment line.
func extractScheduleLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"// drift:schedule ", "# drift:schedule "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

// parseTriggerComments scans source files in dir for drift:trigger annotations.
//
// Supported formats (// for Go/Node, # for Python):
//
//	// drift:trigger queue my-queue
//	// drift:trigger queue my-queue poll=250ms retry=5
//	# drift:trigger webhook /hooks/payment
func parseTriggerComments(dir string) ([]TriggerSpec, error) {
	var triggers []TriggerSpec
	for _, f := range sourceFiles(dir) {
		data, err := os.ReadFile(f) // #nosec G304 — CLI reads user's source file by design
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			content, ok := extractTriggerLine(line)
			if !ok {
				continue
			}
			parts := strings.Fields(content)
			if len(parts) < 2 {
				continue
			}
			trigType := parts[0]
			if trigType != "queue" && trigType != "webhook" {
				fmt.Printf("Warning: unknown trigger type %q — skipping\n", trigType)
				continue
			}
			spec := TriggerSpec{
				Type:     trigType,
				Source:   parts[1],
				PollMS:   500,
				MaxRetry: 3,
			}
			for _, kv := range parts[2:] {
				if strings.HasPrefix(kv, "poll=") {
					if d, err := time.ParseDuration(strings.TrimPrefix(kv, "poll=")); err == nil {
						spec.PollMS = int(d.Milliseconds())
					}
				} else if strings.HasPrefix(kv, "retry=") {
					if n, err := strconv.Atoi(strings.TrimPrefix(kv, "retry=")); err == nil && n > 0 {
						spec.MaxRetry = n
					}
				}
			}
			triggers = append(triggers, spec)
		}
	}
	return triggers, nil
}

// refuseScheduleComments fails the deploy when source carries a
// `// drift:schedule` comment, naming the Driftfile attribute that declares a
// schedule.
//
// # Why this REFUSES rather than ignoring the comment
//
// A schedule is a pulse's `cron` in the Driftfile: declared in the file,
// honoured by every deploy path, and METERED, because it travels on the
// artifact through the operator, which counts it against the slice's envelope
// and bills it (tier.CentsPerScheduledJob). A comment in source declares
// nothing, and ignoring it silently would leave whoever wrote it believing a
// schedule runs. Refusing costs one line moved into the Driftfile and says
// exactly which line.
func refuseScheduleComments(dir string) error {
	for _, f := range sourceFiles(dir) {
		data, err := os.ReadFile(f) // #nosec G304 — CLI reads user's source file by design
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			expr, ok := extractScheduleLine(line)
			if !ok || expr == "" {
				continue
			}
			return fmt.Errorf(
				"%s carries `// drift:schedule %s`, which declares nothing.\n"+
					"       Declare the schedule in the Driftfile instead, on this pulse:\n\n"+
					"         cron = \"%s\"\n\n"+
					"       That is honoured by every deploy path, and is counted against the\n"+
					"       slice's envelope rather than registered unmetered.",
				filepath.Base(f), expr, expr)
		}
	}
	return nil
}

// sendSourceToOperator uploads the compiled binary to the API as
// multipart/form-data. The whole payload is buffered (typical Go binaries
// are ~10–30 MB) so the request body can be replayed if the auto-refresh
// path needs to retry after a 401.
// createUserSourceArchive creates a tar.gz of the user's original source files
// from the function directory. This is the portable, Drift-free snapshot of the
// user's code — no generated wrappers, no injected SDKs, no compiled binaries.
// Used for the snapshot/backup system so users can migrate away from Drift.
func createUserSourceArchive(absFolder, name string) (string, error) {
	archivePath := filepath.Join(os.TempDir(), fmt.Sprintf("drift-user-source-%s.tar.gz", name))
	if err := createTarGz(absFolder, archivePath); err != nil {
		return "", fmt.Errorf("create user source archive: %w", err)
	}
	return archivePath, nil
}

// operatorSink is the default SlotSink: it uploads the built function to the
// operator (the cloud deploy path). `drift file run` swaps in localSlotSink
// to write the on-disk slot layout instead — see sink.go.
// invocationProtocol says how the slice may talk to this artifact.
//
// "loop" means the wrapper reads newline-delimited envelopes until stdin closes,
// so the slice may keep the process warm and stop paying the sandbox install and
// runtime startup on every call. Anything else means one envelope then exit, and
// the slice must spawn per invocation.
//
// It is declared per LANGUAGE because the loop lives in different places: Go's
// wrapper owns its own stdin cycle, while Rust, Ruby and PHP hand theirs to the
// SDK's `run`, so warming those needs an SDK release rather than a template
// change. Python and Node are dispatched to a persistent language server long
// before this matters.
//
// The safe answer is the empty one: a slice told "loop" about a one-shot binary
// waits forever on the second call, while a slice told nothing about a looping
// one merely spawns more than it had to.
func invocationProtocol(language string) string {
	if atomic_common.IsGo(language) {
		return "loop"
	}
	return ""
}

// slotProtocol is invocationProtocol for one slot. A Field is Go but reads no
// envelopes at all, so it declares none: "loop" would tell the slice it may hand
// the process a request, and nothing ever does.
func slotProtocol(method, language string) string {
	if method == FieldMethod {
		return ""
	}
	return invocationProtocol(language)
}

func operatorSink(a FuncArtifact) error {
	name, method, language, auth, element, stream := a.Name, a.Method, a.Language, a.Auth, a.Element, a.Stream
	secrets, sourcePath, userSourcePath, triggers, digest := a.Secrets, a.SourcePath, a.UserSourcePath, a.Triggers, a.Digest
	fields := map[string]any{
		"name":     name,
		"method":   method,
		"language": language,
		"auth":     auth,
		"element":  element,
		"stream":   stream,
		"secrets":  secrets,
		// Declared plain configuration, separate from `secrets` on purpose —
		// see the Driftfile schema's `env` and hurdles/011. Sent verbatim; the
		// CLI neither validates the names nor reads the values, because the
		// runtime owns which names it refuses and is the only layer a tenant
		// cannot bypass.
		"env":      a.Env,
		"triggers": triggers,
		"digest":   digest,
		// Lets the slice regenerate the entry-point wrapper on a snapshot restore
		// (#PLATFORM-CORE-OPERATOR-5JPT4H). Empty for compiled languages.
		"source_module": a.SourceModule,
		"entry_func":    a.EntryFunc,
		"protocol":      slotProtocol(method, language),
		// The declared reply shape. Sent even when empty so the field is present
		// on the wire: the operator and the slice both read it, and this is the
		// first of the three parsers it has to survive.
		"response": a.Response,
	}
	// Only when true, so a Pulse that is not concurrent is sent exactly the
	// metadata it always was, and an operator or slice that predates the key
	// never reads it for one. The deprecated count is never sent: the slice
	// decides the number.
	if a.Concurrent {
		fields["concurrent"] = true
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	f, err := os.Open(sourcePath) // #nosec G304 — CLI tool reads user's compiled binary by design
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaPart, err := mw.CreateFormField("metadata")
	if err != nil {
		return fmt.Errorf("failed to create metadata field: %w", err)
	}
	if _, err := metaPart.Write(meta); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}
	srcPart, err := mw.CreateFormFile("source", filepath.Base(sourcePath))
	if err != nil {
		return fmt.Errorf("failed to create source field: %w", err)
	}
	if _, err := io.Copy(srcPart, f); err != nil {
		return fmt.Errorf("failed to buffer source: %w", err)
	}

	// Attach the user's original source files for snapshot/backup.
	if userSourcePath != "" {
		uf, err := os.Open(userSourcePath) // #nosec G304
		if err == nil {
			usPart, partErr := mw.CreateFormFile("user-source", filepath.Base(userSourcePath))
			if partErr == nil {
				io.Copy(usPart, uf) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			}
			uf.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		}
	}

	if err := mw.Close(); err != nil {
		return fmt.Errorf("failed to finalize multipart: %w", err)
	}

	// The operator serializes atomic deploys per slice behind a non-blocking
	// lock — that lock is what makes the function-quota check atomic (count →
	// compare to limit → assign slot, all as one critical section). When
	// `drift apply` builds functions in parallel and they reach the deploy
	// step together, the losers get 409 "another deploy in progress".
	// Retry those (with backoff) so they serialize through the lock instead of
	// failing spuriously. A 429 (function limit reached) is a REAL rejection
	// from inside that critical section — never retried; it surfaces as-is.
	bodyBytes := buf.Bytes()
	contentType := mw.FormDataContentType()
	deadline := time.Now().Add(90 * time.Second)
	for {
		resp, err := common.DoRequestWithContentType(
			http.MethodPost,
			common.APIBaseURL+"/ops/atomic",
			contentType,
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			// Transport error (a timeout or a momentary blip — common when an
			// element ships many functions at once and the API is briefly
			// saturated). The deploy is idempotent, so retry within the deadline
			// rather than failing the whole element on one slow upload.
			if time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)
				continue
			}
			return common.TransportError("deploy the function", err)
		}
		if resp.StatusCode == http.StatusConflict && time.Now().Before(deadline) {
			resp.Body.Close() // #nosec G104 -- retrying; body discarded intentionally
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_, cerr := common.CheckResponse(resp, "deploy the function")
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited
		return cerr
	}
}

// DeployFunction builds and deploys one declared function. It is exported so
// `drift apply` can call it: apply is the only command that ships a function.
// When quiet is true, all per-function status chatter is
// suppressed so the manifest deploy can render its own clean summary line for
// each function.
//
// Every fact it needs comes from the Driftfile entry in spec. The one thing it
// asks the filesystem is which file declares the handler — the question a
// manifest cannot answer.
func DeployFunction(spec FunctionSpec, quiet bool) error {
	absFolder, err := filepath.Abs(spec.Dir)
	if err != nil {
		return fmt.Errorf("failed to resolve folder path: %w", err)
	}

	// (method, name) is the pair the operator and slice speak. For HTTP it is
	// the verb and the route. For a queue it is the synthetic "queue" method,
	// which no HTTP request can match — that is what keeps a queue handler
	// unreachable from /api/* while the in-process trigger registry can still
	// look it up by (method, name) — and the queue's own name.
	method, name := spec.Wire()
	element := spec.Element
	auth, stream, secrets := spec.Auth, spec.Stream, spec.Secrets
	response := spec.Response

	callable, err := atomic_common.FindCallable(absFolder, spec.Handler)
	if err != nil {
		return fmt.Errorf("function %q: %w", spec.Name, err)
	}
	language, sourceFile := atomic_common.BuildLanguage(callable.Language), callable.SourceFile

	// The one-function path reaches here without BuildElements, so it applies
	// the Field language rule itself, from the same owner.
	if err := refuseNonGoField(spec, language); err != nil {
		return err
	}
	if err := refuseConcurrency(spec, language); err != nil {
		return err
	}

	// Catch a common footgun before building: a function that uses the SDK
	// but ships no dependency manifest to declare it — otherwise it deploys
	// fine and then fails cryptically at runtime ("No module named 'drift'").
	// No-op for Go/Rust (they auto-provision the SDK at build).
	if err := atomic_common.VerifySDKManifest(absFolder, language); err != nil {
		return err
	}

	if !quiet {
		langLabel := language
		if atomic_common.IsGo(langLabel) {
			langLabel = "go"
		}
		switch q := spec.QueueSource(); {
		case spec.IsField():
			fmt.Printf("Deploying field '%s' (%s)\n", name, langLabel)
		case q != "":
			fmt.Printf("Deploying queue handler '%s' (%s, source: %s)\n", name, langLabel, q)
		default:
			// Org-only routing: a pulse is served at /<name>; the element is
			// never a route segment.
			fmt.Printf("Deploying pulse '%s /%s' (%s, auth: %s)\n", method, name, langLabel, auth)
		}
	}

	// A `// drift:trigger queue NAME` comment binds an EXTRA queue to a
	// function that already has its own trigger — a handler consuming from
	// several queues. It stacks with the one the Driftfile declares.
	//
	// A Field takes none: nothing invokes it, and the operator refuses a Field
	// that carries a trigger.
	var triggers []TriggerSpec
	if !spec.IsField() {
		triggers, err = parseTriggerComments(absFolder)
		if err != nil {
			return fmt.Errorf("failed to parse trigger comments: %w", err)
		}
	}
	// A `// drift:schedule` comment is refused rather than ignored: a schedule
	// is a pulse's `cron` in the Driftfile. See refuseScheduleComments.
	if err := refuseScheduleComments(absFolder); err != nil {
		return err
	}
	triggers = append(triggers, triggersFor(ElementFunc{Spec: spec, SourceFile: sourceFile})...)

	if len(triggers) > 0 && !quiet {
		fmt.Printf("  %d trigger(s) found: ", len(triggers))
		for i, t := range triggers {
			if i > 0 {
				fmt.Print(", ")
			}
			if t.Type == "schedule" {
				fmt.Printf("schedule(every %s)", t.Schedule)
			} else {
				fmt.Printf("%s(%s)", t.Type, t.Source)
			}
		}
		fmt.Println()
	}

	var sourcePath string
	switch language {
	case "python":
		sourcePath, err = buildPython(absFolder, method, name, callable)
	case "node":
		sourcePath, err = buildNode(absFolder, method, name, callable)
	case "ruby":
		sourcePath, err = buildRuby(absFolder, method, name, callable)
	case "php":
		sourcePath, err = buildPHP(absFolder, method, name, callable)
	case "rust":
		sourcePath, err = buildRust(absFolder, method, name, callable)
	default:
		sourcePath, err = buildGo(absFolder, spec)
	}
	if err != nil {
		return err
	}
	defer os.Remove(sourcePath)

	// Create a portable archive of the user's original source files.
	userSourcePath, usErr := createUserSourceArchive(absFolder, name)
	if usErr != nil {
		userSourcePath = "" // best-effort — deploy still works without it
	} else {
		defer os.Remove(userSourcePath)
	}

	// Content fingerprint of this function — its source AND its declaration —
	// recorded with the deploy so a later `drift apply` can skip it if nothing
	// changed.
	//
	// Best-effort: on error we send "" — the deploy still succeeds, it just
	// won't be skippable next time (an empty digest never matches, and
	// DeployDigest keeps it empty rather than hashing the emptiness into a value
	// that could).
	buildDigest, dErr := FunctionDigest(absFolder, element)
	if dErr != nil {
		buildDigest = ""
	}
	digest := DeployDigest(buildDigest, spec)

	// The two facts the SLICE needs to render this function's entry-point
	// wrapper itself, taken from the same callable the build was bound to
	// rather than restated. Empty for compiled languages, whose entry point is
	// a binary the runtime cannot produce.
	var wrapSourceModule, wrapEntryFunc string
	switch language {
	case "python", "node", "ruby", "php":
		wrapSourceModule = strings.TrimSuffix(filepath.Base(sourceFile), filepath.Ext(sourceFile))
		wrapEntryFunc = callable.Handler
	}

	if err := sendSourceToOperator(FuncArtifact{
		Name: name, Method: method, Language: language, Auth: auth,
		Element: element, Stream: stream, Response: response, Secrets: secrets,
		Env:        spec.Env,
		Concurrent: spec.IsConcurrent(),
		Triggers:   triggers, Digest: digest,
		SourcePath: sourcePath, UserSourcePath: userSourcePath,
		SourceModule: wrapSourceModule, EntryFunc: wrapEntryFunc,
	}); err != nil {
		return err
	}

	if !quiet {
		if spec.IsField() {
			fmt.Printf("  Field '%s' deployed successfully!\n", name)
		} else {
			fmt.Printf("  Pulse '%s /%s' deployed successfully!\n", method, name)
		}
	}
	return nil
}
