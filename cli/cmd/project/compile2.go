package project

// compile2.go — a Driftfile 2, compiled to today's two payloads.
//
//  1. The DOCUMENT: the same flattened Pulse/Field document a YAML 1.x
//     Driftfile decodes to, validated against the same platform schema and run
//     through the same passes (finishParse). FunctionSpecs, the build, the
//     digests and every edge reader read it unchanged.
//  2. The SHAPE: a SliceConfig, the payload the platform prices, creates and
//     resizes with. Each booking comes from the block it books, each storage
//     size from the block it sizes, and the slice-wide limits from `limits`.
//
// Write-only values (secrets, API keys, site passwords) are compiled to
// REFERENCES: the document carries `$NAME` exactly as a YAML file writes it,
// and the value is read from the environment only by whatever sends it. No
// value is ever in either payload, so neither can leak one.
//
// # What the file leaves out
//
// Decided: the smallest shape that runs what the file names. Per field:
//
//   - A booking or a storage size has no default. A Pulse, Field, database,
//     collection or bucket the file names must state its size, because the
//     platform refuses a booking or a size of zero (tier.ValidateFunctionMemory,
//     ValidateDeclaredStorage), and a figure nobody chose is one nobody can be
//     held to.
//   - A queue's depth defaults to 0, which the platform reads as "the slice's
//     queue_depth" (models.BackboneQueues: `<= 0` falls back to MaxDepthEach).
//   - The slot count is the number of Pulses and Fields, and the database
//     count is the number of `sql` blocks. Both are derived: the platform
//     enforces a stored 0 as the free slice's 5 slots and 1 database, which
//     would refuse a sixth slot or a second database the file declares.
//   - scheduled_jobs defaults to the number of Pulses with a `cron`, the
//     fewest that run them. Fewer than that is refused.
//   - Every other limit defaults to 0. The platform accepts a 0 for each of
//     them at create and resize, prices it at nothing, and enforces it as the
//     free slice's figure (tier.WithDefaults on read). That is the smallest
//     billed shape that still runs.
//   - `billing = "free"` is the exception: the platform provisions a free
//     slice from the free preset, so an omitted limit is the preset's figure,
//     and a written one may only be lower.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// Compiled2 is a Driftfile 2 compiled: the document, the shape, and what the
// file says about money and write-only values.
type Compiled2 struct {
	Manifest *Manifest
	Shape    SliceConfig
	// Billing is "free" or "monthly".
	Billing string
	// WriteOnly lists every write-only value by reference, never by value.
	WriteOnly []WriteOnlyRef
	// Format is what `drift { format = ... }` says.
	Format string
	// Environments are the declared environment blocks, validated. What they
	// do arrives with per-environment slices.
	Environments []string
	// Notices are said once, before the plan.
	Notices []string
	// Domains is how many custom domains the file declares.
	Domains int
	// Addresses maps each booking key to the address the plan prints it under
	// ("post:rpc" → "pulse POST /rpc").
	Addresses map[string]string
}

// WriteOnlyRef is one value the platform stores and never returns.
type WriteOnlyRef struct {
	Address string // "secret STRIPE_KEY", "pulse POST /admin/reset", "site_auth drift"
	Kind    string // "secret", "api_key", "site_password"
	Env     string // the environment variable it is read from
}

// docRange ties a JSON pointer in the compiled document to where the file
// wrote it, so a schema refusal is reported at a line and column.
type docRange struct {
	rng  hcl.Range
	what string
}

type compiler struct {
	ev     *df2Eval
	base   string
	doc    Node
	ranges map[string]docRange
	out    *Compiled2
	crons  int
}

// CompileDriftfile2 reads and compiles the format-2 Driftfile at path.
func CompileDriftfile2(path string) (*Compiled2, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	file, ev, err := readFormat2(path, data)
	if err != nil {
		return nil, err
	}
	c := &compiler{
		ev:     ev,
		base:   filepath.Dir(path),
		doc:    Node{},
		ranges: map[string]docRange{},
		out:    &Compiled2{Addresses: map[string]string{}},
	}
	c.compile(file)
	if ev.diags.HasErrors() {
		return nil, diagError(ev.diags)
	}

	// The document is held to the platform's own schema, exactly as a YAML
	// file is, and every refusal is moved back to where this file wrote it.
	if errs := validateAgainstSchema(c.doc); len(errs) > 0 {
		return nil, c.relocateSchemaErrors(errs)
	}

	m := &Manifest{doc: c.doc, baseDir: c.base}
	if err := finishParse(m); err != nil {
		return nil, err
	}
	c.out.Manifest = m
	return c.out, nil
}

func (c *compiler) at(ptr string, rng hcl.Range, what string) {
	c.ranges[ptr] = docRange{rng: rng, what: what}
}

func (c *compiler) compile(f *df2File) {
	c.compileDrift(f)
	c.compileSlice(f)
	c.compileElements(f.Elements)
	c.compileBackbone(f)
	c.compileSites(f.Sites)
	c.compileEdge(f.Edge)
	c.compileHooksAndTests(f.Hooks, f.Tests)
	c.compileEnvironments(f.Environments)
	c.compileLimits(f)
}

// ─── drift and slice ────────────────────────────────────────────────────────

func (c *compiler) compileDrift(f *df2File) {
	if len(f.Drift) > 1 {
		c.ev.errorf(f.Drift[1].DefRange, "Duplicate drift block", "a Driftfile declares its format once.")
	}
	d := f.Drift[0]
	v, ok := c.ev.str(d.Format, "format")
	if !ok {
		c.ev.errorf(d.DefRange, "Missing format", `the drift block says which format this file is: format = "%s".`, Format2Implemented)
		return
	}
	if !format2Re.MatchString(v) {
		c.ev.errorf(d.Format.Range(), "Unsupported format",
			"format %q is not a Driftfile 2 format this CLI reads; it implements %s.", v, Format2Implemented)
		return
	}
	c.out.Format = v
	if v != Format2Implemented {
		c.out.Notices = append(c.out.Notices, fmt.Sprintf(
			"this file declares format %s and this CLI implements %s: keys it does not know are refused; `drift upgrade` reads them", v, Format2Implemented))
	}
}

func (c *compiler) compileSlice(f *df2File) {
	switch len(f.Slice) {
	case 0:
		c.ev.errorf(f.Drift[0].DefRange, "Missing slice block", `a Driftfile 2 names its slice: slice "my-app" { billing = "monthly" }.`)
		return
	case 1:
	default:
		c.ev.errorf(f.Slice[1].DefRange, "Duplicate slice block", "one file declares one slice.")
		return
	}
	s := f.Slice[0]
	c.doc["slice"] = s.Name
	c.at("", s.NameRange, fmt.Sprintf("slice %q", s.Name))
	c.at("/slice", s.NameRange, fmt.Sprintf("slice %q", s.Name))

	billing, ok := c.ev.str(s.Billing, "billing")
	switch {
	case !ok:
		// Money is stated, never defaulted: a file that does not say how the
		// slice is paid for is not one a plan can price.
		c.ev.errorf(s.DefRange, "Missing billing", `the slice says how it is paid for: billing = "monthly", or "free" for a shape inside the free slice.`)
	case billing != "free" && billing != "monthly":
		c.ev.errorf(s.Billing.Range(), "Unknown billing", `billing is "free" or "monthly", not %q.`, billing)
	default:
		c.out.Billing = billing
	}

	if present(s.Labels) {
		if labels, ok := c.ev.strMap(s.Labels, "labels"); ok {
			l := Node{}
			for k, v := range labels {
				l[k] = v
			}
			c.doc["labels"] = map[string]any(l)
			c.at("/labels", s.Labels.Range(), "labels")
			c.out.Shape.Labels = labels
		}
	}
	if len(s.Limits) > 1 {
		c.ev.errorf(s.Limits[1].DefRange, "Duplicate limits block", "a slice has one limits block.")
	}
}

// ─── atomic ─────────────────────────────────────────────────────────────────

// pulseLabelRe reads a Pulse's label: an HTTP method and its route, written
// the way people write a route, or QUEUE and the queue it drains.
var pulseLabelRe = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) /(.*)$|^QUEUE ([^ /]+)$`)

func (c *compiler) compileElements(elements []df2Element) {
	var list []any
	seen := map[string]bool{}
	slots := 0
	for i, el := range elements {
		ptr := fmt.Sprintf("/atomic/elements/%d", i)
		what := fmt.Sprintf("element %q", el.Name)
		c.at(ptr, el.NameRange, what)
		if seen[el.Name] {
			c.ev.errorf(el.DefRange, "Duplicate element", "element %q is declared twice; an element is one folder, declared once.", el.Name)
			continue
		}
		seen[el.Name] = true

		entry := Node{"name": el.Name}
		c.at(ptr+"/name", el.NameRange, what)
		dir := ""
		if d, ok := c.ev.str(el.Dir, what+": dir"); ok {
			entry["dir"] = d
			dir = d
			c.at(ptr+"/dir", el.Dir.Range(), what)
		}
		c.checkDir(dir, el, what)

		var pulses []any
		for j, p := range el.Pulses {
			if n := c.compilePulse(p, fmt.Sprintf("%s/pulses/%d", ptr, j)); n != nil {
				pulses = append(pulses, map[string]any(n))
			}
			slots++
		}
		var fields []any
		for j, f := range el.Fields {
			if n := c.compileField(f, fmt.Sprintf("%s/fields/%d", ptr, j)); n != nil {
				fields = append(fields, map[string]any(n))
			}
			slots++
		}
		if len(el.Pulses) == 0 && len(el.Fields) == 0 {
			c.ev.errorf(el.DefRange, "Empty element", "element %q declares no pulse and no field, so it books nothing and runs nothing.", el.Name)
			continue
		}
		if len(pulses) > 0 {
			entry["pulses"] = pulses
		}
		if len(fields) > 0 {
			entry["fields"] = fields
		}
		list = append(list, map[string]any(entry))
	}
	if slots > slice.MaxFunctionSlots {
		c.ev.errorf(elements[0].DefRange, "Too many slots",
			"the file declares %d pulses and fields, and a slice holds at most %d.", slots, slice.MaxFunctionSlots)
	}
	if len(list) > 0 {
		c.doc["atomic"] = map[string]any{"elements": list}
	}
}

// checkDir refuses an element whose folder is not on this machine, at the
// element's own position.
func (c *compiler) checkDir(dir string, el df2Element, what string) {
	if dir == "" {
		dir = filepath.Join("atomic", el.Name)
	}
	p := dir
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.base, p)
	}
	if info, err := os.Stat(p); err != nil || !info.IsDir() {
		rng := el.DefRange
		if present(el.Dir) {
			rng = el.Dir.Range()
		}
		c.ev.errorf(rng, "Directory not found", "%s: %s is not a directory.", what, dir)
	}
}

func (c *compiler) compilePulse(p df2Pulse, ptr string) Node {
	m := pulseLabelRe.FindStringSubmatch(p.Label)
	what := fmt.Sprintf("pulse %q", p.Label)
	c.at(ptr, p.LabelRange, what)
	if m == nil {
		c.ev.errorf(p.LabelRange, "Not a pulse trigger",
			`%s: a pulse is labelled with its method and route, "POST /rpc", or with QUEUE and the queue it drains, "QUEUE orders".`, what)
		return nil
	}
	method, route := strings.ToLower(m[1]), m[2]
	if m[3] != "" {
		method, route = "queue", m[3]
	}
	entry := Node{"route": route, "method": method}
	c.at(ptr+"/route", p.LabelRange, what)
	c.at(ptr+"/method", p.LabelRange, what)

	if h, ok := c.ev.str(p.Handler, what+": handler"); ok {
		entry["handler"] = h
		c.at(ptr+"/handler", p.Handler.Range(), what)
	} else if !present(p.Handler) {
		c.ev.errorf(p.DefRange, "Missing handler", "%s names the callable that serves it: handler = \"PostRPC\".", what)
	}
	c.optString(entry, "response", p.Response, ptr, what)
	c.optString(entry, "stream", p.Stream, ptr, what)
	c.optString(entry, "dir", p.Dir, ptr, what)
	if cron, ok := c.ev.str(p.Cron, what+": cron"); ok {
		entry["cron"] = cron
		c.at(ptr+"/cron", p.Cron.Range(), what)
		c.crons++
	}
	if b, ok := c.ev.boolean(p.Concurrent, what+": concurrent"); ok {
		entry["concurrent"] = b
		c.at(ptr+"/concurrent", p.Concurrent.Range(), what)
	}
	c.optEnvSecrets(entry, p.Env, p.Secrets, ptr, what)

	switch len(p.Auth) {
	case 0:
	case 1:
		if env, ok := c.ev.envReference(p.Auth[0].APIKey, what+": auth.api_key"); ok {
			entry["auth"] = "apikey"
			c.at(ptr+"/auth", p.Auth[0].DefRange, what)
			c.out.WriteOnly = append(c.out.WriteOnly, WriteOnlyRef{
				Address: "pulse " + p.Label, Kind: "api_key", Env: env,
			})
		} else if !present(p.Auth[0].APIKey) {
			c.ev.errorf(p.Auth[0].DefRange, "Missing api_key", `%s: an auth block names the key: api_key = env("NAME").`, what)
		}
	default:
		c.ev.errorf(p.Auth[1].DefRange, "Duplicate auth block", "%s has one gate.", what)
	}

	key := method + ":" + route
	c.out.Addresses[key] = "pulse " + p.Label
	c.booking(key, route, method, p.Memory, p.DefRange, what)
	return entry
}

func (c *compiler) compileField(f df2Field, ptr string) Node {
	what := fmt.Sprintf("field %q", f.Name)
	c.at(ptr, f.NameRange, what)
	entry := Node{"name": f.Name}
	c.at(ptr+"/name", f.NameRange, what)
	if h, ok := c.ev.str(f.Handler, what+": handler"); ok {
		entry["handler"] = h
		c.at(ptr+"/handler", f.Handler.Range(), what)
	} else if !present(f.Handler) {
		c.ev.errorf(f.DefRange, "Missing handler", "%s names the callable it runs: handler = \"Sequencer\".", what)
	}
	c.optString(entry, "dir", f.Dir, ptr, what)
	c.optEnvSecrets(entry, f.Env, f.Secrets, ptr, what)

	key := "field:" + f.Name
	c.out.Addresses[key] = "field " + f.Name
	if bytes := c.booking(key, f.Name, "field", f.Memory, f.DefRange, what); bytes > 0 {
		// A Field runs Go and nothing else, so its language is known here and
		// the compiled floor binds now, as tier.ValidateAtomicMemory binds it.
		if floor := CompiledMemoryFloor(); floor.Bytes > 0 && bytes < floor.Bytes {
			c.ev.errorf(f.Memory.Range(), "Below the compiled floor",
				"%s books %s; a Field runs Go, and a compiled slot needs at least %s.", what, sizeLabel2(bytes), sizeLabel2(floor.Bytes))
		}
	}
	return entry
}

// booking reads a slot's memory and adds it to the shape's slot list.
func (c *compiler) booking(key, route, method string, expr hcl.Expression, def hcl.Range, what string) int {
	raw, ok := c.ev.str(expr, what+": memory")
	if !ok {
		if !present(expr) {
			c.ev.errorf(def, "Missing memory", "%s books its memory: memory = \"32MiB\". It is the slot's own pool and what it is billed at, so it has no default.", what)
		}
		return 0
	}
	bytes, err := parseSize2(raw)
	if err != nil {
		c.ev.errorf(expr.Range(), "Not a size", "%s: memory %s.", what, err)
		return 0
	}
	if bytes%bytesPerMiB != 0 {
		c.ev.errorf(expr.Range(), "Not whole MiB", "%s: memory is booked in whole MiB, and %q is not.", what, raw)
		return 0
	}
	if re := memoryPattern(); re != nil && !re.MatchString(strconv.Itoa(bytes/bytesPerMiB)+"MB") {
		c.ev.errorf(expr.Range(), "Memory out of range",
			"%s books %s; a slot books from 8MiB to 256MiB (16MiB or more in a compiled language).", what, raw)
		return 0
	}
	c.out.Shape.Atomic.Functions = append(c.out.Shape.Atomic.Functions, AtomicFunction{
		Name: key, Route: route, Method: method, MemoryBytes: bytes,
	})
	return bytes
}

// memoryPattern is the platform's booking range, read from the schema's
// `memory` pattern rather than restated: the same rule a YAML booking is held
// to. Nil when this machine holds no schema; the plan refuses to run then.
func memoryPattern() *regexp.Regexp {
	raw, err := common.LocalDriftfileSchema()
	if err != nil {
		return nil
	}
	var doc struct {
		Definitions struct {
			AtomicEntry struct {
				Properties struct {
					Memory struct {
						Pattern string `json:"pattern"`
					} `json:"memory"`
				} `json:"properties"`
			} `json:"atomicEntry"`
		} `json:"definitions"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Definitions.AtomicEntry.Properties.Memory.Pattern == "" {
		return nil
	}
	re, cerr := regexp.Compile(doc.Definitions.AtomicEntry.Properties.Memory.Pattern)
	if cerr != nil {
		return nil
	}
	return re
}

func (c *compiler) optString(entry Node, key string, expr hcl.Expression, ptr, what string) {
	if v, ok := c.ev.str(expr, what+": "+key); ok {
		entry[key] = v
		c.at(ptr+"/"+key, expr.Range(), what)
	}
}

func (c *compiler) optEnvSecrets(entry Node, env, secrets hcl.Expression, ptr, what string) {
	if m, ok := c.ev.strMap(env, what+": env"); ok {
		n := map[string]any{}
		for k, v := range m {
			n[k] = v
		}
		entry["env"] = n
		c.at(ptr+"/env", env.Range(), what)
	}
	if l, ok := c.ev.strList(secrets, what+": secrets"); ok {
		entry["secrets"] = toAnyList(l)
		c.at(ptr+"/secrets", secrets.Range(), what)
	}
}

// ─── backbone ───────────────────────────────────────────────────────────────

func (c *compiler) compileBackbone(f *df2File) {
	bb := Node{}
	sh := &c.out.Shape.Backbone

	var sqls []any
	for i, s := range f.SQL {
		ptr := fmt.Sprintf("/backbone/sql/%d", i)
		what := fmt.Sprintf("sql %q", s.Name)
		c.at(ptr, s.NameRange, what)
		c.at(ptr+"/name", s.NameRange, what)
		e := Node{"name": s.Name}
		for _, kv := range []struct {
			key  string
			expr hcl.Expression
			dir  bool
		}{{"schema", s.Schema, false}, {"migrations", s.Migrations, true}, {"seed", s.Seed, false}} {
			if v, ok := c.ev.str(kv.expr, what+": "+kv.key); ok {
				e[kv.key] = v
				c.at(ptr+"/"+kv.key, kv.expr.Range(), what)
				c.checkPath(v, kv.dir, kv.expr.Range(), what+": "+kv.key)
			}
		}
		if size := c.size(s.Size, s.DefRange, what); size > 0 {
			setInt(&sh.SQL.Databases, s.Name, size, c, s.DefRange, what)
		}
		sqls = append(sqls, map[string]any(e))
	}
	if len(sqls) > 0 {
		bb["sql"] = sqls
	}
	sh.SQL.MaxDatabases = len(f.SQL)

	var nosql []any
	for i, n := range f.NoSQL {
		ptr := fmt.Sprintf("/backbone/nosql/%d", i)
		what := fmt.Sprintf("nosql %q", n.Name)
		c.at(ptr, n.NameRange, what)
		c.at(ptr+"/slot", n.NameRange, what)
		e := Node{"slot": n.Name}
		if v, ok := c.ev.str(n.Seed, what+": seed"); ok {
			e["seed"] = v
			c.at(ptr+"/seed", n.Seed.Range(), what)
			c.checkPath(v, false, n.Seed.Range(), what+": seed")
		}
		c.optString(e, "ttl", n.TTL, ptr, what)
		if l, ok := c.ev.strList(n.Unique, what+": unique"); ok {
			e["unique"] = toAnyList(l)
			c.at(ptr+"/unique", n.Unique.Range(), what)
		}
		if size := c.size(n.Size, n.DefRange, what); size > 0 {
			setInt(&sh.NoSQL.Collections, n.Name, size, c, n.DefRange, what)
		}
		nosql = append(nosql, map[string]any(e))
	}
	if len(nosql) > 0 {
		bb["nosql"] = nosql
	}

	var blobs []any
	for i, b := range f.Blobs {
		ptr := fmt.Sprintf("/backbone/blobs/%d", i)
		what := fmt.Sprintf("blobs %q", b.Name)
		c.at(ptr, b.NameRange, what)
		c.at(ptr+"/name", b.NameRange, what)
		if size := c.size(b.Size, b.DefRange, what); size > 0 {
			setInt(&sh.Blobs.Buckets, b.Name, size, c, b.DefRange, what)
		}
		blobs = append(blobs, map[string]any{"name": b.Name})
	}
	if len(blobs) > 0 {
		bb["blobs"] = blobs
	}

	var queues []any
	for i, q := range f.Queues {
		ptr := fmt.Sprintf("/backbone/queues/%d", i)
		what := fmt.Sprintf("queue %q", q.Name)
		c.at(ptr, q.NameRange, what)
		depth := 0
		if d, ok := c.ev.integer(q.Depth, what+": depth"); ok {
			if d < 0 {
				c.ev.errorf(q.Depth.Range(), "Negative depth", "%s: depth is a count of messages, 0 or more.", what)
			}
			depth = d
		}
		setInt(&sh.Queues.Queues, q.Name, depth, c, q.DefRange, what)
		queues = append(queues, q.Name)
	}
	if len(queues) > 0 {
		bb["queues"] = queues
	}

	if len(f.Caches) > 0 {
		cache := map[string]any{}
		for _, ce := range f.Caches {
			what := fmt.Sprintf("cache %q", ce.Key)
			ptr := "/backbone/cache/" + ce.Key
			c.at(ptr, ce.DefRange, what)
			e := Node{}
			if v, ok := c.ev.str(ce.File, what+": file"); ok {
				e["file"] = v
				c.checkPath(v, false, ce.File.Range(), what+": file")
			}
			c.optString(e, "value", ce.Value, ptr, what)
			if t, ok := c.ev.integer(ce.TTL, what+": ttl"); ok {
				e["ttl"] = t
			}
			if _, dup := cache[ce.Key]; dup {
				c.ev.errorf(ce.DefRange, "Duplicate cache entry", "%s is declared twice.", what)
			}
			cache[ce.Key] = map[string]any(e)
		}
		bb["cache"] = cache
	}

	if len(f.Secrets) > 0 {
		secrets := map[string]any{}
		for _, s := range f.Secrets {
			what := fmt.Sprintf("secret %q", s.Name)
			c.at("/backbone/secrets/"+s.Name, s.NameRange, what)
			if _, dup := secrets[s.Name]; dup {
				c.ev.errorf(s.DefRange, "Duplicate secret", "%s is declared twice.", what)
				continue
			}
			if !isEnvName(s.Name) {
				c.ev.errorf(s.NameRange, "Not a secret name", "%s: a secret is named like an environment variable, STRIPE_KEY.", what)
			}
			env, ok := c.ev.envReference(s.Value, what+": value")
			if !ok {
				if !present(s.Value) {
					c.ev.errorf(s.DefRange, "Missing value", `%s names where its value comes from: value = env("NAME").`, what)
				}
				continue
			}
			secrets[s.Name] = "$" + env
			c.out.WriteOnly = append(c.out.WriteOnly, WriteOnlyRef{Address: "secret " + s.Name, Kind: "secret", Env: env})
		}
		bb["secrets"] = secrets
	}

	if len(bb) > 0 {
		c.doc["backbone"] = map[string]any(bb)
	}
}

// setInt adds one named item to a declared set, refusing a name declared twice.
func setInt(set *map[string]int, name string, v int, c *compiler, rng hcl.Range, what string) {
	if *set == nil {
		*set = map[string]int{}
	}
	if _, dup := (*set)[name]; dup {
		c.ev.errorf(rng, "Duplicate name", "%s is declared twice.", what)
		return
	}
	(*set)[name] = v
}

// size reads a storage size, which every database, collection and bucket
// must state.
func (c *compiler) size(expr hcl.Expression, def hcl.Range, what string) int {
	raw, ok := c.ev.str(expr, what+": size")
	if !ok {
		if !present(expr) {
			c.ev.errorf(def, "Missing size", "%s states its size, size = \"20MiB\": it is the enforced limit and what it is billed at, and the platform refuses a size of zero.", what)
		}
		return 0
	}
	bytes, err := parseSize2(raw)
	if err != nil {
		c.ev.errorf(expr.Range(), "Not a size", "%s: size %s.", what, err)
		return 0
	}
	if bytes <= 0 {
		c.ev.errorf(expr.Range(), "Size of zero", "%s: the platform reads a size of zero as unbounded and refuses it.", what)
		return 0
	}
	return bytes
}

// checkPath refuses a file or directory the document names that is not on
// this machine, at the attribute that names it.
func (c *compiler) checkPath(p string, dir bool, rng hcl.Range, what string) {
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(c.base, p)
	}
	info, err := os.Stat(full)
	switch {
	case err != nil:
		c.ev.errorf(rng, "Not found", "%s: %s does not exist.", what, p)
	case dir && !info.IsDir():
		c.ev.errorf(rng, "Not a directory", "%s: %s is not a directory.", what, p)
	case !dir && info.IsDir():
		c.ev.errorf(rng, "Not a file", "%s: %s is a directory.", what, p)
	}
}

// ─── canvas, edge, hooks, tests, environments ───────────────────────────────

func (c *compiler) compileSites(sites []df2Site) {
	var list []any
	for i, s := range sites {
		ptr := fmt.Sprintf("/canvas/sites/%d", i)
		what := fmt.Sprintf("site %q", s.Path)
		c.at(ptr, s.PathRange, what)
		dir, ok := c.ev.str(s.Dir, what+": dir")
		if !ok {
			if !present(s.Dir) {
				c.ev.errorf(s.DefRange, "Missing dir", `%s names the folder it serves: dir = "site".`, what)
			}
			continue
		}
		c.checkPath(dir, true, s.Dir.Range(), what+": dir")
		e := map[string]any{"dir": dir}
		// The root is written with no path, as `canvas: ./site` is: that is the
		// spelling the slice stores under the slug `default`.
		if s.Path != "/" {
			e["path"] = s.Path
		}
		list = append(list, e)
	}
	if len(list) > 0 {
		c.doc["canvas"] = map[string]any{"sites": list}
	}
}

func (c *compiler) compileEdge(edges []df2Edge) {
	if len(edges) == 0 {
		return
	}
	if len(edges) > 1 {
		c.ev.errorf(edges[1].DefRange, "Duplicate edge block", "a slice has one edge block.")
	}
	e := edges[0]
	out := Node{}
	c.at("/edge", e.DefRange, "edge")

	if len(e.Egress) > 0 {
		eg := Node{}
		c.at("/edge/egress", e.Egress[0].DefRange, "edge.egress")
		if v, ok := c.ev.str(e.Egress[0].Mode, "edge.egress: mode"); ok {
			eg["mode"] = v
		}
		if l, ok := c.ev.strList(e.Egress[0].Hosts, "edge.egress: hosts"); ok {
			eg["hosts"] = toAnyList(l)
		}
		out["egress"] = map[string]any(eg)
	}

	if len(e.Domains) > 0 {
		var ds []any
		for i, d := range e.Domains {
			c.at(fmt.Sprintf("/edge/domains/%d", i), d.DefRange, fmt.Sprintf("domain %q", d.Host))
			n := map[string]any{"host": d.Host}
			if w, ok := c.ev.boolean(d.Wildcard, "domain: wildcard"); ok && w {
				n["wildcard"] = true
			}
			ds = append(ds, n)
		}
		out["domains"] = ds
		c.out.Domains = len(e.Domains)
	}

	// edge.rate_limit and limits.requests_per_minute are ONE field of the
	// shape, so the file may write it in one place. Here it compiles into the
	// shape like every other limit, and compileLimits refuses both at once.
	if present(e.RateLimit) {
		if v, ok := c.ev.str(e.RateLimit, "edge.rate_limit"); ok {
			rpm, err := rateLimitRPM(v)
			if err != nil {
				c.ev.errorf(e.RateLimit.Range(), "Not a rate", "edge.rate_limit: %v.", err)
			} else {
				c.out.Shape.Atomic.MaxNumberOfRequestsPerMinute = rpm
			}
		}
	}

	if len(e.SiteAuth) > 0 {
		sa := e.SiteAuth[0]
		gate := Node{}
		c.at("/edge/site_auth", sa.DefRange, "edge.site_auth")
		c.optString(gate, "realm", sa.Realm, "/edge/site_auth", "edge.site_auth")
		var users []any
		for i, u := range sa.Users {
			what := fmt.Sprintf("site_auth user %q", u.Name)
			c.at(fmt.Sprintf("/edge/site_auth/users/%d", i), u.DefRange, what)
			env, ok := c.ev.envReference(u.Password, what+": password")
			if !ok {
				if !present(u.Password) {
					c.ev.errorf(u.DefRange, "Missing password", `%s names where its password comes from: password = env("NAME").`, what)
				}
				continue
			}
			users = append(users, map[string]any{"user": u.Name, "password": "$" + env})
			c.out.WriteOnly = append(c.out.WriteOnly, WriteOnlyRef{Address: "site_auth " + u.Name, Kind: "site_password", Env: env})
		}
		gate["users"] = users
		out["site_auth"] = map[string]any(gate)
	}

	if l, ok := c.ev.strList(e.Links, "edge.links"); ok {
		out["links"] = toAnyList(l)
		c.at("/edge/links", e.Links.Range(), "edge.links")
	}
	if l, ok := c.ev.strList(e.Allow, "edge.allow"); ok {
		out["allow"] = toAnyList(l)
		c.at("/edge/allow", e.Allow.Range(), "edge.allow")
	}
	if m, ok := c.ev.strMap(e.Hosts, "edge.hosts"); ok {
		hosts := make([]string, 0, len(m))
		for h := range m {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		var list []any
		for _, h := range hosts {
			list = append(list, map[string]any{"host": h, "slice": m[h]})
		}
		out["hosts"] = list
		c.at("/edge/hosts", e.Hosts.Range(), "edge.hosts")
	}
	if len(out) > 0 {
		c.doc["edge"] = map[string]any(out)
	}
}

func (c *compiler) compileHooksAndTests(hooks []df2Hook, tests []df2Test) {
	h := map[string][]any{}
	for _, hk := range hooks {
		if hk.Phase != "pre_deploy" && hk.Phase != "post_deploy" {
			c.ev.errorf(hk.PhaseRange, "Unknown hook", `a hook runs at "pre_deploy" or "post_deploy", not %q.`, hk.Phase)
			continue
		}
		if run, ok := c.ev.str(hk.Run, "hook: run"); ok {
			h[hk.Phase] = append(h[hk.Phase], run)
		} else if !present(hk.Run) {
			c.ev.errorf(hk.DefRange, "Missing run", "a hook names the command it runs: run = \"make build\".")
		}
	}
	if len(h) > 0 {
		hn := map[string]any{}
		for k, v := range h {
			hn[k] = v
		}
		c.doc["hooks"] = hn
	}
	var e2e []any
	for _, t := range tests {
		if run, ok := c.ev.str(t.Run, fmt.Sprintf("test %q: run", t.Name)); ok {
			e2e = append(e2e, run)
		} else if !present(t.Run) {
			c.ev.errorf(t.DefRange, "Missing run", "test %q names the command it runs.", t.Name)
		}
	}
	if len(e2e) > 0 {
		c.doc["tests"] = map[string]any{"e2e": e2e}
	}
}

// compileEnvironments validates every environment block. Nothing here acts on
// one yet: each environment becoming its own slice, created and sized by the
// file, is the next step of Driftfile 2.
func (c *compiler) compileEnvironments(envs []df2Environment) {
	seen := map[string]bool{}
	name := NamePattern()
	for _, e := range envs {
		what := fmt.Sprintf("environment %q", e.Name)
		if seen[e.Name] {
			c.ev.errorf(e.DefRange, "Duplicate environment", "%s is declared twice.", what)
			continue
		}
		seen[e.Name] = true
		if s, ok := c.ev.str(e.Slice, what+": slice"); ok && name != nil && !name.MatchString(s) {
			c.ev.errorf(e.Slice.Range(), "Not a slice name", "%s: %q is not a slice name.", what, s)
		}
		if b, ok := c.ev.str(e.Billing, what+": billing"); ok && b != "free" && b != "monthly" {
			c.ev.errorf(e.Billing.Range(), "Unknown billing", `%s: billing is "free" or "monthly", not %q.`, what, b)
		}
		if vars, ok := c.ev.strMap(e.Vars, what+": vars"); ok {
			for k := range vars {
				if _, declared := c.ev.vars[k]; !declared {
					c.ev.errorf(e.Vars.Range(), "Undeclared variable", "%s sets %q, which no variable block declares.", what, k)
				}
			}
		}
		c.out.Environments = append(c.out.Environments, e.Name)
	}
	sort.Strings(c.out.Environments)
}

// ─── limits ─────────────────────────────────────────────────────────────────

// limitUnit is how a limit is written: a count, a size, or a duration held in
// seconds, hours or days.
type limitUnit int

const (
	unitCount limitUnit = iota
	unitSize
	unitSeconds
	unitHours
	unitDays
)

// shapeLimit is one `limits` attribute and the SliceConfig scalar it writes.
type shapeLimit struct {
	name string
	expr func(*df2Limits) hcl.Expression
	unit limitUnit
	at   func(*SliceConfig) *int
}

// shapeLimits is the whole mapping from `limits` to the shape.
var shapeLimits = []shapeLimit{
	{"requests_per_minute", func(l *df2Limits) hcl.Expression { return l.RequestsPerMinute }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfRequestsPerMinute }},
	{"timeout", func(l *df2Limits) hcl.Expression { return l.Timeout }, unitSeconds, func(c *SliceConfig) *int { return &c.Atomic.MaxFunctionRuntimeInSeconds }},
	{"log_retention", func(l *df2Limits) hcl.Expression { return l.LogRetention }, unitHours, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfHoursForLogRetention }},
	{"deploy_history", func(l *df2Limits) hcl.Expression { return l.DeployHistory }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfDeploymentsInHistory }},
	{"backup_retention", func(l *df2Limits) hcl.Expression { return l.BackupRetention }, unitDays, func(c *SliceConfig) *int { return &c.Backbone.BackupRetentionDays }},
	{"scheduled_jobs", func(l *df2Limits) hcl.Expression { return l.ScheduledJobs }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfScheduledJobs }},
	{"atomic_storage", func(l *df2Limits) hcl.Expression { return l.AtomicStorage }, unitSize, func(c *SliceConfig) *int { return &c.Atomic.MaxStorageBytes }},
	{"canvas_storage", func(l *df2Limits) hcl.Expression { return l.CanvasStorage }, unitSize, func(c *SliceConfig) *int { return &c.Canvas.TotalMaxSizeInBytes }},
	{"realtime_connections", func(l *df2Limits) hcl.Expression { return l.RealtimeConnections }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Realtime.MaxConcurrentConnections }},
	{"secrets", func(l *df2Limits) hcl.Expression { return l.Secrets }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxCount }},
	{"secret_size", func(l *df2Limits) hcl.Expression { return l.SecretSize }, unitSize, func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxSizeInBytesEach }},
	{"locks", func(l *df2Limits) hcl.Expression { return l.Locks }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Locks.MaxConcurrent }},
	{"blob_object_size", func(l *df2Limits) hcl.Expression { return l.BlobObjectSize }, unitSize, func(c *SliceConfig) *int { return &c.Backbone.Blobs.MaxSizeInBytesEach }},
	{"queue_depth", func(l *df2Limits) hcl.Expression { return l.QueueDepth }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Queues.MaxDepthEach }},
	{"vault_entry_size", func(l *df2Limits) hcl.Expression { return l.VaultEntrySize }, unitSize, func(c *SliceConfig) *int { return &c.Deed.Vault.MaxSizeInBytesEach }},
	{"vault_entries", func(l *df2Limits) hcl.Expression { return l.VaultEntries }, unitCount, func(c *SliceConfig) *int { return &c.Deed.Vault.MaxEntriesPerUID }},
	{"pocket_record_size", func(l *df2Limits) hcl.Expression { return l.PocketRecordSize }, unitSize, func(c *SliceConfig) *int { return &c.Deed.Pocket.MaxSizeInBytesEach }},
}

func (c *compiler) compileLimits(f *df2File) {
	if len(f.Slice) != 1 {
		return
	}
	sh := &c.out.Shape
	rateFromEdge := sh.Atomic.MaxNumberOfRequestsPerMinute

	// A free slice is provisioned from the free preset: what the file leaves
	// out is the preset's figure. The slot and database counts and the shared
	// pool are the preset's too, because the free create path takes every
	// scalar from it.
	written := map[string]bool{}
	if c.out.Billing == "free" {
		for _, l := range freeLimits {
			*l.at(sh) = *l.at(&freePreset)
		}
	}

	var lim *df2Limits
	if len(f.Slice[0].Limits) > 0 {
		lim = &f.Slice[0].Limits[0]
	}
	for _, sl := range shapeLimits {
		if lim == nil {
			break
		}
		expr := sl.expr(lim)
		if !present(expr) {
			continue
		}
		what := "limits." + sl.name
		v, ok := c.limitValue(expr, sl.unit, what)
		if !ok {
			continue
		}
		*sl.at(sh) = v
		written[sl.name] = true
	}

	if rateFromEdge > 0 {
		if written["requests_per_minute"] {
			c.ev.errorf(f.Edge[0].RateLimit.Range(), "Written twice",
				"edge.rate_limit and limits.requests_per_minute are one limit; write it in one place.")
		} else {
			sh.Atomic.MaxNumberOfRequestsPerMinute = rateFromEdge
		}
	}

	if c.out.Billing != "free" {
		sh.Atomic.MaxNumberOfFunctions = len(sh.Atomic.Functions)
		if !written["scheduled_jobs"] {
			sh.Atomic.MaxNumberOfScheduledJobs = c.crons
		}
	}
	if sh.Atomic.MaxNumberOfScheduledJobs < c.crons {
		rng := f.Slice[0].DefRange
		if lim != nil && present(lim.ScheduledJobs) {
			rng = lim.ScheduledJobs.Range()
		}
		c.ev.errorf(rng, "Too few scheduled jobs",
			"%d pulses carry a cron and the slice allows %d scheduled jobs.", c.crons, sh.Atomic.MaxNumberOfScheduledJobs)
	}

	if c.out.Billing == "free" && !c.ev.diags.HasErrors() {
		if err := freeEnvelopeViolation(*sh, c.out.Domains); err != nil {
			c.ev.errorf(f.Slice[0].Billing.Range(), "Not the free slice",
				"billing is \"free\", and %v. Make the shape smaller, or write billing = \"monthly\".", err)
		}
	}
}

// limitValue reads one limit in its unit, as the integer the shape stores.
func (c *compiler) limitValue(expr hcl.Expression, unit limitUnit, what string) (int, bool) {
	if unit == unitCount {
		n, ok := c.ev.integer(expr, what)
		if ok && n < 0 {
			c.ev.errorf(expr.Range(), "Negative limit", "%s is 0 or more.", what)
			return 0, false
		}
		return n, ok
	}
	raw, ok := c.ev.str(expr, what)
	if !ok {
		return 0, false
	}
	if unit == unitSize {
		b, err := parseSize2(raw)
		if err != nil {
			c.ev.errorf(expr.Range(), "Not a size", "%s %s.", what, err)
			return 0, false
		}
		return b, true
	}
	secs, err := parseDuration2(raw)
	if err != nil {
		c.ev.errorf(expr.Range(), "Not a duration", "%s %s.", what, err)
		return 0, false
	}
	per := map[limitUnit]int{unitSeconds: 1, unitHours: 3600, unitDays: 86400}[unit]
	if secs%per != 0 {
		names := map[limitUnit]string{unitHours: "hours", unitDays: "days"}
		c.ev.errorf(expr.Range(), "Not whole", "%s is kept in whole %s, and %q is not.", what, names[unit], raw)
		return 0, false
	}
	return secs / per, true
}

// ─── schema refusals, moved back to the file ────────────────────────────────

func (c *compiler) relocateSchemaErrors(errs ParseErrors) error {
	var diags hcl.Diagnostics
	for _, msg := range errs {
		loc := instanceLocation(msg)
		best, bestLen := docRange{}, -1
		for ptr, r := range c.ranges {
			if (loc == ptr || strings.HasPrefix(loc, ptr+"/")) && len(ptr) > bestLen {
				best, bestLen = r, len(ptr)
			}
		}
		text := msg
		if i := strings.Index(msg, "': "); i >= 0 && strings.HasPrefix(msg, "at '") {
			text = msg[i+3:]
		}
		if bestLen < 0 {
			diags = append(diags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid Driftfile", Detail: msg})
			continue
		}
		key := strings.TrimPrefix(strings.TrimPrefix(loc, keyPrefix(c.ranges, best)), "/")
		detail := best.what
		if key != "" {
			detail += ": " + key
		}
		r := best.rng
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError, Summary: "Invalid " + strings.SplitN(best.what, " ", 2)[0],
			Detail: detail + ": " + text, Subject: &r,
		})
	}
	return diagError(diags)
}

// keyPrefix finds the pointer a range was recorded under.
func keyPrefix(ranges map[string]docRange, want docRange) string {
	longest := ""
	for ptr, r := range ranges {
		if r == want && len(ptr) >= len(longest) {
			longest = ptr
		}
	}
	return longest
}

// ─── units ──────────────────────────────────────────────────────────────────

var size2Re = regexp.MustCompile(`^([0-9]+)(KiB|MiB|GiB)$`)

// parseSize2 reads a Driftfile 2 size: whole KiB, MiB or GiB. "MB" is refused
// rather than read as MiB, so a size means one number whoever reads it.
func parseSize2(s string) (int, error) {
	m := size2Re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a size: write a whole number of KiB, MiB or GiB, e.g. \"64MiB\"", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("%q is too large", s)
	}
	scale := map[string]int{"KiB": 1024, "MiB": bytesPerMiB, "GiB": 1024 * bytesPerMiB}[m[2]]
	if n > (1<<31-1)/scale {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return n * scale, nil
}

var duration2Re = regexp.MustCompile(`^([0-9]+)(s|m|h|d)$`)

// parseDuration2 reads "30s", "24h", "3d" as seconds.
func parseDuration2(s string) (int, error) {
	m := duration2Re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a duration: write a whole number of s, m, h or d, e.g. \"24h\"", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1<<24 {
		return 0, fmt.Errorf("%q is too long", s)
	}
	return n * map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400}[m[2]], nil
}

// sizeLabel2 renders bytes the way a Driftfile 2 writes them.
func sizeLabel2(b int) string {
	switch {
	case b >= 1024*bytesPerMiB && b%(1024*bytesPerMiB) == 0:
		return fmt.Sprintf("%dGiB", b/(1024*bytesPerMiB))
	case b%bytesPerMiB == 0:
		return fmt.Sprintf("%dMiB", b/bytesPerMiB)
	case b%1024 == 0:
		return fmt.Sprintf("%dKiB", b/1024)
	}
	return fmt.Sprintf("%dB", b)
}

func toAnyList(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}
