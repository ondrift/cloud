package project

// driftfile.go — the Driftfile: HCL, read into blocks.
//
// A Driftfile is one HCL file that declares the whole slice: what runs on it
// (elements, their Pulses and Fields, Backbone, sites, edge) AND its shape (each
// booking beside what it books, each storage size beside what it sizes, and the
// slice's limits).
//
// This file reads the TEXT: its blocks and the small expression language.
// compile.go turns the blocks into the two payloads, the flattened Pulse/Field
// document every reader consumes and the SliceConfig the platform prices.
// plan.go diffs those against the live slice.
//
// # A YAML Driftfile is refused, by its first tokens
//
// The Driftfile was YAML before it was HCL, and a YAML file is not read. It is
// recognised by its first tokens, a `key:` pair, which no HCL file begins
// with, and refused with one message saying the format changed. Recognising it
// by the tokens rather than by a failed parse means the person reads why the
// file is not accepted, instead of an HCL syntax error about a colon.
//
// # Expressions stay small
//
// Literals, `var.<name>`, and three functions: env(), file() and sha256(). No
// count, no for_each, no modules. Every other function is refused by HCL itself
// ("there is no function named ...") because the evaluation context offers no
// other.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// isYAML reports whether a Driftfile's text is YAML: its first tokens, past
// comments and blank lines, are a `key:` pair, or a `---` document marker.
// HCL begins with a block or an attribute, never with either.
func isYAML(data []byte) bool {
	toks, _ := hclsyntax.LexConfig(data, driftfileName, hcl.InitialPos)
	var sig []hclsyntax.Token
	for _, t := range toks {
		if t.Type == hclsyntax.TokenNewline || t.Type == hclsyntax.TokenComment {
			continue
		}
		sig = append(sig, t)
		if len(sig) == 3 {
			break
		}
	}
	switch {
	case len(sig) >= 2 && sig[0].Type == hclsyntax.TokenIdent && sig[1].Type == hclsyntax.TokenColon:
		return true
	case len(sig) == 3 && sig[0].Type == hclsyntax.TokenMinus && sig[1].Type == hclsyntax.TokenMinus &&
		sig[2].Type == hclsyntax.TokenMinus:
		return true
	}
	return false
}

// RefuseYAML is refuseYAML for a reader outside this package: the refusal
// when data is YAML, and nil otherwise.
func RefuseYAML(path string, data []byte) error {
	if isYAML(data) {
		return refuseYAML(path)
	}
	return nil
}

// refuseYAML is the one answer to a YAML Driftfile, wherever it is read.
func refuseYAML(path string) error {
	return fmt.Errorf("%s is written in YAML, and the Driftfile is HCL now: this CLI reads no YAML Driftfile.\n"+
		"  Rewrite it in HCL. `drift file new` writes a starter, and `drift file explain` lists every block it can hold",
		filepath.Base(path))
}

// ─── the blocks ─────────────────────────────────────────────────────────────
//
// Every attribute is an hcl.Expression rather than a Go type, so the compiler
// evaluates it itself and every diagnostic it raises can point at the
// attribute's own line and column. gohcl still owns the STRUCTURE: an unknown
// block or attribute, a missing or extra label, and a duplicated attribute are
// refused by it, at their positions.

type dfFile struct {
	Slice        []dfSlice       `hcl:"slice,block"`
	Atomic       []dfAtomic      `hcl:"atomic,block"`
	Backbone     []dfBackbone    `hcl:"backbone,block"`
	Canvas       []dfCanvas      `hcl:"canvas,block"`
	Edge         []dfEdge        `hcl:"edge,block"`
	Variables    []dfVariable    `hcl:"variable,block"`
	Environments []dfEnvironment `hcl:"environment,block"`
	Hooks        []dfHook        `hcl:"hook,block"`
	Tests        []dfTest        `hcl:"test,block"`

	// Elements, SQL, NoSQL, Blobs, Queues, Caches, Secrets and Sites carry no
	// hcl tag: gohcl never decodes them directly. Each pillar's own blocks are
	// written inside its container now — `atomic { element ... }`, `backbone {
	// sql ... }`, `canvas { site ... }` — and flattenPillars copies them here
	// right after decode, so compile.go reads exactly what it always has.
	Elements []dfElement
	SQL      []dfSQL
	NoSQL    []dfNoSQL
	Blobs    []dfBlobs
	Queues   []dfQueue
	Caches   []dfCache
	Secrets  []dfSecret
	Realtime []dfRealtime
	Sites    []dfSite
}

// pillarOfBlock names the container each pillar block is written inside.
var pillarOfBlock = map[string]string{
	"element":  "atomic",
	"sql":      "backbone",
	"nosql":    "backbone",
	"blobs":    "backbone",
	"queue":    "backbone",
	"cache":    "backbone",
	"secret":   "backbone",
	"realtime": "backbone",
	"site":     "canvas",
}

// dfAtomic is the Atomic pillar's own container: every element it declares.
type dfAtomic struct {
	Elements []dfElement `hcl:"element,block"`
	DefRange hcl.Range   `hcl:",def_range"`
}

// dfBackbone is the Backbone pillar's own container: every primitive it
// declares.
type dfBackbone struct {
	SQL      []dfSQL      `hcl:"sql,block"`
	NoSQL    []dfNoSQL    `hcl:"nosql,block"`
	Blobs    []dfBlobs    `hcl:"blobs,block"`
	Queues   []dfQueue    `hcl:"queue,block"`
	Caches   []dfCache    `hcl:"cache,block"`
	Secrets  []dfSecret   `hcl:"secret,block"`
	Realtime []dfRealtime `hcl:"realtime,block"`
	DefRange hcl.Range    `hcl:",def_range"`
}

// dfCanvas is the Canvas pillar's own container: every site it declares.
type dfCanvas struct {
	Sites    []dfSite  `hcl:"site,block"`
	DefRange hcl.Range `hcl:",def_range"`
}

// flattenPillars copies each pillar container's blocks onto dfFile's own
// fields, so everything past this point (compile.go) reads the pillars the
// same way regardless of the container syntax above it. A second container of
// the same kind is refused here, the same way compile.go refuses a second
// slice or edge block.
func flattenPillars(file *dfFile, ev *dfEval) {
	switch len(file.Atomic) {
	case 0:
	case 1:
		file.Elements = file.Atomic[0].Elements
	default:
		ev.errorf(file.Atomic[1].DefRange, "Duplicate atomic block", "a Driftfile has one atomic block.")
	}
	switch len(file.Backbone) {
	case 0:
	case 1:
		b := file.Backbone[0]
		file.SQL, file.NoSQL, file.Blobs = b.SQL, b.NoSQL, b.Blobs
		file.Queues, file.Caches, file.Secrets, file.Realtime = b.Queues, b.Caches, b.Secrets, b.Realtime
	default:
		ev.errorf(file.Backbone[1].DefRange, "Duplicate backbone block", "a Driftfile has one backbone block.")
	}
	switch len(file.Canvas) {
	case 0:
	case 1:
		file.Sites = file.Canvas[0].Sites
	default:
		ev.errorf(file.Canvas[1].DefRange, "Duplicate canvas block", "a Driftfile has one canvas block.")
	}
}

type dfSlice struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Billing   hcl.Expression `hcl:"billing,optional" format:"-"`
	Labels    hcl.Expression `hcl:"labels,optional"`
	Limits    []dfLimits     `hcl:"limits,block"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

// dfLimits is the slice-wide part of the shape: everything a slice has that
// no single block in the file owns. Each attribute maps to one SliceConfig
// scalar; compile.go's shapeLimits table is where that mapping lives.
type dfLimits struct {
	RequestsPerMinute   hcl.Expression `hcl:"requests_per_minute,optional"`
	Timeout             hcl.Expression `hcl:"timeout,optional"`
	LogRetention        hcl.Expression `hcl:"log_retention,optional"`
	DeployHistory       hcl.Expression `hcl:"deploy_history,optional"`
	BackupRetention     hcl.Expression `hcl:"backup_retention,optional"`
	ScheduledJobs       hcl.Expression `hcl:"scheduled_jobs,optional"`
	AtomicStorage       hcl.Expression `hcl:"atomic_storage,optional"`
	CanvasStorage       hcl.Expression `hcl:"canvas_storage,optional"`
	RealtimeConnections hcl.Expression `hcl:"realtime_connections,optional"`
	Secrets             hcl.Expression `hcl:"secrets,optional"`
	SecretSize          hcl.Expression `hcl:"secret_size,optional"`
	Locks               hcl.Expression `hcl:"locks,optional"`
	BlobObjectSize      hcl.Expression `hcl:"blob_object_size,optional"`
	QueueDepth          hcl.Expression `hcl:"queue_depth,optional"`
	VaultEntrySize      hcl.Expression `hcl:"vault_entry_size,optional"`
	VaultEntries        hcl.Expression `hcl:"vault_entries,optional"`
	PocketRecordSize    hcl.Expression `hcl:"pocket_record_size,optional"`
	DefRange            hcl.Range      `hcl:",def_range"`
}

type dfElement struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	Pulses    []dfPulse      `hcl:"pulse,block"`
	Fields    []dfField      `hcl:"field,block"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfPulse struct {
	Label      string         `hcl:"trigger,label"`
	LabelRange hcl.Range      `hcl:"trigger,label_range"`
	Handler    hcl.Expression `hcl:"handler,optional"`
	Response   hcl.Expression `hcl:"response,optional"`
	Concurrent hcl.Expression `hcl:"concurrent,optional"`
	Stream     hcl.Expression `hcl:"stream,optional"`
	Cron       hcl.Expression `hcl:"cron,optional"`
	Env        hcl.Expression `hcl:"env,optional"`
	Secrets    hcl.Expression `hcl:"secrets,optional"`
	Memory     hcl.Expression `hcl:"memory,optional"`
	Dir        hcl.Expression `hcl:"dir,optional"`
	Auth       []dfAuth       `hcl:"auth,block"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type dfAuth struct {
	APIKey   hcl.Expression `hcl:"api_key,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfField struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Handler   hcl.Expression `hcl:"handler,optional"`
	Memory    hcl.Expression `hcl:"memory,optional"`
	Env       hcl.Expression `hcl:"env,optional"`
	Secrets   hcl.Expression `hcl:"secrets,optional"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfSQL struct {
	Name       string         `hcl:"name,label"`
	NameRange  hcl.Range      `hcl:"name,label_range"`
	Size       hcl.Expression `hcl:"size,optional"`
	Schema     hcl.Expression `hcl:"schema,optional"`
	Migrations hcl.Expression `hcl:"migrations,optional"`
	Seed       hcl.Expression `hcl:"seed,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type dfNoSQL struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Size      hcl.Expression `hcl:"size,optional"`
	Seed      hcl.Expression `hcl:"seed,optional"`
	TTL       hcl.Expression `hcl:"ttl,optional"`
	Unique    hcl.Expression `hcl:"unique,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfBlobs struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Size      hcl.Expression `hcl:"size,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfQueue struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Depth     hcl.Expression `hcl:"depth,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfCache struct {
	Key      string         `hcl:"key,label"`
	File     hcl.Expression `hcl:"file,optional"`
	Value    hcl.Expression `hcl:"value,optional"`
	TTL      hcl.Expression `hcl:"ttl,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfSecret struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Value     hcl.Expression `hcl:"value,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfRealtime struct {
	ClientChannels hcl.Expression `hcl:"client_channels,optional"`
	DefRange       hcl.Range      `hcl:",def_range"`
}

type dfSite struct {
	Path      string         `hcl:"path,label"`
	PathRange hcl.Range      `hcl:"path,label_range"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfEdge struct {
	Egress    []dfEgress     `hcl:"egress,block"`
	Domains   []dfDomain     `hcl:"domain,block"`
	RateLimit hcl.Expression `hcl:"rate_limit,optional"`
	SiteAuth  []dfSiteAuth   `hcl:"site_auth,block"`
	Links     hcl.Expression `hcl:"links,optional"`
	Allow     hcl.Expression `hcl:"allow,optional"`
	Hosts     hcl.Expression `hcl:"hosts,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfEgress struct {
	Mode     hcl.Expression `hcl:"mode,optional"`
	Hosts    hcl.Expression `hcl:"hosts,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfDomain struct {
	Host     string         `hcl:"host,label"`
	Wildcard hcl.Expression `hcl:"wildcard,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfSiteAuth struct {
	Realm    hcl.Expression `hcl:"realm,optional"`
	Paths    hcl.Expression `hcl:"paths,optional"`
	Users    []dfUser       `hcl:"user,block"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfUser struct {
	Name     string         `hcl:"name,label"`
	Password hcl.Expression `hcl:"password,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfVariable struct {
	Name     string         `hcl:"name,label"`
	Default  hcl.Expression `hcl:"default,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type dfEnvironment struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Slice     hcl.Expression `hcl:"slice,optional"`
	Billing   hcl.Expression `hcl:"billing,optional" format:"-"`
	Vars      hcl.Expression `hcl:"vars,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type dfHook struct {
	Phase      string         `hcl:"phase,label"`
	PhaseRange hcl.Range      `hcl:"phase,label_range"`
	Run        hcl.Expression `hcl:"run,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type dfTest struct {
	Name     string         `hcl:"name,label"`
	Run      hcl.Expression `hcl:"run,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

// ─── reading the text ───────────────────────────────────────────────────────

// readDriftfile parses a Driftfile into its blocks and the evaluation context
// its expressions run in, for the default slice.
func readDriftfile(path string, data []byte) (*dfFile, *dfEval, error) {
	return readDriftfileEnv(path, data, "")
}

// selectedEnv is the environment a file is read for: its name and the slice it
// is applied to.
type selectedEnv struct {
	name, slice string
	sliceRange  hcl.Range
	hasSlice    bool
}

// readDriftfileEnv parses a Driftfile for one environment, or for the default
// slice when env is "". The context needs the variables, and the variables are
// themselves blocks, so they are read first on their own; an environment sets
// variables, so the environments are read with them.
//
// A variable's value is the selected environment's, else its default. One
// with neither is an error at the variable, naming where it could be set.
func readDriftfileEnv(path string, data []byte, env string) (*dfFile, *dfEval, error) {
	if isYAML(data) {
		return nil, nil, refuseYAML(path)
	}
	name := filepath.Base(path)
	f, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, nil, diagError(diags)
	}
	body := f.Body.(*hclsyntax.Body)

	ev := &dfEval{dir: filepath.Dir(path), file: name}

	// A pillar's own block written at the top of the file says which block it
	// belongs in, rather than the reader's "not expected here".
	for _, blk := range body.Blocks {
		if pillar, ok := pillarOfBlock[blk.Type]; ok {
			ev.errorf(blk.TypeRange, "Block belongs inside "+pillar,
				"%s blocks are written inside the %s block: %s { %s ... }.", blk.Type, pillar, pillar, blk.Type)
		}
	}
	if ev.diags.HasErrors() {
		return nil, nil, diagError(ev.diags)
	}

	// Variables and environments first, with no `var` of their own: a default
	// or an environment's value is a literal or an env()/file() call, and one
	// variable cannot read another.
	vschema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "environment", LabelNames: []string{"name"}},
	}}
	vcontent, _, _ := f.Body.PartialContent(vschema)
	ev.ctx = &hcl.EvalContext{Functions: ev.functions()}

	// The selected environment, and what it sets.
	var declaredEnvs []string
	envVars := map[string]cty.Value{}
	for _, blk := range vcontent.Blocks {
		if blk.Type != "environment" {
			continue
		}
		declaredEnvs = append(declaredEnvs, blk.Labels[0])
		if blk.Labels[0] != env || env == "" {
			continue
		}
		var e dfEnvironment
		if d := gohcl.DecodeBody(blk.Body, ev.ctx, &e); d.HasErrors() {
			ev.diags = append(ev.diags, d...)
			continue
		}
		sel := &selectedEnv{name: env}
		if s, ok := ev.str(e.Slice, fmt.Sprintf("environment %q: slice", env)); ok {
			sel.slice, sel.hasSlice, sel.sliceRange = s, true, e.Slice.Range()
		}
		if present(e.Vars) {
			v, d := e.Vars.Value(ev.ctx)
			if d.HasErrors() {
				ev.diags = append(ev.diags, d...)
			} else if v.CanIterateElements() {
				for it := v.ElementIterator(); it.Next(); {
					k, el := it.Element()
					envVars[k.AsString()] = el
				}
			}
		}
		ev.env = sel
	}
	if env != "" && ev.env == nil && !ev.diags.HasErrors() {
		sort.Strings(declaredEnvs)
		detail := "this file declares no environment block"
		if len(declaredEnvs) > 0 {
			detail = "the file declares " + strings.Join(declaredEnvs, ", ")
		}
		ev.errorf(body.Range(), "Unknown environment", "there is no environment %q: %s.", env, detail)
	}

	vars := map[string]cty.Value{}
	for _, blk := range vcontent.Blocks {
		if blk.Type != "variable" {
			continue
		}
		var v dfVariable
		if d := gohcl.DecodeBody(blk.Body, ev.ctx, &v); d.HasErrors() {
			ev.diags = append(ev.diags, d...)
			continue
		}
		v.Name = blk.Labels[0]
		if _, dup := vars[v.Name]; dup {
			ev.errorf(blk.DefRange, "Duplicate variable", "variable %q is declared twice.", v.Name)
			continue
		}
		if set, ok := envVars[v.Name]; ok {
			vars[v.Name] = set
			continue
		}
		val, d := v.Default.Value(ev.ctx)
		ev.diags = append(ev.diags, d...)
		if !d.HasErrors() && val.IsNull() {
			where := "give it a default"
			switch {
			case env != "":
				where = fmt.Sprintf("give it a default, or set it in environment %q's vars", env)
			case len(declaredEnvs) > 0:
				where = "give it a default, or plan and apply an environment that sets it"
			}
			ev.errorf(blk.DefRange, "Variable has no value", "variable %q has no value here: %s.", v.Name, where)
			continue
		}
		vars[v.Name] = val
	}
	if ev.diags.HasErrors() {
		return nil, nil, diagError(ev.diags)
	}
	ev.ctx = &hcl.EvalContext{
		Variables: map[string]cty.Value{"var": cty.ObjectVal(vars)},
		Functions: ev.functions(),
	}
	ev.vars = vars

	var file dfFile
	if d := gohcl.DecodeBody(f.Body, ev.ctx, &file); d.HasErrors() {
		return nil, nil, diagError(d)
	}
	flattenPillars(&file, ev)
	if ev.diags.HasErrors() {
		return nil, nil, diagError(ev.diags)
	}
	return &file, ev, nil
}

// ─── evaluation ─────────────────────────────────────────────────────────────

// dfEval evaluates attribute expressions and collects every diagnostic, so a
// file with five mistakes is reported once with all five.
type dfEval struct {
	ctx *hcl.EvalContext
	dir string
	// file is the Driftfile's name, for a diagnostic that belongs to the whole
	// file rather than to one block in it.
	file  string
	vars  map[string]cty.Value
	diags hcl.Diagnostics
	// env is the environment the file is read for; nil for the default slice.
	env *selectedEnv
}

func (e *dfEval) errorf(rng hcl.Range, summary, format string, args ...any) {
	r := rng
	e.diags = append(e.diags, &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   fmt.Sprintf(format, args...),
		Subject:  &r,
	})
}

// present reports whether an optional attribute was written at all. gohcl
// hands an absent one over as a static null.
func present(expr hcl.Expression) bool {
	if expr == nil {
		return false
	}
	if _, synthetic := expr.(hclsyntax.Expression); !synthetic {
		v, d := expr.Value(nil)
		return d.HasErrors() || !v.IsNull()
	}
	return true
}

// value evaluates an attribute converted to want, and reports whether it was
// written. An error is recorded at the attribute and reads as absent.
func (e *dfEval) value(expr hcl.Expression, want cty.Type, what string) (cty.Value, bool) {
	if !present(expr) {
		return cty.NilVal, false
	}
	v, d := expr.Value(e.ctx)
	if d.HasErrors() {
		e.diags = append(e.diags, d...)
		return cty.NilVal, false
	}
	if v.IsNull() {
		return cty.NilVal, false
	}
	cv, err := convert.Convert(v, want)
	if err != nil {
		e.errorf(expr.Range(), "Wrong type", "%s: %s.", what, err.Error())
		return cty.NilVal, false
	}
	if !cv.IsWhollyKnown() {
		e.errorf(expr.Range(), "Unknown value", "%s cannot be known before apply.", what)
		return cty.NilVal, false
	}
	return cv, true
}

func (e *dfEval) str(expr hcl.Expression, what string) (string, bool) {
	v, ok := e.value(expr, cty.String, what)
	if !ok {
		return "", false
	}
	return v.AsString(), true
}

func (e *dfEval) integer(expr hcl.Expression, what string) (int, bool) {
	v, ok := e.value(expr, cty.Number, what)
	if !ok {
		return 0, false
	}
	bf := v.AsBigFloat()
	if !bf.IsInt() {
		e.errorf(expr.Range(), "Not a whole number", "%s must be a whole number.", what)
		return 0, false
	}
	n, _ := bf.Int64()
	return int(n), true
}

func (e *dfEval) boolean(expr hcl.Expression, what string) (bool, bool) {
	v, ok := e.value(expr, cty.Bool, what)
	if !ok {
		return false, false
	}
	return v.True(), true
}

func (e *dfEval) strList(expr hcl.Expression, what string) ([]string, bool) {
	v, ok := e.value(expr, cty.List(cty.String), what)
	if !ok {
		return nil, false
	}
	out := []string{}
	for it := v.ElementIterator(); it.Next(); {
		_, el := it.Element()
		out = append(out, el.AsString())
	}
	return out, true
}

func (e *dfEval) strMap(expr hcl.Expression, what string) (map[string]string, bool) {
	v, ok := e.value(expr, cty.Map(cty.String), what)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	for it := v.ElementIterator(); it.Next(); {
		k, el := it.Element()
		out[k.AsString()] = el.AsString()
	}
	return out, true
}

// envReference reads a WRITE-ONLY value: a secret, an API key, a site
// password. It must be written as `env("NAME")` and nothing else, so the file
// can only ever hold a reference. A literal is refused: it would be a
// credential committed with the file. var.x and file() are refused for the
// same reason, since both put the value itself in the repository.
//
// The value is not read here. A plan compares write-only values by their
// presence and, where the platform shows one, a fingerprint; it never needs
// the value in the document, and keeping it out keeps it out of every output.
func (e *dfEval) envReference(expr hcl.Expression, what string) (string, bool) {
	if !present(expr) {
		return "", false
	}
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "env" || len(call.Args) != 1 {
		e.errorf(expr.Range(), "A secret is a reference",
			`%s is write-only and must be written env("NAME"): a literal here would be a credential committed with this file.`, what)
		return "", false
	}
	arg, d := call.Args[0].Value(nil)
	if d.HasErrors() || arg.IsNull() || !arg.Type().Equals(cty.String) {
		e.errorf(call.Args[0].Range(), "A secret is a reference",
			`%s: env() takes the variable's name as a literal string, env("NAME").`, what)
		return "", false
	}
	name := arg.AsString()
	if !isEnvName(name) {
		e.errorf(call.Args[0].Range(), "Not a variable name",
			"%s: %q is not an environment variable name.", what, name)
		return "", false
	}
	return name, true
}

// functions is the whole function library: env(), file() and sha256().
func (e *dfEval) functions() map[string]function.Function {
	return map[string]function.Function{
		// env reads a variable through today's precedence: the terminal, then
		// --var/--secret, then .env.<env>, then .env. applyVariableSources has
		// already layered the lower tiers into the process environment, so the
		// lookup sees the highest one that is set.
		"env": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "name", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				name := args[0].AsString()
				v, ok := os.LookupEnv(name)
				if !ok {
					return cty.NilVal, fmt.Errorf("environment variable %s is not set (set it in the terminal, with --var %s=..., or in a .env file beside the Driftfile)", name, name)
				}
				return cty.StringVal(v), nil
			},
		}),
		// file reads a file relative to the Driftfile, as text.
		"file": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "path", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				p := args[0].AsString()
				if !filepath.IsAbs(p) {
					p = filepath.Join(e.dir, p)
				}
				raw, err := os.ReadFile(p) // #nosec G304 — a file the user's own Driftfile names
				if err != nil {
					return cty.NilVal, fmt.Errorf("read %s: %w", args[0].AsString(), err)
				}
				if !utf8.Valid(raw) {
					return cty.NilVal, fmt.Errorf("%s is not UTF-8 text", args[0].AsString())
				}
				return cty.StringVal(string(raw)), nil
			},
		}),
		// sha256 is the hex SHA-256 of a string, e.g. sha256(file("seed.sql")).
		"sha256": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "text", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				sum := sha256.Sum256([]byte(args[0].AsString()))
				return cty.StringVal(hex.EncodeToString(sum[:])), nil
			},
		}),
	}
}

// ─── diagnostics ────────────────────────────────────────────────────────────

// diagError renders HCL diagnostics as the ParseErrors every Driftfile caller
// already prints, each one carrying its file:line:column.
func diagError(diags hcl.Diagnostics) error {
	var out ParseErrors
	seen := map[string]bool{}
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		msg := d.Summary
		if d.Detail != "" {
			msg += ": " + strings.TrimSuffix(d.Detail, ".")
		}
		if d.Subject != nil {
			msg = fmt.Sprintf("%s:%d:%d: %s", d.Subject.Filename, d.Subject.Start.Line, d.Subject.Start.Column, msg)
		}
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return positionLess(out[i], out[j]) })
	return out
}

// positionLess orders rendered diagnostics by line, then column, so a file's
// errors read top to bottom.
func positionLess(a, b string) bool {
	la, ca := diagPosition(a)
	lb, cb := diagPosition(b)
	if la != lb {
		return la < lb
	}
	return ca < cb
}

var diagPosRe = regexp.MustCompile(`^[^:]*:(\d+):(\d+):`)

func diagPosition(s string) (int, int) {
	m := diagPosRe.FindStringSubmatch(s)
	if m == nil {
		return 1 << 30, 0
	}
	var l, c int
	fmt.Sscanf(m[1]+" "+m[2], "%d %d", &l, &c)
	return l, c
}
