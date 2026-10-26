// elements.go — Elements, built from what the Driftfile declares.
//
// An Element is a single-language backend: one language, one dependency
// manifest, and N functions across the flat files in one directory. It is
// Drift's "service" unit (what you'd put in one container elsewhere).
//
// An element is a GROUPING, not a discovery: the Driftfile says which element
// each function belongs to, and the element's directory follows from its name —
// `atomic/` for the default element, `atomic/<element>` for a named one. The
// language is whatever that directory is written in.
//
// Nothing here reads the user's source to decide what to deploy. The manifest
// is the declaration; source is consulted only to find the file a declared
// handler lives in, which is the one question the manifest cannot answer.
package atomic_cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
	"github.com/ondrift/cloud/cli/common"
)

// DefaultElementName is the element a function belongs to when it names none:
// the flat source files directly under atomic/.
const DefaultElementName = "default"

// FunctionSpec is one declared pulse or field, resolved.
//
// It is a plain struct rather than the manifest type because the manifest lives
// in the project package, which imports this one. The project package reads the
// Driftfile and fills these in; this package never reads one.
type FunctionSpec struct {
	// Name is the identity exactly as the Driftfile writes it —
	// "post:users" or "queue:orders". It is what the slice books.
	Name string
	// Handler is the callable that serves it.
	Handler string
	// Element is the element it belongs to; empty means the default.
	Element string
	// Dir is the absolute source directory, already resolved against the
	// Driftfile (the element's directory unless the entry overrode it).
	Dir string
	// Auth is the gate ("none", "apikey", or "" for the default).
	Auth string
	// Stream is the response shape ("", "sse", "ws").
	Stream string
	// Response is how the reply is written: "", "envelope", "json" or "raw".
	// Empty means envelope, which is what every function answered before the
	// field existed.
	Response string
	// Secrets is the allowlist of Backbone secrets the runner injects.
	Secrets []string
	// Env is the function's declared plain configuration — NOT credentials.
	//
	// Carried verbatim to the operator and on to the slice, which injects it
	// into the function's environment. The CLI does not validate the names: the
	// runtime owns which ones it refuses (`is_reserved_env`) and is the only
	// layer a tenant cannot bypass, so a second copy of that list here would be
	// a security rule in two places that can disagree. The schema carries a
	// third spelling, for the error message alone.
	//
	// hurdles/011: before this, `secrets:` was the only per-function injection
	// channel, so an in-cluster address was declared beside the token that
	// authenticates to it.
	Env map[string]string
	// Concurrent is the Driftfile's `concurrent`: the handler is safe to run in
	// parallel, and how many of its calls run at once is the slice's to
	// decide.
	Concurrent bool
	// Concurrency is the deprecated `concurrency: N` from Driftfile 1.22.0, as
	// written; 0 means the Driftfile left it out. Above 1 it declares what
	// Concurrent does, and the number is used for nothing else.
	Concurrency int
	// PollMS and MaxRetry are a queue Pulse's `poll` and `max_retries`, as the
	// document carries them; 0 means the Driftfile left them out and the
	// defaults apply (500 ms, 3 attempts).
	PollMS   int
	MaxRetry int
}

// IsConcurrent reports whether the Pulse was declared concurrent, in either
// spelling. Only a buffered Go Pulse reached over HTTP may be (see
// refuseConcurrency), and it reaches the operator and the digest, as
// `concurrent`, only then.
func (s FunctionSpec) IsConcurrent() bool {
	return s.Concurrent || s.Concurrency > 1
}

// What a queue Pulse polls and retries with when the Driftfile says nothing: the
// figures the slice itself applies to a trigger that carries none.
const (
	defaultQueuePollMS   = 500
	defaultQueueMaxRetry = 3
)

// FieldMethod is the method a Field's slot is booked and deployed under. No
// HTTP request can carry it, so nothing routes to a Field: the slice's
// supervisor starts it, and the registry never matches it to a call.
const FieldMethod = "field"

// QueueMethod is the method a Pulse a queue triggers is booked and deployed
// under, `queue:<queue>`. Like FieldMethod, no HTTP request can carry it.
const QueueMethod = "queue"

// SlotMethods is every method a slot can be booked under: the HTTP methods a
// Pulse answers, QueueMethod and FieldMethod. The Driftfile's `method` enum is
// the first eight; a Field's method comes from the section it is declared in.
var SlotMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", QueueMethod, FieldMethod}

// IsSlotMethod reports whether m, in any case, is one of SlotMethods.
func IsSlotMethod(m string) bool {
	return slices.Contains(SlotMethods, strings.ToLower(m))
}

// FieldKey is a Field's booking key, `field:<name>`: the string the slice books
// it under, the name a FunctionSpec carries for it, and what the reference
// check and the idle report compare.
func FieldKey(name string) string { return FieldMethod + ":" + name }

// IsField reports whether this spec is a Field rather than a Pulse.
func (s FunctionSpec) IsField() bool {
	method, _ := s.Wire()
	return method == FieldMethod
}

// Trigger reports how the slot is started: "http" or "queue" for a Pulse, and
// "field" for a Field, which nothing invokes.
func (s FunctionSpec) Trigger() string {
	switch {
	case s.IsField():
		return FieldMethod
	case strings.HasPrefix(s.Name, "queue:"):
		return "queue"
	}
	return "http"
}

// refuseNonGoField is the rule that a Field runs Go in this release, applied
// before anything is built.
//
// The entry point that turns a handler into a long-running process is a Go
// template (default/field_native.txt), and no other language has one. Building
// anything else would produce a Pulse's wrapper, which reads one request and
// exits, and the supervisor would restart it into a crash loop.
func refuseNonGoField(spec FunctionSpec, lang string) error {
	if !spec.IsField() || atomic_common.IsGo(lang) {
		return nil
	}
	_, name := spec.Wire()
	return fmt.Errorf("field %q is written in %s, and %w. "+
		"Write its handler in Go, as `func %s(ctx context.Context) error`, in an element of its own "+
		"if the rest of this one is %s", name, lang, ErrFieldLanguage, spec.Handler, lang)
}

// ErrFieldLanguage is the rule refuseNonGoField applies, so a caller can tell
// that refusal from any other build failure and report it before anything is
// deployed.
var ErrFieldLanguage = errors.New("a Field runs Go in this release")

// MaxPulseConcurrency bounds the deprecated `concurrency: N`. The platform's
// tier.MaxPulseConcurrency is the authority; this is the copy the refusal below
// states. A test pins it to a schema that still publishes the count's bound;
// format 2 does not, because the compiled document never carries the count.
const MaxPulseConcurrency = 64

// ErrConcurrency is the rule refuseConcurrency applies, so a caller can tell
// that refusal from any other build failure and report it before anything is
// deployed.
var ErrConcurrency = errors.New("a concurrent Pulse this release cannot honour")

// concurrencyRefusal carries the operator's own words for a refused
// concurrency and answers errors.Is(err, ErrConcurrency).
type concurrencyRefusal struct{ text string }

func (r concurrencyRefusal) Error() string        { return r.text }
func (r concurrencyRefusal) Is(target error) bool { return target == ErrConcurrency }

// validatePulseConcurrency is the operator's tier.ValidatePulseConcurrency,
// rule for rule and word for word, so a Driftfile is refused here, before
// anything builds, with the text the operator's 400 would carry. concurrent is
// the Driftfile's `concurrent`; n is the deprecated `concurrency` as written,
// 0 when it was not.
func validatePulseConcurrency(method, language, stream string, concurrent bool, n int) error {
	if n < 0 || n > MaxPulseConcurrency {
		return concurrencyRefusal{fmt.Sprintf("concurrency %d is not usable: it must be between 1 and %d", n, MaxPulseConcurrency)}
	}
	if !concurrent && n <= 1 {
		return nil
	}
	declared := "concurrent: true"
	if !concurrent {
		declared = fmt.Sprintf("concurrency: %d", n)
	}
	if language != "go" {
		return concurrencyRefusal{fmt.Sprintf("%s needs a Go Pulse: a concurrent Pulse runs Go in this release, "+
			"and this one is built as %q. Set concurrent to false, or move the handler into a Go element",
			declared, language)}
	}
	if stream != "" {
		return concurrencyRefusal{fmt.Sprintf("%s cannot be combined with stream %q: a concurrent Pulse serves "+
			"buffered requests in this release. Set concurrent to false, or drop the stream", declared, stream)}
	}
	switch {
	case method == FieldMethod:
		return concurrencyRefusal{fmt.Sprintf("%s cannot be declared on a Field: a Field is one process by definition", declared)}
	case method == QueueMethod:
		return concurrencyRefusal{fmt.Sprintf("%s cannot be combined with method queue: a queue Pulse is invoked "+
			"one call at a time by its trigger. Set concurrent to false", declared)}
	case !IsSlotMethod(method):
		return concurrencyRefusal{fmt.Sprintf("%s needs an HTTP method, got %q", declared, method)}
	}
	return nil
}

// refuseConcurrency applies validatePulseConcurrency to one declared slot
// whose element is written in lang, naming the slot.
func refuseConcurrency(spec FunctionSpec, lang string) error {
	method, _ := spec.Wire()
	language := lang
	if atomic_common.IsGo(lang) {
		language = "go"
	}
	if err := validatePulseConcurrency(method, language, spec.Stream, spec.Concurrent, spec.Concurrency); err != nil {
		return fmt.Errorf("%s %q: %w", slotNoun(spec), spec.Name, err)
	}
	return nil
}

// Wire splits the declared name into the (method, name) pair the operator and
// the slice speak.
//
// For HTTP that is the verb and the route. For a queue it is the synthetic
// "queue" method — which no real HTTP request can match, so the handler has no
// URL — and the queue's own name. The slice re-joins them with `:` to get the
// booking key, which is why that key comes back out identical to what the
// Driftfile declared.
func (s FunctionSpec) Wire() (method, name string) {
	method, name, _ = strings.Cut(s.Name, ":")
	return method, name
}

// QueueSource is the queue a queue-triggered function consumes, or "".
func (s FunctionSpec) QueueSource() string {
	if s.Trigger() != "queue" {
		return ""
	}
	_, q := s.Wire()
	return q
}

// ElementName is the element this function belongs to, defaulted.
func (s FunctionSpec) ElementName() string {
	if s.Element == "" {
		return DefaultElementName
	}
	return s.Element
}

// ElementFunc is one function within an Element: its declaration, plus where
// its handler was found.
type ElementFunc struct {
	Spec       FunctionSpec
	SourceFile string // basename of the file declaring the handler
}

// Element is a single-language backend — one language, one dependency
// manifest, N functions across the flat files in Dir.
type Element struct {
	Name  string // "default" for the flat element, else the element's name
	Dir   string // absolute path to the element's package directory
	Lang  string // "go" | "python" | "node" | "ruby" | "php" | "rust"
	Funcs []ElementFunc
}

// ElementDir is where an element's source lives, given the project's atomic
// root. The default element is the flat files directly under it.
func ElementDir(atomicRoot, element string) string {
	if element == "" || element == DefaultElementName {
		return atomicRoot
	}
	return filepath.Join(atomicRoot, element)
}

// BuildElements groups declared functions into elements and binds each one to
// the source file holding its handler.
//
// Every failure here is a manifest that cannot be built — a handler that is not
// there, an element written in two languages — and every one is found offline,
// before a single byte is uploaded.
func BuildElements(specs []FunctionSpec) ([]Element, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	byElement := map[string][]FunctionSpec{}
	order := []string{}
	for _, s := range specs {
		name := s.ElementName()
		if _, seen := byElement[name]; !seen {
			order = append(order, name)
		}
		byElement[name] = append(byElement[name], s)
	}
	sort.Strings(order)

	var elements []Element
	for _, name := range order {
		group := byElement[name]

		// Every function in an element shares its directory. An entry may
		// override `dir`, and one that points somewhere else is not in this
		// element any more — say so rather than build half of it.
		dir := group[0].Dir
		for _, s := range group[1:] {
			if s.Dir != dir {
				return nil, fmt.Errorf(
					"element %q is declared in two directories (%s for %q, %s for %q) — "+
						"an element is one package with one dependency manifest, so its "+
						"functions cannot live apart. Give them separate `element:` names.",
					name, dir, group[0].Name, s.Dir, s.Name)
			}
		}

		lang, err := atomic_common.ElementLanguage(dir)
		if err != nil {
			return nil, fmt.Errorf("element %q: %w", name, err)
		}

		el := Element{Name: name, Dir: dir, Lang: lang}
		for _, s := range group {
			if ferr := refuseNonGoField(s, lang); ferr != nil {
				return nil, fmt.Errorf("element %q: %w", name, ferr)
			}
			if cerr := refuseConcurrency(s, lang); cerr != nil {
				return nil, fmt.Errorf("element %q: %w", name, cerr)
			}
			c, cerr := atomic_common.FindCallable(dir, s.Handler)
			if cerr != nil {
				return nil, fmt.Errorf("%s %q: %w", slotNoun(s), s.Name, cerr)
			}
			el.Funcs = append(el.Funcs, ElementFunc{Spec: s, SourceFile: c.SourceFile})
		}
		sort.Slice(el.Funcs, func(i, j int) bool {
			return el.Funcs[i].Spec.Name < el.Funcs[j].Spec.Name
		})
		elements = append(elements, el)
	}
	return elements, nil
}

// slotNoun names what a spec is, for a message: "pulse" or "field".
func slotNoun(s FunctionSpec) string {
	if s.IsField() {
		return "field"
	}
	return "pulse"
}

// DeployKey is the platform's identity for a function — the Driftfile's own
// name. It is also the cross-element collision key: org-only routing means the
// /api space is shared, so two functions sharing a key collide wherever they
// live.
func (f ElementFunc) DeployKey() string { return f.Spec.Name }

// MethodPath renders a function for diagnostics. It is the declared name,
// because that is the string the user wrote and the one every other surface —
// the slice's booking, the API's metering, this CLI's own output — repeats.
func (f ElementFunc) MethodPath() string { return f.Spec.Name }

// CheckCollisions rejects a manifest in which two functions share an identity.
// A function is identified by method AND path, so get:x and post:x are two
// functions; two post:x would shadow each other on the slice, with the last one
// deployed answering for both.
func CheckCollisions(specs []FunctionSpec) error {
	byName := map[string]int{}
	for _, s := range specs {
		byName[s.Name]++
	}
	var collisions []string
	for name, n := range byName {
		if n > 1 {
			collisions = append(collisions, fmt.Sprintf("%q is declared %d times", name, n))
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Strings(collisions)
	return fmt.Errorf("route collision: these slots share an identity and would shadow "+
		"each other on deploy (a pulse is identified by method AND path, so get:x and post:x "+
		"are fine, but two post:x are not; a field by its name):\n  - %s", strings.Join(collisions, "\n  - "))
}

// triggersFor assembles the trigger specs a function ships with: the queue it
// consumes, if any, plus the Driftfile schedule bound to it.
//
// THE SCHEDULE IS LOOKED UP BY THE FULL DECLARED NAME, `f.Spec.Name`, and not by
// the route half of it. The two are different strings — `get:cronprobe` against
// `cronprobe` — and this line asked for the second while declaredSchedules
// stored the first, so EVERY `cron:` in a Driftfile was silently dropped: the
// deploy succeeded, no trigger was registered, and nothing anywhere said so.
//
// It was true once. `name` used to be a function's route, and the retired
// spelling `name: post:auth/challenge` was the exception; normaliseFunctionIdentities
// now composes `name` as `method:route` for EVERY entry, which changed what this
// side was reading without changing this side. functionidentity.go names exactly
// this failure — those accessors are total, so a reader left on a key that
// stopped being written yields "" with no error anywhere.
func triggersFor(f ElementFunc) []TriggerSpec {
	// A Field is started by the slice's supervisor and invoked by nothing, and
	// the operator refuses a Field that carries a trigger.
	if f.Spec.IsField() {
		return nil
	}
	var triggers []TriggerSpec
	method, _ := f.Spec.Wire()
	if q := f.Spec.QueueSource(); q != "" {
		poll, retry := f.Spec.PollMS, f.Spec.MaxRetry
		if poll == 0 {
			poll = defaultQueuePollMS
		}
		if retry == 0 {
			retry = defaultQueueMaxRetry
		}
		triggers = append(triggers, TriggerSpec{
			Type: "queue", Source: q, Method: "queue", PollMS: poll, MaxRetry: retry,
		})
	}
	// A Driftfile `cron:` is additive — the function keeps its own trigger and
	// also fires on schedule, so the schedule carries the function's own method.
	return append(triggers, scheduleTriggerFor(f.Spec.Name, method)...)
}

// DeployGoElement builds and deploys every function in a Go element. The
// element's package is staged and dependency-resolved ONCE; each function is
// then compiled to its own binary (reusing the warm build cache) and sent to
// the operator. digest is the element's content fingerprint, recorded against
// every function so an unchanged element is skippable next deploy.
//
// out receives one line per slot, in the element's order, after every slot has
// been attempted; nil writes nothing. The caller's writer rather than stdout,
// because `drift apply` buffers each section and prints it under its own
// heading: a line written straight to stdout lands above that heading.
func DeployGoElement(el Element, digest string, out io.Writer) error {
	if len(el.Funcs) == 0 {
		return nil
	}

	stage, err := buildGoElementStage(el.Dir, el.Name)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage.dir) // #nosec G104

	// One user-source archive for the element's package, shared by its
	// functions (they all compile from the same source).
	userSrc, usErr := createUserSourceArchive(el.Dir, el.Name)
	if usErr == nil {
		defer os.Remove(userSrc) // #nosec G104
	} else {
		userSrc = ""
	}

	// Warm Go's build cache by compiling the shared package once (a throwaway
	// entrypoint). The parallel builds below then only re-link, and a
	// package-wide compile error surfaces here once rather than N times across
	// the workers.
	if _, warmDir, werr := buildGoEntrypointIsolated(stage, goEntryOf(el.Funcs[0].Spec), "warm"); werr != nil {
		return fmt.Errorf("element %q failed to compile: %w", el.Name, werr)
	} else {
		os.RemoveAll(warmDir) // #nosec G104
	}

	// Build + ship every function CONCURRENTLY — each in its own dir, sharing
	// the warmed module + build caches, so this is N parallel links + uploads.
	deployOne := func(f ElementFunc) error {
		method, name := f.Spec.Wire()
		bin, fnDir, berr := buildGoEntrypointIsolated(stage, goEntryOf(f.Spec),
			safeTmpName(el.Name+"-"+f.Spec.Handler))
		if berr != nil {
			return berr
		}
		defer os.RemoveAll(fnDir) // #nosec G104
		// No SourceModule/EntryFunc: a native function's entry point is the built
		// binary, which the runtime cannot generate and this path must supply.
		return sendSourceToOperator(FuncArtifact{
			Name: name, Method: method, Language: "go", Auth: f.Spec.Auth,
			Element: el.Name, Stream: f.Spec.Stream, Response: f.Spec.Response,
			Secrets:    f.Spec.Secrets,
			Env:        f.Spec.Env,
			Concurrent: f.Spec.IsConcurrent(),
			Triggers:   triggersFor(f), Digest: DeployDigest(digest, f.Spec),
			SourcePath: bin, UserSourcePath: userSrc,
		})
	}

	results := make([]error, len(el.Funcs))
	workers := min(runtime.NumCPU(), 8, len(el.Funcs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := range el.Funcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = deployOne(el.Funcs[i])
		}(i)
	}
	wg.Wait()

	// Ordered report; return the first failure with its real error.
	var firstErr error
	for i, f := range el.Funcs {
		switch {
		case results[i] != nil:
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.MethodPath(), results[i])
			}
			if out != nil {
				fmt.Fprintf(out, "    %s %s\n", common.Cross(), f.MethodPath())
			}
		case out != nil:
			fmt.Fprintf(out, "    %s %s\n", common.Check(), f.MethodPath())
		}
	}
	return firstErr
}
