package project

// schema.go — validating a Driftfile against the format the PLATFORM defines,
// rather than against a copy of it compiled into this binary.
//
// # Why this replaces a Go mirror
//
// The Driftfile used to be validated by decoding it into 17 hand-written struct
// types and re-decoding with `KnownFields(true)` to reject anything unrecognised.
// That made the CLI a second definition of the format, and it had a cost users
// felt: a Driftfile using a field the platform had added was rejected outright, so
// every format addition needed a CLI release before anyone could use it. Serving
// the schema existed to remove exactly that coupling, and then nothing read it.
//
// Now the platform's schema is the authority. A field this binary predates is
// legal if the schema says so, and the CLI passes it through untouched.
//
// # What this does NOT validate, deliberately
//
// Anything that needs the local filesystem or cross-field state: whether
// `atomic.functions[N].dir` exists on disk, whether a `$ENVREF` resolves. A schema
// describes a legal DOCUMENT; it cannot know what is on this machine. Those checks
// live in validate() and stay there — the duplication being removed is the ~39
// pattern and format rules the schema's own `pattern` keywords already encode.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrNoSchema is returned when this machine has never fetched the schema, so
// there is nothing to validate against.
//
// It is a distinct error because the remedy is specific and one-off, and a user
// hitting it offline on a fresh install deserves to be told that rather than shown
// a validation failure. common/driftfile.go's own header calls this honest rather
// than a gap: a binary that has never met the platform has no basis for claiming
// what the format is, and inventing one is how the two definitions drifted apart.
var ErrNoSchema = errors.New(
	"this machine has no copy of the Driftfile format yet, so it cannot be validated.\n" +
		"  The platform defines the format and this CLI fetches it on first contact —\n" +
		"  run any online command once (`drift account login`, `drift slice list`) and\n" +
		"  it is cached permanently at ~/.drift/driftfile.schema.json.")

// compiledSchema caches the compiled schema for the process. Compiling is the
// expensive half and a single `project deploy` validates more than once.
var compiledSchema *jsonschema.Schema

// validateAgainstSchema checks the shorthand-expanded document against the
// platform's schema and returns one error per violation, in document order.
func validateAgainstSchema(doc map[string]any) ParseErrors {
	sch, err := loadSchema()
	if err != nil {
		return ParseErrors{err.Error()}
	}
	if sch == nil {
		return nil // no schema on this machine; caller decides whether that is fatal
	}

	// The validator works on the JSON data model, and a YAML decode yields Go
	// types it does not accept (notably map[any]any and int). A JSON round trip
	// is the honest conversion rather than a hand-written walk that would have to
	// know about every type YAML can produce.
	raw, merr := json.Marshal(doc)
	if merr != nil {
		return ParseErrors{fmt.Sprintf("Driftfile: cannot be represented as JSON for validation: %v", merr)}
	}
	var jsonDoc any
	if uerr := json.Unmarshal(raw, &jsonDoc); uerr != nil {
		return ParseErrors{fmt.Sprintf("Driftfile: %v", uerr)}
	}

	if verr := sch.Validate(jsonDoc); verr != nil {
		var ve *jsonschema.ValidationError
		if errors.As(verr, &ve) {
			return schemaErrors(ve)
		}
		return ParseErrors{fmt.Sprintf("Driftfile: %v", verr)}
	}
	return nil
}

// loadSchema compiles this machine's cached schema, or returns nil when there
// isn't one. A malformed cache is reported rather than silently ignored: it means
// the file was corrupted, and pretending the format is unknown would turn that
// into a confusing pass.
func loadSchema() (*jsonschema.Schema, error) {
	if compiledSchema != nil {
		return compiledSchema, nil
	}
	raw, rerr := common.LocalDriftfileSchema()
	if rerr != nil {
		return nil, nil // never fetched; the caller decides whether that is fatal
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the cached Driftfile schema is not valid JSON (%v) — "+
			"delete ~/.drift/driftfile.schema.json and run any online command to refetch it", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("driftfile.schema.json", doc); err != nil {
		return nil, fmt.Errorf("the cached Driftfile schema could not be loaded: %w", err)
	}
	sch, err := c.Compile("driftfile.schema.json")
	if err != nil {
		return nil, fmt.Errorf("the cached Driftfile schema could not be compiled: %w", err)
	}
	compiledSchema = sch
	return sch, nil
}

// schemaErrors flattens a validation error tree into one message per leaf.
//
// The tree is what makes a schema library usable or not. Reporting the root only
// produces "doesn't match schema", which tells a user nothing; reporting every
// node repeats the same failure at four levels of nesting. The leaves are the
// actual violations, so those are what gets printed, keyed by the instance
// location the user can find in their file.
func schemaErrors(ve *jsonschema.ValidationError) ParseErrors {
	seen := map[string]bool{}
	var out []string

	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		// Error() on a LEAF renders that node alone, already keyed by its instance
		// location. Rendering the ErrorKind directly needs the library's own
		// message printer and panics on a nil one, which is a poor trade for text
		// this already produces correctly.
		msg := strings.TrimSpace(e.Error())
		if msg == "" {
			return
		}
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}
	walk(ve)

	sort.Strings(out)
	return ParseErrors(dropShallowerBranches(out))
}

// dropShallowerBranches removes an error whose instance location is a strict
// prefix of another error's.
//
// This is what makes `oneOf` usable. The Driftfile lets `environments` be either a
// bare list or a map, so a map with a bad key inside it fails BOTH branches and the
// validator reports both:
//
//	at '/environments': got object, want array          <- the branch not taken
//	at '/environments/staging': additional properties 'name' not allowed
//
// The first is a red herring — the user did not write an array and does not care
// that one was allowed — and printing it alongside the real error invites them to
// "fix" the wrong thing. The deeper location is the specific complaint, so the
// shallower one goes.
//
// Prefix, not equality, and on a path boundary: `/environments` shadows
// `/environments/staging`, while `/atomic` must not shadow `/atomics`.
func dropShallowerBranches(msgs []string) []string {
	locs := make([]string, len(msgs))
	for i, m := range msgs {
		locs[i] = instanceLocation(m)
	}
	out := make([]string, 0, len(msgs))
	for i, m := range msgs {
		shadowed := false
		for j, other := range locs {
			if i == j || locs[i] == "" || other == "" || len(other) <= len(locs[i]) {
				continue
			}
			if strings.HasPrefix(other, locs[i]+"/") {
				shadowed = true
				break
			}
		}
		if !shadowed {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return msgs // never turn a failure into silence
	}
	return out
}

// instanceLocation pulls the `/a/b` out of the library's `at '/a/b': ...` prefix.
// An unrecognised shape yields "", which simply opts that message out of shadowing.
func instanceLocation(msg string) string {
	const marker = "at '"
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// CompiledFloor is the platform's per-language floor on a function's booking:
// how little a COMPILED function may book, and the languages that rule exempts.
//
// Both are read from the fetched schema for the same reason NamePattern is. The
// bound belongs to the platform — `tier.MinCompiledFunctionMemoryBytes` refuses
// the deploy, and the interpreted set is the branch the slice itself takes — so a
// copy compiled into this binary would be a second definition that drifts, and
// would drift in the direction that costs most: a CLI refusing a booking the
// platform accepts is a CLI that has invented a rule.
//
// It cannot be a `pattern` on `memory`, which is why it is read rather than
// validated. A pattern constrains the DOCUMENT, and the Driftfile never says what
// language a function is written in — that is detected from the source beside it,
// which only this machine can see.
type CompiledFloor struct {
	// Bytes is the minimum a compiled function may book. Zero means the schema
	// did not publish one.
	Bytes int
	// Declared is the floor as the schema writes it ("16MB"), for the message.
	Declared string
	// interpreted is the set the floor does NOT apply to.
	interpreted map[string]bool
}

// Applies reports whether the floor binds a function written in lang.
//
// Keyed on the interpreted set rather than a list of compiled languages because
// that is the stable side, and the same side the operator and the slice key on:
// the wire value for a compiled language is a known-moving target, while the
// interpreted four are not. An unrecognised language is therefore treated as
// compiled — matching what the platform will do with it.
func (c CompiledFloor) Applies(lang string) bool {
	if c.Bytes <= 0 || len(c.interpreted) == 0 {
		return false
	}
	return !c.interpreted[strings.ToLower(lang)]
}

// CompiledMemoryFloor reads the floor this machine's schema publishes.
//
// Returns a zero CompiledFloor when the schema is absent, predates the
// annotation, or publishes it unusably — and callers then skip the check rather
// than inventing one. That is the same choice NamePattern makes and the same
// reason: the api is the final enforcer either way, so a guess buys nothing and
// costs the CLI its claim to hold no rules of its own. The cost of skipping is
// that the tenant meets the floor at deploy instead of at lint, which is exactly
// where they met it before.
func CompiledMemoryFloor() CompiledFloor {
	raw, err := common.LocalDriftfileSchema()
	if err != nil {
		return CompiledFloor{}
	}
	var doc struct {
		Definitions struct {
			AtomicEntry struct {
				Properties struct {
					Memory struct {
						CompiledMinimum string   `json:"x-drift-compiled-minimum"`
						Interpreted     []string `json:"x-drift-interpreted-languages"`
					} `json:"memory"`
				} `json:"properties"`
			} `json:"atomicEntry"`
		} `json:"definitions"`
	}
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return CompiledFloor{}
	}
	mem := doc.Definitions.AtomicEntry.Properties.Memory
	if mem.CompiledMinimum == "" || len(mem.Interpreted) == 0 {
		return CompiledFloor{}
	}
	bytes, perr := parseSizeBytes(mem.CompiledMinimum)
	if perr != nil || bytes <= 0 {
		return CompiledFloor{}
	}
	set := make(map[string]bool, len(mem.Interpreted))
	for _, l := range mem.Interpreted {
		set[strings.ToLower(l)] = true
	}
	return CompiledFloor{Bytes: bytes, Declared: mem.CompiledMinimum, interpreted: set}
}

// NamePattern is the platform's rule for a project/slice identifier, read out of
// the fetched schema rather than restated here.
//
// The CLI needs it for one value the schema cannot see: the slice name it DERIVES
// (`<project>-<environment>`) never appears in the Driftfile, so no amount of
// document validation can catch `a-very-long-project-name` + `-staging` going over
// the limit. Checking it locally is legitimate; hardcoding the rule to do so is
// not — that was `nameRe`, a second copy of a rule the platform owns, of exactly
// the kind that let `nosql: [widgets]` be legal here and illegal there.
//
// So the rule is fetched with everything else. Returns nil when the schema has no
// pattern for `name` or is not on this machine, and callers then skip the check
// rather than inventing one: the server is the final enforcer either way, and a
// guess is what this whole change removes.
func NamePattern() *regexp.Regexp {
	raw, err := common.LocalDriftfileSchema()
	if err != nil {
		return nil
	}
	var doc struct {
		Properties struct {
			Name struct {
				Pattern string `json:"pattern"`
			} `json:"name"`
		} `json:"properties"`
	}
	if jerr := json.Unmarshal(raw, &doc); jerr != nil || doc.Properties.Name.Pattern == "" {
		return nil
	}
	re, cerr := regexp.Compile(doc.Properties.Name.Pattern)
	if cerr != nil {
		return nil
	}
	return re
}
