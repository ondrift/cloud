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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	atomic_common "github.com/ondrift/cloud/cli/cmd/atomic/common"
	"github.com/ondrift/cloud/cli/common"
)

// DefaultElementName is the element a function belongs to when it names none:
// the flat source files directly under atomic/.
const DefaultElementName = "default"

// FunctionSpec is one `atomic.functions` entry, resolved.
//
// It is a plain struct rather than the manifest type because the manifest lives
// in the project package, which imports this one. The project package reads the
// document and fills these in; this package never parses YAML.
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
	// Secrets is the allowlist of Backbone secrets the runner injects.
	Secrets []string
}

// Trigger reports how the function is invoked: "http" or "queue".
func (s FunctionSpec) Trigger() string {
	if strings.HasPrefix(s.Name, "queue:") {
		return "queue"
	}
	return "http"
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
			c, cerr := atomic_common.FindCallable(dir, s.Handler)
			if cerr != nil {
				return nil, fmt.Errorf("function %q: %w", s.Name, cerr)
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
	return fmt.Errorf("route collision — these functions share an identity and would shadow "+
		"each other on deploy (a function is identified by method AND path, so get:x and post:x "+
		"are fine, but two post:x are not):\n  - %s", strings.Join(collisions, "\n  - "))
}

// triggersFor assembles the trigger specs a function ships with: the queue it
// consumes, if any, plus the Driftfile schedule bound to it.
func triggersFor(f ElementFunc) []TriggerSpec {
	var triggers []TriggerSpec
	method, name := f.Spec.Wire()
	if q := f.Spec.QueueSource(); q != "" {
		triggers = append(triggers, TriggerSpec{
			Type: "queue", Source: q, Method: "queue", PollMS: 500, MaxRetry: 3,
		})
	}
	// A Driftfile `cron:` is additive — the function keeps its own trigger and
	// also fires on schedule, so the schedule carries the function's own method.
	return append(triggers, scheduleTriggerFor(name, method)...)
}

// DeployGoElement builds and deploys every function in a Go element. The
// element's package is staged and dependency-resolved ONCE; each function is
// then compiled to its own binary (reusing the warm build cache) and sent to
// the operator. digest is the element's content fingerprint, recorded against
// every function so an unchanged element is skippable next deploy.
func DeployGoElement(el Element, digest string, quiet bool) error {
	if len(el.Funcs) == 0 {
		return nil
	}

	stageDir, err := buildGoElementStage(el.Dir, el.Name)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir) // #nosec G104

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
	warmMethod, _ := el.Funcs[0].Spec.Wire()
	if _, warmDir, werr := buildGoEntrypointIsolated(stageDir, el.Funcs[0].Spec.Handler, warmMethod, "warm"); werr != nil {
		return fmt.Errorf("element %q failed to compile: %w", el.Name, werr)
	} else {
		os.RemoveAll(warmDir) // #nosec G104
	}

	// Build + ship every function CONCURRENTLY — each in its own dir, sharing
	// the warmed module + build caches, so this is N parallel links + uploads.
	deployOne := func(f ElementFunc) error {
		method, name := f.Spec.Wire()
		bin, fnDir, berr := buildGoEntrypointIsolated(stageDir, f.Spec.Handler, method,
			safeTmpName(el.Name+"-"+f.Spec.Handler))
		if berr != nil {
			return berr
		}
		defer os.RemoveAll(fnDir) // #nosec G104
		// No SourceModule/EntryFunc: a native function's entry point is the built
		// binary, which the runtime cannot generate and this path must supply.
		return sendSourceToOperator(FuncArtifact{
			Name: name, Method: method, Language: "go", Auth: f.Spec.Auth,
			Element: el.Name, Stream: f.Spec.Stream, Secrets: f.Spec.Secrets,
			Triggers: triggersFor(f), Digest: digest,
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
			if !quiet {
				fmt.Printf("    %s %s\n", common.Cross(), f.MethodPath())
			}
		case !quiet:
			fmt.Printf("    %s %s\n", common.Check(), f.MethodPath())
		}
	}
	return firstErr
}
