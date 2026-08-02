package project

// manifest.go is the Driftfile parser + validator.
//
// Implements the Driftfile manifest format (v2 — project-level). The
// Driftfile *is* the project: the resource shape (name, retention,
// atomic/backbone/canvas/domains) sits at the top level, with two optional
// siblings — `environments` (per-environment config overrides) and `hooks`
// (local pre/post-deploy commands).
//
// The parser does three things in one pass:
//
//   1. Decodes the YAML into the canonical shape, expanding the short-form
//      sugars on the way (atomic-as-bare-list, canvas-as-bare-string, and
//      environments-as-bare-list) — at the top level AND inside each
//      environment override block.
//   2. Resolves `$ENVREF` shorthands in secrets to their literal
//      values from the deployer's environment.
//   3. Validates every field against the spec's binding validation
//      table, collecting all errors into one ParseErrors return so
//      the user sees the whole picture in a single block.
//
// Environment selection + merge (SelectEnvironment) happens AFTER parse,
// driven by the deploy command, so a single parse serves every environment.
//
// What the parser does NOT do:
//
//   - Reach over the network. Live-slice diffing, cost calculation,
//     and reconcile-rule classification all happen later, in the
//     deploy command driver.
//   - Apply defaults for envelope knobs. Omitted knobs are passed
//     through as zero-valued strings; the API gateway resolves them
//     to the slice envelope's defaults.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The Driftfile's rules are NOT here. The platform defines the format and serves
// the schema; schema.go validates against it, and NamePattern() reads the one rule
// the CLI needs for a value the document never contains (the derived slice name).
//
// A block of regexes lived here — name, size, memory, duration, timeout, rate,
// cron — restating rules the schema already carries as `pattern` keywords. Two
// definitions of one format do not stay in step: this binary accepted
// `nosql: [widgets]` while the platform rejected it, because the local copy
// answered first (#CLI-STANDARDUSAGE-ERF1CV).

// ─── The Driftfile, as the document the platform defines ─────────────

// Manifest is a parsed Driftfile: the validated DOCUMENT plus where it came from.
//
// There are no Go types for `atomic`, `backbone`, `canvas` or anything else the
// format contains, and that is the point (#CLI-STANDARDUSAGE-ERF1CV). A struct is
// a definition of the format, and the platform already publishes one — serving the
// schema exists so this side does not need a second. Seventeen types lived here;
// keeping them meant a Driftfile could be legal on the platform and rejected by
// this binary, which is exactly what happened to `nosql: [widgets]`.
//
// So the CLI holds the file as what it is. It reads the handful of values it
// genuinely acts on locally — a directory to upload, a secret to send — through
// total accessors, and passes everything else through untouched, including keys
// this release has never heard of.
type Manifest struct {
	// doc is the whole shorthand-expanded, schema-validated document.
	doc Node

	// slice is the resource shape: the document root minus the three siblings
	// that are about deployment rather than shape. SelectEnvironment replaces it
	// with the environment-merged result.
	slice Node

	// baseDir is the Driftfile's directory; relative paths resolve against it.
	baseDir string
}

// sliceSiblings are the root keys that are NOT part of the resource shape.
// Everything else at the root is the slice, because Driftfile v2 inlines it.
var sliceSiblings = []string{"environments", "hooks", "tests"}

// Doc is the whole document, including keys this binary has no knowledge of.
func (m *Manifest) Doc() Node { return m.doc }

// Slice is the resolved resource shape — after an environment overlay, if one was
// selected. Accessors are total, which is safe because the schema has already run.
func (m *Manifest) Slice() Node { return m.slice }

// Raw is Doc under its previous name, kept so callers reading the untouched
// document do not have to care which of the two they wanted.
func (m *Manifest) Raw() Node { return m.doc }

// Name is the project/slice name. SelectEnvironment rewrites it to the derived
// per-environment name.
func (m *Manifest) Name() string { return m.slice.Str("name") }

// SetName exists for SelectEnvironment, which derives `<project>-<env>`. Nothing
// else has any business renaming a slice.
func (m *Manifest) SetName(name string) { m.slice["name"] = name }

// BaseDir is the directory the Driftfile was read from.
func (m *Manifest) BaseDir() string { return m.baseDir }

// EnvironmentNames lists the declared environments, sorted.
func (m *Manifest) EnvironmentNames() []string {
	names := m.doc.Keys("environments")
	sort.Strings(names)
	return names
}

// HasEnvironment reports whether `name` is declared.
func (m *Manifest) HasEnvironment(name string) bool {
	return m.doc.Sub("environments").Has(name)
}

// PreDeploy / PostDeploy are the local shell commands run around a deploy.
func (m *Manifest) PreDeploy() []string  { return m.doc.Strings("hooks", "pre_deploy") }
func (m *Manifest) PostDeploy() []string { return m.doc.Strings("hooks", "post_deploy") }

// E2ETests are the commands `drift project test` runs against a local instance.
func (m *Manifest) E2ETests() []string { return m.doc.Strings("tests", "e2e") }

type ParseErrors []string

func (p ParseErrors) Error() string {
	if len(p) == 1 {
		return p[0]
	}
	return fmt.Sprintf("%d validation errors:\n  - %s", len(p), strings.Join(p, "\n  - "))
}

// ParseDriftfile reads a Driftfile from disk, expands shorthands,
// resolves $ENVREF secrets, and validates everything against the
// spec. baseDir is the directory containing the Driftfile and is
// used as the resolution root for relative paths.
func ParseDriftfile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Expand ${VAR} placeholders against the process environment before
	// YAML parsing — the env-aware Driftfile feature. ${VAR} is the
	// staging/prod overlay primitive: typically `slice.name: ${ENV}-myapp`
	// resolved by `drift project deploy --env=prod` setting ENV=prod.
	// Distinct from `$VAR` (no braces) which is the secret-envref shape
	// in slice.backbone.secrets — that path runs later, on already-parsed
	// values, and is unaffected by this substitution.
	expanded, err := substituteBraceVars(data)
	if err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}
	data = expanded

	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Rewrite the documented short forms into their canonical maps. This is a
	// TRANSFORMATION, not a rule: it changes shape and never decides legality,
	// which is why it can run before the only thing that does.
	if err := expandShorthands(&raw); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}

	var m Manifest
	m.baseDir = filepath.Dir(path)

	// The document, which is what the platform defines and therefore what gets
	// validated.
	if err := raw.Decode(&m.doc); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}

	// ONE validation, and it is the only one (#CLI-STANDARDUSAGE-ERF1CV). The
	// platform defines the format; nothing here gets a second opinion on whether
	// a document is legal.
	if errs := validateAgainstSchema(m.doc); len(errs) > 0 {
		return nil, errs
	}

	// The resource shape is the document root minus the three siblings that are
	// about deployment rather than shape — Driftfile v2 inlines the slice.
	// SelectEnvironment replaces this with the environment-merged result.
	m.slice = m.doc.Clone()
	for _, sibling := range sliceSiblings {
		delete(m.slice, sibling)
	}

	// The PLATFORM decides what a legal Driftfile is (#CLI-STANDARDUSAGE-ERF1CV).
	// This runs before the local checks so a structural problem is reported in the
	// schema's terms rather than as whatever the local pass makes of a value it
	// could not decode.
	//
	// A machine that has never fetched the schema gets nil back, and parsing
	// continues on the local checks alone. That is deliberate: refusing to parse
	// would make a never-online CLI useless for `project run`, which needs no
	// platform at all. `drift file lint` states the gap explicitly instead, because
	// there "I validated nothing" must not read as "it is valid".
	if errs := validateAgainstSchema(m.doc); len(errs) > 0 {
		return nil, errs
	}

	// $ENVREF resolution is not validation — it substitutes a value from the
	// deployer's environment, and reports the ones that are not set because a
	// secret silently becoming the literal "$VAR" is a credential that is wrong
	// in a way nothing downstream can detect.
	if err := resolveSecretEnvRefs(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ParseHooks decodes ONLY the `hooks:` block, with no validation and no schema.
//
// The deploy command calls it BEFORE the full parse so a `pre_deploy` build can
// produce artifacts (a canvas/ dist directory, say) that the full parse then
// validates. Hook command strings are left verbatim — `${VAR}` in a command is
// expanded by the shell at run time, not at the YAML layer. A missing or garbled
// `hooks:` block yields nothing, never an error, so a half-built project can still
// run its build step.
func ParseHooks(path string) (pre, post []string) {
	doc, err := readDocument(path)
	if err != nil {
		return nil, nil
	}
	return doc.Strings("hooks", "pre_deploy"), doc.Strings("hooks", "post_deploy")
}

// ParseTests decodes ONLY the `tests:` block, same posture as ParseHooks.
func ParseTests(path string) []string {
	doc, err := readDocument(path)
	if err != nil {
		return nil
	}
	return doc.Strings("tests", "e2e")
}

// readDocument reads a Driftfile as an untyped document with no validation.
// Used by the cheap single-block parses above, which run before the real one.
func readDocument(path string) (Node, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, err
	}
	var doc Node
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return nil, uerr
	}
	return doc, nil
}

// SchemaAvailable reports whether this machine holds the platform's Driftfile
// schema, so a caller that must not silently validate nothing can say so.
func SchemaAvailable() bool {
	sch, err := loadSchema()
	return err == nil && sch != nil
}

// ─── Hooks (cheap pre-build parse) ──────────────────────────────────

// ParseProjectName cheaply decodes ONLY the top-level `name` — no validation,
// no `${VAR}`/`$ENVREF` resolution — so commands that just need the project's
// identity (e.g. `drift project stop`/`logs` finding the container) work without
// the project's secrets being set in the environment.
func ParseProjectName(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return "", fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	if strings.TrimSpace(wrapper.Name) == "" {
		return "", fmt.Errorf("Driftfile has no name")
	}
	return wrapper.Name, nil
}

// ─── Environment selection + merge ──────────────────────────────────

// SelectEnvironment resolves the deploy target for the chosen environment:
// it validates `env` against the declared environments, deep-merges that
// environment's overrides onto the base slice, and derives the slice name.
// It mutates the manifest in place (m.Slice becomes the effective slice) and
// returns the resolved environment name (empty for a single-environment
// project). `explicit` is true when the user named an environment (positional
// arg or --env), which makes "no environments declared" an error rather than
// a silent fall-through.
//
// Resolution:
//   - No environments declared: a bare-name single slice. An explicit env is
//     an error (nothing to select).
//   - env == "": default to `prod`/`production` if present, else error asking
//     the user to pick one.
//   - env names a declared environment: merge + derive name.
//
// Naming: `prod`/`production` (and the no-environments case) deploy under the
// bare project name; every other environment deploys under `<name>-<env>`.
func (m *Manifest) SelectEnvironment(env string, explicit bool) (string, error) {
	names := m.EnvironmentNames()

	if len(names) == 0 {
		if explicit && env != "" {
			return "", fmt.Errorf("this project declares no environments, so it can't deploy %q — add an `environments:` block, or drop the argument to deploy the single slice", env)
		}
		return "", nil
	}

	if env == "" {
		switch {
		case m.HasEnvironment("prod"):
			env = "prod"
		case m.HasEnvironment("production"):
			env = "production"
		default:
			return "", fmt.Errorf("this project declares environments (%s) but no default — pick one: drift project deploy <env>", strings.Join(names, ", "))
		}
	}
	if !m.HasEnvironment(env) {
		return "", fmt.Errorf("unknown environment %q — declared: %s", env, strings.Join(names, ", "))
	}

	base := m.Name()
	m.slice = m.mergeEnvironment(env)

	// The merge re-reads the RAW document, where a `$VAR` secret is still the
	// placeholder ParseDriftfile resolved. Resolve again — exactly once, on
	// unresolved values, the same as for a project with no environments. Without
	// it a deploy ships the literal "$VAR" as the secret's value, which nothing
	// downstream can detect.
	if err := resolveSecretEnvRefs(m); err != nil {
		return "", err
	}

	m.SetName(deriveSliceName(base, env))

	// The one name the schema cannot see: this is DERIVED (`<project>-<env>`) and
	// never appears in the Driftfile, so no document validation catches a long
	// project name plus `-staging` going over the limit.
	//
	// The rule still comes from the platform — NamePattern() reads it out of the
	// fetched schema. Restating it here as a literal regex is what `nameRe` was,
	// and a second copy of a platform rule is what let `nosql: [widgets]` be legal
	// in this binary and illegal on the server. No schema, no check: the server is
	// the final enforcer, and a guess is the thing being removed.
	if re := NamePattern(); re != nil && !re.MatchString(m.Name()) {
		return "", fmt.Errorf("derived slice name %q (project %q + environment %q) is not a "+
			"valid slice name — shorten the project or environment name", m.Name(), base, env)
	}
	return env, nil
}

// deriveSliceName maps (project name, environment) to a slice name. The
// production environment — and a single-environment project — own the bare
// project name; every other environment gets a `-<env>` suffix so its slice
// is a distinct, separately-billed instance.
func deriveSliceName(base, env string) string {
	if env == "" || env == "prod" || env == "production" {
		return base
	}
	return base + "-" + env
}

// mergeEnvironment resolves the slice for `env` by merging that environment's
// overlay onto the base AS A DOCUMENT.
//
// It replaced four hand-written mergers that walked a typed Slice field by field.
// That shape had two bugs, both live and both invisible
// (#CLI-STANDARDUSAGE-T9914R):
//
//   - **A value could not be overridden to zero or false.** Every clause gated on
//     truthiness — `if overlay.DeployHistory != 0` — so `deploy_history: 0` in an
//     environments block was indistinguishable from an absent key and discarded.
//   - **A merge could silently forget.** Overrides were applied clause by clause,
//     so a new Driftfile field was not overridable until someone added one.
//
// DeepMerge keys off PRESENCE, and cannot fail to know about a field because it
// does not know what a field is. There is no decode to follow it any more: the
// result IS the slice.
func (m *Manifest) mergeEnvironment(env string) Node {
	base := m.doc.Clone()
	for _, sibling := range sliceSiblings {
		delete(base, sibling)
	}
	return DeepMerge(base, m.doc.Sub("environments", env))
}

func expandShorthands(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	// Top-level section sugars (atomic-as-list, canvas-as-string/list).
	expandSectionShorthands(doc)

	// environments: expand the bare-list form to a map, then run the same
	// section sugars inside every (non-empty) environment override block.
	if envNode := findChild(doc, "environments"); envNode != nil {
		if envNode.Kind == yaml.SequenceNode {
			expandEnvListToMap(envNode)
		}
		if envNode.Kind == yaml.MappingNode {
			for i := 1; i < len(envNode.Content); i += 2 {
				if body := envNode.Content[i]; body.Kind == yaml.MappingNode {
					expandSectionShorthands(body)
				}
			}
		}
	}

	return nil
}

// expandSectionShorthands rewrites the atomic/canvas section sugars within one
// mapping node (the Driftfile root, or an environment override block).
func expandSectionShorthands(m *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return
	}

	// atomic short form: a sequence becomes { functions: <seq> }.
	if atomicNode := findChild(m, "atomic"); atomicNode != nil && atomicNode.Kind == yaml.SequenceNode {
		wrap := *atomicNode
		atomicNode.Kind = yaml.MappingNode
		atomicNode.Tag = ""
		atomicNode.Style = 0
		atomicNode.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "functions", Tag: "!!str"},
			&wrap,
		}
	}

	// canvas short forms:
	//   string   -> { sites: [string] }
	//   sequence -> { sites: <seq> }
	if canvasNode := findChild(m, "canvas"); canvasNode != nil {
		switch canvasNode.Kind {
		case yaml.ScalarNode:
			path := canvasNode.Value
			canvasNode.Kind = yaml.MappingNode
			canvasNode.Tag = ""
			canvasNode.Style = 0
			canvasNode.Value = ""
			canvasNode.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "sites", Tag: "!!str"},
				{
					Kind: yaml.SequenceNode,
					Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Value: path, Tag: "!!str"},
					},
				},
			}
		case yaml.SequenceNode:
			wrap := *canvasNode
			canvasNode.Kind = yaml.MappingNode
			canvasNode.Tag = ""
			canvasNode.Style = 0
			canvasNode.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "sites", Tag: "!!str"},
				&wrap,
			}
		}
	}
}

// expandEnvListToMap rewrites `environments: [prod, staging]` into the
// canonical `environments: { prod: {}, staging: {} }` — each named environment
// inheriting the base shape unchanged.
func expandEnvListToMap(node *yaml.Node) {
	content := make([]*yaml.Node, 0, len(node.Content)*2)
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			continue // be lenient; a malformed entry surfaces at validation
		}
		content = append(content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: item.Value, Tag: "!!str"},
			&yaml.Node{Kind: yaml.MappingNode}, // empty override body
		)
	}
	node.Kind = yaml.MappingNode
	node.Tag = ""
	node.Style = 0
	node.Content = content
}

// findChild returns the value node for a given key in a mapping node,
// or nil if the key isn't present.
func findChild(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ─── Secret $ENVREF resolution ──────────────────────────────────────

// braceVarRe matches ${NAME} placeholders. NAME is ASCII letters,
// digits, and underscores starting with a letter or underscore — same
// shape as POSIX shell variable names. Unmatched braces (e.g. `${`
// without closing) are left alone.
var braceVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substituteBraceVars replaces every `${VAR}` in data with
// `os.Getenv("VAR")`. Returns an error listing every variable that
// is referenced but not set, so the user sees every gap at once
// instead of fixing them one at a time.
func substituteBraceVars(data []byte) ([]byte, error) {
	missing := map[string]struct{}{}
	out := braceVarRe.ReplaceAllFunc(data, func(match []byte) []byte {
		name := string(braceVarRe.FindSubmatch(match)[1])
		val, ok := os.LookupEnv(name)
		if !ok {
			missing[name] = struct{}{}
			return match
		}
		return []byte(val)
	})
	if len(missing) > 0 {
		var names []string
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("${VAR} placeholders reference unset variables: %s (provide them via the environment, a --secret KEY=value flag, or a .env file next to the Driftfile)", strings.Join(names, ", "))
	}
	return out, nil
}

// secretsMap holds the resolved secrets after $ENVREF substitution.
// The Manifest's Secrets field still holds the *manifest* values
// (with the literal "$NAME" string for envrefs); resolveSecretEnvRefs
// converts them in place. This keeps the SDK's mental model simple:
// after ParseDriftfile, what's in m.Slice().Sub("backbone", "secrets") is what
// the platform should store.
func resolveSecretEnvRefs(m *Manifest) error {
	secrets := m.Slice().Sub("backbone", "secrets")
	if len(secrets) == 0 {
		return nil
	}
	missing := []string{}
	for k, v := range secrets.StrMap() {
		if !strings.HasPrefix(v, "$") {
			continue
		}
		// Quoted "$dollars" is a literal — it would already have lost
		// the quotes by the time we see the string here, so we can't
		// distinguish from a real envref. The spec's escape hatch
		// ("To force a literal that starts with $, quote it") relies
		// on the fact that a literal value is meant to BE that string.
		// We treat all bare $-prefixed values as envrefs.
		envName := strings.TrimPrefix(v, "$")
		envVal, ok := os.LookupEnv(envName)
		if !ok {
			missing = append(missing, fmt.Sprintf("secret %q: environment variable %s is not set", k, envName))
			continue
		}
		secrets[k] = envVal
	}
	if len(missing) > 0 {
		return ParseErrors(missing)
	}
	return nil
}

// resolveBaseDir resolves a possibly-relative path against the
// Driftfile's directory, leaving absolute paths untouched.
func resolveBaseDir(m *Manifest, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	if m.baseDir == "" {
		return rel
	}
	return filepath.Join(m.baseDir, rel)
}

// ResolvePath is the exported sibling of resolveBaseDir, used by the
// run driver after parse to find files referenced by the manifest.
func (m *Manifest) ResolvePath(rel string) string { return resolveBaseDir(m, rel) }
