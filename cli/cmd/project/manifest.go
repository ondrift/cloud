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

// ─── The canonical (post-expansion) shape ────────────────────────────

// Manifest is the parsed Driftfile, after shorthand expansion. The project's
// resource shape is inlined at the top level (the file *is* the project);
// `environments` and `hooks` are optional siblings. Downstream code reads
// m.Slice exactly as before — the only change from v1 is where those keys
// live in the file, not the struct shape they decode into.
type Manifest struct {
	// Slice is the base resource shape, inlined at the Driftfile root: name,
	// retention knobs, and the atomic/backbone/canvas/domains sections. Each
	// environment instantiates a slice from this shape plus its overrides.
	Slice Slice `yaml:",inline"`

	// Environments maps an environment name to a partial slice whose *set*
	// fields override the base when that environment is selected. The bare-list
	// form (`environments: [prod, staging]`) expands to a map of empty bodies.
	// Empty/absent = a single-environment project deployed under its bare name.
	Environments map[string]Slice `yaml:"environments,omitempty"`

	// Hooks are local shell commands run around a deploy (see Hooks).
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Tests are local shell commands `drift project test` runs against a
	// `project run`-started local instance (see Tests).
	Tests Tests `yaml:"tests,omitempty"`

	// baseDir is set after parsing; relative paths in the manifest
	// resolve against it.
	baseDir string `yaml:"-"`

	// raw is the shorthand-expanded document, kept so an environment overlay
	// can be merged as DATA rather than field by field.
	//
	// It exists because a struct cannot express the difference between "absent"
	// and "present and zero", and an override needs that difference: the
	// field-by-field mergers gated on `!= ""` / `!= 0`, so `deploy_history: 0`
	// in an environments block was read as absent and discarded
	// (#CLI-STANDARDUSAGE-T9914R). YAML has key presence; the typed Slice does
	// not, so the merge happens before the decode.
	raw Node `yaml:"-"`
}

// Hooks are shell commands the CLI runs locally around a deploy: pre_deploy
// before anything ships (typically a build/lint), post_deploy after the slice
// is live (typically a smoke test). Commands run in declaration order via the
// shell, from the project root; a non-zero exit aborts. Deliberately NOT a
// pipeline engine — no test stages, env matrices, parallelism, caching, or
// remote execution. Cross-environment orchestration is the user's CI calling
// `drift project deploy` more than once, never a Driftfile concern.
type Hooks struct {
	PreDeploy  []string `yaml:"pre_deploy,omitempty"`
	PostDeploy []string `yaml:"post_deploy,omitempty"`
}

// Tests are shell commands `drift project test` runs once the project is up
// locally (the same instance `drift project run` starts) — e2e/integration
// checks against a real running instance, before anything ships to Drift.
// Commands run in declaration order via the shell, from the project root; a
// non-zero exit fails the run. The instance's local URL rides in as
// DRIFT_TEST_URL so a test command knows where to point (the port is picked
// at runtime, never fixed). Deliberately NOT a pipeline engine — same posture
// as Hooks: no stages, matrices, parallelism, or remote execution.
type Tests struct {
	E2E []string `yaml:"e2e,omitempty"`
}

type Slice struct {
	Name            string `yaml:"name"`
	LogRetention    string `yaml:"log_retention"`
	BackupRetention string `yaml:"backup_retention"`

	Atomic   AtomicSection   `yaml:"atomic"`
	Backbone BackboneSection `yaml:"backbone"`
	Canvas   CanvasSection   `yaml:"canvas"`

	// Domains lists per-slice custom hostnames the slice should answer
	// on (e.g. forms.gemeente.example). Schema-only today; the
	// reconcile path is planned.
	Domains []DomainEntry `yaml:"domains"`
}

// DomainEntry declares one custom hostname for the slice. Verify is
// the ownership-proof method; "dns-txt" is the only mode for v1.
type DomainEntry struct {
	Host     string `yaml:"host"`
	Verify   string `yaml:"verify"`             // "dns-txt" (default for v1)
	Wildcard bool   `yaml:"wildcard,omitempty"` // route every subdomain of Host to this slice
}

type AtomicSection struct {
	FunctionMemory  string        `yaml:"function_memory"`
	FunctionTimeout string        `yaml:"function_timeout"`
	RateLimit       string        `yaml:"rate_limit"`
	DeployHistory   int           `yaml:"deploy_history"` // past deploys kept per function (rollback)
	Functions       []AtomicEntry `yaml:"functions"`

	// Egress declares the slice's outbound network posture.
	// Schema-only today; richer enforcement modes are planned.
	Egress *EgressSection `yaml:"egress,omitempty"`
}

// EgressSection — declares whether the slice's outbound traffic to
// the public internet is open (today's default) or restricted to a
// curated list of hostnames. Private-CIDR exclusion (RFC-1918,
// link-local incl. IMDS, CGNAT) is preserved unconditionally
// regardless of mode.
type EgressSection struct {
	Mode  string   `yaml:"mode"`            // "open" | "allowlist"
	Hosts []string `yaml:"hosts,omitempty"` // e.g. "api.stripe.com", "*.amazonaws.com", "smtp.sendgrid.net:587"
}

type AtomicEntry struct {
	Name    string `yaml:"name"`
	Dir     string `yaml:"dir"`
	Element string `yaml:"element"`
	Cron    string `yaml:"cron"`

	// Alerts is the per-function alerting list. v1: `errors`
	// trigger only; `webhook` notify only.
	Alerts []AlertEntry `yaml:"alerts,omitempty"`
}

// AlertEntry declares one alert on a function. `On` is the trigger
// (`errors` for v1). `Threshold` and `Window` together define when
// the alert fires (e.g. >=1 error over a 5-minute window). `Notify`
// is the destination — `webhook=https://hooks.slack.com/...` for v1.
type AlertEntry struct {
	On        string `yaml:"on"`        // "errors" (v1)
	Threshold int    `yaml:"threshold"` // count of errors in the window
	Window    string `yaml:"window"`    // duration string e.g. "5m"
	Notify    string `yaml:"notify"`    // "webhook=https://..." (v1)
}

type BackboneSection struct {
	BlobMaxSize   string `yaml:"blob_max_size"`
	BlobMaxCount  int    `yaml:"blob_max_count"` // free safety quota (not a price driver)
	QueueMaxDepth int    `yaml:"queue_max_depth"`
	SecretMaxSize string `yaml:"secret_max_size"` // max size of one secret value (e.g. "4KB")
	Locks         int    `yaml:"locks"`           // max concurrent Backbone locks
	// RealtimeConnections caps simultaneous live realtime WebSocket
	// connections across the slice (the live pub/sub primitive). Billed in
	// 50-connection blocks; 0 (omitted) means realtime is off for this slice.
	RealtimeConnections int                   `yaml:"realtime_connections"`
	NoSQL               []NoSQLEntry          `yaml:"nosql"`
	Queues              []QueueEntry          `yaml:"queues"`
	Cache               map[string]CacheEntry `yaml:"cache"`
	Secrets             map[string]string     `yaml:"secrets"`

	// SQL declares per-slice SQLite databases. Each entry becomes a
	// `.db` file.
	SQL []SQLEntry `yaml:"sql,omitempty"`

	// Blobs declares named buckets in the per-slice blob store. Each entry
	// carries its own storage quota — there is no slice-wide blob envelope.
	Blobs []BlobEntry `yaml:"blobs,omitempty"`
}

// SQLEntry declares one SQL database. `Schema` is a path to a SQL
// file with idempotent DDL (`CREATE TABLE IF NOT EXISTS`); it runs
// on every deploy. `Seed` is a path to a SQL file that runs only
// when the database has no user tables yet. `Size` is this database's
// own storage quota (e.g. "32MB") — required, since it both prices and
// is enforced from that value; there is no slice-wide default to fall
// back to.
type SQLEntry struct {
	Name   string `yaml:"name"`
	Size   string `yaml:"size"`
	Schema string `yaml:"schema,omitempty"`
	Seed   string `yaml:"seed,omitempty"`
}

type NoSQLEntry struct {
	Name string `yaml:"name"`
	// Size is this collection's own storage quota (e.g. "50MB") — required,
	// since it both prices and is enforced from that value; there is no
	// slice-wide default to fall back to.
	Size string `yaml:"size"`
	Seed string `yaml:"seed"` // path to JSONL
	// TTL: how long a document lives after its LAST write before the
	// platform deletes it — resets on every update. Duration string
	// (durationRe: <int>[smhd]); empty = kept forever. Per-collection,
	// not per-document.
	TTL string `yaml:"ttl"`
}

// BlobEntry declares one named bucket in the blob store. `Size` is this
// bucket's own storage quota (e.g. "100MB") — required, same reasoning as
// NoSQLEntry.Size/SQLEntry.Size.
type BlobEntry struct {
	Name string `yaml:"name"`
	Size string `yaml:"size"`
}

// QueueEntry declares one queue. `Name` is the only field, and that is the
// whole point of the type: `queues:` used to be a []string while every sibling
// resource list took a string-or-map, so the map form the spec advertises as
// forward-compatible failed with a raw decoder error (#DRIFTFILE-R0TNSP).
//
// Queues have no size — they are FIFOs bounded by backbone.queue_max_depth,
// slice-wide — so unlike nosql/sql/blobs there is nothing a long form needs to
// carry yet. It exists so the promised per-queue options (visibility timeout,
// max-receives) can be ADDED without breaking a single existing manifest, and
// until they are implemented an unknown key is refused rather than ignored.
type QueueEntry struct {
	Name string `yaml:"name"`
}

// CacheEntry is the long-form expansion. Short-form `<key>: <path>`
// expands to {File: <path>}. Short-form `{value: ...}` expands to
// {Value: <inline-value>}.
type CacheEntry struct {
	File  string `yaml:"file"`
	Value string `yaml:"value"`
	TTL   int    `yaml:"ttl"`
}

type CanvasSection struct {
	CanvasSize string        `yaml:"canvas_size"`
	Sites      []CanvasEntry `yaml:"sites"`
}

type CanvasEntry struct {
	Dir   string `yaml:"dir"`
	Route string `yaml:"route"`
}

// ─── Parser ─────────────────────────────────────────────────────────

// ParseErrors aggregates every validation failure in one error so the
// user sees them all at once. Implements `error` so it can flow
// through the cobra RunE return.
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
	if err := raw.Decode(&m.raw); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}

	// ONE validation, and it runs FIRST (#CLI-STANDARDUSAGE-ERF1CV).
	//
	// Ordering is the whole point. The typed decode below still refuses shapes of
	// its own — a bare string where it wants a map — so leaving it in front meant
	// the CLI got first refusal and the schema never spoke. That is how
	// `nosql: [widgets]` came to be accepted by this binary and rejected by the
	// platform: two rule sets, and the local one answered first.
	if errs := validateAgainstSchema(m.raw); len(errs) > 0 {
		return nil, errs
	}

	// Everything past here is a PROJECTION of an already-valid document, never a
	// second opinion on whether it is valid.
	if err := raw.Decode(&m); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
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
	if errs := validateAgainstSchema(m.raw); len(errs) > 0 {
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

// Raw is the Driftfile as a document — shorthand-expanded, schema-validated, and
// carrying every key the file had, including ones this binary has no field for.
//
// It is the read path the typed Slice is being retired in favour of
// (#CLI-STANDARDUSAGE-ERF1CV): the struct can only expose what this release was
// compiled to know, while the document is what the platform actually defines. Its
// accessors are total, which is safe precisely because the schema has already run.
func (m *Manifest) Raw() Node { return m.raw }

// SchemaAvailable reports whether this machine holds the platform's Driftfile
// schema, so a caller that must not silently validate nothing can say so.
func SchemaAvailable() bool {
	sch, err := loadSchema()
	return err == nil && sch != nil
}

// ─── Hooks (cheap pre-build parse) ──────────────────────────────────

// ParseHooks decodes ONLY the `hooks:` block, with no validation and no
// file-existence checks. The deploy command calls it BEFORE the full
// ParseDriftfile so a `pre_deploy` build can produce artifacts (e.g. a
// canvas/ dist directory) that the full parse then validates. Hook command
// strings are left verbatim — `${VAR}` in a command is expanded by the shell
// at run time (the deploy environment is already exported), not at the YAML
// layer. A missing/garbled `hooks:` block yields empty hooks, never an error,
// so a half-built project can still run its build step.
func ParseHooks(path string) (Hooks, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return Hooks{}, fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Hooks Hooks `yaml:"hooks"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return Hooks{}, fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	return wrapper.Hooks, nil
}

// ParseTests decodes ONLY the `tests:` block, with no validation and no
// file-existence checks — same cheap-parse posture as ParseHooks, so `drift
// project test` can check "are any tests even declared" before paying for a
// full build.
func ParseTests(path string) (Tests, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return Tests{}, fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Tests Tests `yaml:"tests"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return Tests{}, fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	return wrapper.Tests, nil
}

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
	if len(m.Environments) == 0 {
		if explicit && env != "" {
			return "", fmt.Errorf("this project declares no environments, so it can't deploy %q — add an `environments:` block, or drop the argument to deploy the single slice", env)
		}
		return "", nil
	}

	names := sortedKeys(m.Environments)
	if env == "" {
		switch {
		case hasKey(m.Environments, "prod"):
			env = "prod"
		case hasKey(m.Environments, "production"):
			env = "production"
		default:
			return "", fmt.Errorf("this project declares environments (%s) but no default — pick one: drift project deploy <env>", strings.Join(names, ", "))
		}
	}

	// The typed map answers "is this a declared environment"; the merge below
	// reads the DOCUMENT, because only the document distinguishes an absent key
	// from one set to zero.
	if _, ok := m.Environments[env]; !ok {
		return "", fmt.Errorf("unknown environment %q — declared: %s", env, strings.Join(names, ", "))
	}

	base := m.Slice.Name
	merged, err := m.mergeEnvironment(env)
	if err != nil {
		return "", err
	}
	m.Slice = merged

	// The merge re-decodes from the RAW document, where a `$VAR` secret is still
	// the placeholder — ParseDriftfile resolved those onto the slice this just
	// replaced. Resolve again, which is not a double-resolve: the merged slice
	// carries unresolved values, so this runs exactly once on them, the same as
	// for a project that declares no environments. Without it a deploy ships the
	// literal "$VAR" as the secret's value, which nothing downstream can detect.
	if err := resolveSecretEnvRefs(m); err != nil {
		return "", err
	}

	m.Slice.Name = deriveSliceName(base, env)

	// The one name the schema cannot see: this is DERIVED (`<project>-<env>`) and
	// never appears in the Driftfile, so no amount of document validation catches
	// a long project name plus `-staging` going over the limit.
	//
	// The rule still comes from the platform — NamePattern() reads it out of the
	// fetched schema. Restating it here as a literal regex is what `nameRe` was,
	// and a second copy of a platform rule is exactly what let `nosql: [widgets]`
	// be legal in this binary and illegal on the server. No schema, no check: the
	// server is the final enforcer, and a guess is the thing being removed.
	if re := NamePattern(); re != nil && !re.MatchString(m.Slice.Name) {
		return "", fmt.Errorf("derived slice name %q (project %q + environment %q) is not a "+
			"valid slice name — shorten the project or environment name", m.Slice.Name, base, env)
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
// overlay onto the base AS A DOCUMENT, then decoding the result.
//
// It replaces four hand-written mergers (mergeSlice/mergeAtomic/mergeBackbone/
// mergeCanvas) that walked the typed Slice field by field. That shape had two
// bugs, both live and both invisible (#CLI-STANDARDUSAGE-T9914R):
//
//   - **A value could not be overridden to zero or false.** Every clause gated on
//     truthiness — `if overlay.DeployHistory != 0` — so `deploy_history: 0` in an
//     environments block was indistinguishable from an absent key and was
//     discarded. The user's Driftfile said one thing and their slice was another,
//     with no error anywhere.
//   - **A merge could silently forget.** Overrides were applied clause by clause,
//     so a new Driftfile field was not overridable until someone remembered to add
//     one. No compiler error, no failing test. mergeCanvas handled two fields;
//     anything else under `canvas:` could not be overridden at all.
//
// Merging the document cannot have either bug: DeepMerge keys off PRESENCE, and it
// does not know what a field is, so it cannot fail to know about one.
//
// The base slice is the document root — Slice is `yaml:",inline"`, so the
// Driftfile's top level IS the slice — minus the three keys that are its siblings
// rather than part of it.
func (m *Manifest) mergeEnvironment(env string) (Slice, error) {
	baseDoc := m.raw.Clone()
	for _, sibling := range []string{"environments", "hooks", "tests"} {
		delete(baseDoc, sibling)
	}

	merged := DeepMerge(baseDoc, m.raw.Sub("environments", env))

	// Round-trip through YAML rather than reaching for reflection: the decode
	// rules that produced m.Slice are the ones that must produce this too, and
	// re-using them is what keeps a merged slice and an unmerged one the same
	// kind of object.
	encoded, err := yaml.Marshal(merged)
	if err != nil {
		return Slice{}, fmt.Errorf("environments.%s: %w", env, err)
	}
	var out Slice
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		return Slice{}, fmt.Errorf("environments.%s: %w", env, err)
	}
	return out, nil
}

// sortedKeys returns the map's keys in deterministic order, for stable
// messages.
func sortedKeys(m map[string]Slice) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func hasKey(m map[string]Slice, k string) bool {
	_, ok := m[k]
	return ok
}

// expandShorthands rewrites every short form documented in the spec into its
// canonical map. With the project-level layout the sections live at the
// Driftfile root (no `slice:` wrapper), and the same section sugars apply
// inside every `environments.<env>` override block:
//
//	atomic: [a, b, c]    -> atomic: { functions: [a, b, c] }
//	canvas: ./path       -> canvas: { sites: [./path] }
//	canvas: [./a, ./b]   -> canvas: { sites: [./a, ./b] }
//
// Plus the environments bare-list sugar:
//
//	environments: [prod, staging] -> environments: { prod: {}, staging: {} }
//
// The per-list short forms inside atomic.functions, canvas.sites, and
// backbone.nosql are handled natively by the element UnmarshalYAML methods.
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

// ─── Custom unmarshalers for mixed-string-or-map list elements ──────

// UnmarshalYAML accepts either a bare-string (function name) or a map
// (the long form with name/dir/element/cron).
func (a *AtomicEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.Name = node.Value
		return nil
	}
	type raw AtomicEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*a = AtomicEntry(r)
	return nil
}

// UnmarshalYAML accepts either a bare-string (queue name) or a map (the long
// form, whose only key today is `name`). Mirrors NoSQLEntry/SQLEntry/BlobEntry
// so every resource list in `backbone:` now takes the same two shapes.
//
// Unlike its siblings this one rejects unknown keys explicitly. The siblings
// can lean on ParseDriftfile's strict KnownFields re-decode, but that strictness
// does not reach INSIDE a custom unmarshaler — `node.Decode` builds a fresh,
// lenient decoder — so without this check `max_receives: 3` would parse, be
// dropped on the floor, and leave the user believing a redelivery cap is in
// force. Refusing a knob nothing implements is the honest half of promising it
// can exist later.
func (q *QueueEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		q.Name = node.Value
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; key != "name" {
			return fmt.Errorf("backbone.queues: %q is not a supported queue option "+
				"(only `name` is; per-queue options are not implemented yet)", key)
		}
	}
	type raw QueueEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*q = QueueEntry(r)
	return nil
}

// UnmarshalYAML accepts either a bare-string (canvas directory) or a
// map (the long form with dir/route).
func (c *CanvasEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.Dir = node.Value
		return nil
	}
	type raw CanvasEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*c = CanvasEntry(r)
	return nil
}

// UnmarshalYAML for cache map values accepts either a bare-string
// (file path) or a map (the long form with value/ttl).
func (c *CacheEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.File = node.Value
		return nil
	}
	type raw CacheEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*c = CacheEntry(r)
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
// after ParseDriftfile, what's in m.Slice.Backbone.Secrets is what
// the platform should store.
func resolveSecretEnvRefs(m *Manifest) error {
	if m.Slice.Backbone.Secrets == nil {
		return nil
	}
	missing := []string{}
	for k, v := range m.Slice.Backbone.Secrets {
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
		m.Slice.Backbone.Secrets[k] = envVal
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
