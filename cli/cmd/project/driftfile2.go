package project

// driftfile2.go — Driftfile 2: the HCL format, read into blocks.
//
// A Driftfile 2 is one HCL file that declares the whole slice: what runs on it
// (elements, their Pulses and Fields, Backbone, sites, edge) AND its shape (each
// booking beside what it books, each storage size beside what it sizes, and the
// slice's limits). The YAML 1.x format keeps working beside it, unchanged.
//
// This file reads the TEXT: which format a file is, its blocks, and the small
// expression language. compile2.go turns the blocks into today's two payloads,
// the flattened Pulse/Field document every 1.x reader consumes and the
// SliceConfig the platform prices. plan2.go diffs those against the live slice.
//
// # Detection is by the file, never by a flag
//
// A file is format 2 when its first tokens are `drift {`. A YAML 1.x Driftfile
// is a mapping and its first line is a `key:` pair, so it can never begin that
// way; anything else is read as YAML exactly as before. Detecting on the tokens
// rather than on a clean parse means a format-2 file with a syntax error is
// reported as HCL, at its line and column, instead of as a YAML error about a
// file that was never meant to be YAML.
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

// Format2Implemented is the Driftfile 2 format this binary reads. A file
// declaring a later MINOR is still read, with a notice; a later MAJOR is not
// format 2 at all.
const Format2Implemented = "2.0"

// format2Re is what `drift { format = ... }` must say for a file to be read
// as format 2.
var format2Re = regexp.MustCompile(`^2\.(0|[1-9][0-9]*)$`)

// IsFormat2 reports whether a Driftfile's text is format 2: its first tokens,
// past comments and blank lines, are `drift {`.
func IsFormat2(data []byte) bool {
	toks, _ := hclsyntax.LexConfig(data, driftfileName, hcl.InitialPos)
	var sig []hclsyntax.Token
	for _, t := range toks {
		if t.Type == hclsyntax.TokenNewline || t.Type == hclsyntax.TokenComment {
			continue
		}
		sig = append(sig, t)
		if len(sig) == 2 {
			break
		}
	}
	return len(sig) == 2 &&
		sig[0].Type == hclsyntax.TokenIdent && string(sig[0].Bytes) == "drift" &&
		sig[1].Type == hclsyntax.TokenOBrace
}

// ─── the blocks ─────────────────────────────────────────────────────────────
//
// Every attribute is an hcl.Expression rather than a Go type, so the compiler
// evaluates it itself and every diagnostic it raises can point at the
// attribute's own line and column. gohcl still owns the STRUCTURE: an unknown
// block or attribute, a missing or extra label, and a duplicated attribute are
// refused by it, at their positions.

type df2File struct {
	Drift        []df2Drift       `hcl:"drift,block"`
	Slice        []df2Slice       `hcl:"slice,block"`
	Elements     []df2Element     `hcl:"element,block"`
	SQL          []df2SQL         `hcl:"sql,block"`
	NoSQL        []df2NoSQL       `hcl:"nosql,block"`
	Blobs        []df2Blobs       `hcl:"blobs,block"`
	Queues       []df2Queue       `hcl:"queue,block"`
	Caches       []df2Cache       `hcl:"cache,block"`
	Secrets      []df2Secret      `hcl:"secret,block"`
	Sites        []df2Site        `hcl:"site,block"`
	Edge         []df2Edge        `hcl:"edge,block"`
	Variables    []df2Variable    `hcl:"variable,block"`
	Environments []df2Environment `hcl:"environment,block"`
	Hooks        []df2Hook        `hcl:"hook,block"`
	Tests        []df2Test        `hcl:"test,block"`
}

type df2Drift struct {
	Format   hcl.Expression `hcl:"format,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Slice struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Billing   hcl.Expression `hcl:"billing,optional"`
	Labels    hcl.Expression `hcl:"labels,optional"`
	Limits    []df2Limits    `hcl:"limits,block"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

// df2Limits is the slice-wide part of the shape: everything a slice has that
// no single block in the file owns. Each attribute maps to one SliceConfig
// scalar; compile2.go's shapeLimits table is where that mapping lives.
type df2Limits struct {
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

type df2Element struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	Pulses    []df2Pulse     `hcl:"pulse,block"`
	Fields    []df2Field     `hcl:"field,block"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Pulse struct {
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
	Auth       []df2Auth      `hcl:"auth,block"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type df2Auth struct {
	APIKey   hcl.Expression `hcl:"api_key,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Field struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Handler   hcl.Expression `hcl:"handler,optional"`
	Memory    hcl.Expression `hcl:"memory,optional"`
	Env       hcl.Expression `hcl:"env,optional"`
	Secrets   hcl.Expression `hcl:"secrets,optional"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2SQL struct {
	Name       string         `hcl:"name,label"`
	NameRange  hcl.Range      `hcl:"name,label_range"`
	Size       hcl.Expression `hcl:"size,optional"`
	Schema     hcl.Expression `hcl:"schema,optional"`
	Migrations hcl.Expression `hcl:"migrations,optional"`
	Seed       hcl.Expression `hcl:"seed,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type df2NoSQL struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Size      hcl.Expression `hcl:"size,optional"`
	Seed      hcl.Expression `hcl:"seed,optional"`
	TTL       hcl.Expression `hcl:"ttl,optional"`
	Unique    hcl.Expression `hcl:"unique,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Blobs struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Size      hcl.Expression `hcl:"size,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Queue struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Depth     hcl.Expression `hcl:"depth,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Cache struct {
	Key      string         `hcl:"key,label"`
	File     hcl.Expression `hcl:"file,optional"`
	Value    hcl.Expression `hcl:"value,optional"`
	TTL      hcl.Expression `hcl:"ttl,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Secret struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Value     hcl.Expression `hcl:"value,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Site struct {
	Path      string         `hcl:"path,label"`
	PathRange hcl.Range      `hcl:"path,label_range"`
	Dir       hcl.Expression `hcl:"dir,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Edge struct {
	Egress    []df2Egress    `hcl:"egress,block"`
	Domains   []df2Domain    `hcl:"domain,block"`
	RateLimit hcl.Expression `hcl:"rate_limit,optional"`
	SiteAuth  []df2SiteAuth  `hcl:"site_auth,block"`
	Links     hcl.Expression `hcl:"links,optional"`
	Allow     hcl.Expression `hcl:"allow,optional"`
	Hosts     hcl.Expression `hcl:"hosts,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Egress struct {
	Mode     hcl.Expression `hcl:"mode,optional"`
	Hosts    hcl.Expression `hcl:"hosts,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Domain struct {
	Host     string         `hcl:"host,label"`
	Wildcard hcl.Expression `hcl:"wildcard,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2SiteAuth struct {
	Realm    hcl.Expression `hcl:"realm,optional"`
	Users    []df2User      `hcl:"user,block"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2User struct {
	Name     string         `hcl:"name,label"`
	Password hcl.Expression `hcl:"password,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Variable struct {
	Name     string         `hcl:"name,label"`
	Default  hcl.Expression `hcl:"default,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

type df2Environment struct {
	Name      string         `hcl:"name,label"`
	NameRange hcl.Range      `hcl:"name,label_range"`
	Slice     hcl.Expression `hcl:"slice,optional"`
	Billing   hcl.Expression `hcl:"billing,optional"`
	Vars      hcl.Expression `hcl:"vars,optional"`
	DefRange  hcl.Range      `hcl:",def_range"`
}

type df2Hook struct {
	Phase      string         `hcl:"phase,label"`
	PhaseRange hcl.Range      `hcl:"phase,label_range"`
	Run        hcl.Expression `hcl:"run,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

type df2Test struct {
	Name     string         `hcl:"name,label"`
	Run      hcl.Expression `hcl:"run,optional"`
	DefRange hcl.Range      `hcl:",def_range"`
}

// ─── reading the text ───────────────────────────────────────────────────────

// readFormat2 parses a format-2 file into its blocks and the evaluation
// context its expressions run in, for the default slice.
func readFormat2(path string, data []byte) (*df2File, *df2Eval, error) {
	return readFormat2Env(path, data, "")
}

// selectedEnv is the environment a file is read for: its name, the slice it
// is applied to, and its billing when it states one.
type selectedEnv struct {
	name, slice, billing string
	sliceRange           hcl.Range
	billingRange         hcl.Range
	hasSlice, hasBilling bool
}

// readFormat2Env parses a format-2 file for one environment, or for the
// default slice when env is "". The context needs the variables, and the
// variables are themselves blocks, so they are read first on their own; an
// environment sets variables, so the environments are read with them.
//
// A variable's value is the selected environment's, else its default. One
// with neither is an error at the variable, naming where it could be set.
func readFormat2Env(path string, data []byte, env string) (*df2File, *df2Eval, error) {
	name := filepath.Base(path)
	f, diags := hclsyntax.ParseConfig(data, name, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, nil, diagError(diags)
	}
	body := f.Body.(*hclsyntax.Body)

	// The first block is `drift`, and it says which format this is. Checked
	// on the syntax tree, where blocks keep their source order.
	if len(body.Blocks) == 0 || body.Blocks[0].Type != "drift" {
		return nil, nil, diagError(hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "The first block must be drift",
			Detail:   `A Driftfile 2 begins with drift { format = "2.0" }, so the format is stated by the file rather than guessed.`,
			Subject:  body.Range().Ptr(),
		}})
	}

	ev := &df2Eval{dir: filepath.Dir(path)}

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
		var e df2Environment
		if d := gohcl.DecodeBody(blk.Body, ev.ctx, &e); d.HasErrors() {
			ev.diags = append(ev.diags, d...)
			continue
		}
		sel := &selectedEnv{name: env}
		if s, ok := ev.str(e.Slice, fmt.Sprintf("environment %q: slice", env)); ok {
			sel.slice, sel.hasSlice, sel.sliceRange = s, true, e.Slice.Range()
		}
		if b, ok := ev.str(e.Billing, fmt.Sprintf("environment %q: billing", env)); ok {
			sel.billing, sel.hasBilling, sel.billingRange = b, true, e.Billing.Range()
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
		var v df2Variable
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

	var file df2File
	if d := gohcl.DecodeBody(f.Body, ev.ctx, &file); d.HasErrors() {
		return nil, nil, diagError(d)
	}
	return &file, ev, nil
}

// ─── evaluation ─────────────────────────────────────────────────────────────

// df2Eval evaluates attribute expressions and collects every diagnostic, so a
// file with five mistakes is reported once with all five.
type df2Eval struct {
	ctx   *hcl.EvalContext
	dir   string
	vars  map[string]cty.Value
	diags hcl.Diagnostics
	// env is the environment the file is read for; nil for the default slice.
	env *selectedEnv
}

func (e *df2Eval) errorf(rng hcl.Range, summary, format string, args ...any) {
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
func (e *df2Eval) value(expr hcl.Expression, want cty.Type, what string) (cty.Value, bool) {
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

func (e *df2Eval) str(expr hcl.Expression, what string) (string, bool) {
	v, ok := e.value(expr, cty.String, what)
	if !ok {
		return "", false
	}
	return v.AsString(), true
}

func (e *df2Eval) integer(expr hcl.Expression, what string) (int, bool) {
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

func (e *df2Eval) boolean(expr hcl.Expression, what string) (bool, bool) {
	v, ok := e.value(expr, cty.Bool, what)
	if !ok {
		return false, false
	}
	return v.True(), true
}

func (e *df2Eval) strList(expr hcl.Expression, what string) ([]string, bool) {
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

func (e *df2Eval) strMap(expr hcl.Expression, what string) (map[string]string, bool) {
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
func (e *df2Eval) envReference(expr hcl.Expression, what string) (string, bool) {
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
func (e *df2Eval) functions() map[string]function.Function {
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
