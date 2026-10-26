package project

// compile.go — a Driftfile, compiled to its two payloads.
//
//  1. The DOCUMENT: the flattened Pulse/Field document, validated against the
//     platform's schema and run through finishParse. FunctionSpecs, the build,
//     the digests and every edge reader read it.
//  2. The SHAPE: a SliceConfig, the payload the platform prices, creates and
//     resizes with. Each booking comes from the block it books, each storage
//     size from the block it sizes, and the slice-wide limits from `limits`.
//
// Write-only values (secrets, API keys, site passwords) are compiled to
// REFERENCES: the document carries `$NAME`, and the value is read from the
// environment only by whatever sends it. No value is ever in either payload,
// so neither can leak one.
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
	"time"

	"github.com/hashicorp/hcl/v2"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"
)

// Compiled is a Driftfile compiled: the document, the shape, and what the
// file says about money and write-only values.
type Compiled struct {
	Manifest *Manifest
	// Shape is the shape the file is applied as: PricedShape or FreeShape,
	// whichever Use last chose. A compile starts on PricedShape.
	Shape SliceConfig
	// PricedShape is the file read as a configured slice: a limit it leaves out
	// is 0 (the platform's smallest billed figure), and the slot and scheduled-job
	// counts are what the file declares.
	PricedShape SliceConfig
	// FreeShape is the file read as a slice inside the free grant: a limit it
	// leaves out is the free preset's figure, and a written one may only be lower.
	FreeShape SliceConfig
	// FitsFree is whether the file's shape is inside the free grant, and
	// FreeMisfit is the first bound it crosses when it is not. The file does not
	// say how the slice is paid for; this is what decides whether it can be free.
	FitsFree   bool
	FreeMisfit error
	// Free is whether Shape is FreeShape.
	Free bool
	// WriteOnly lists every write-only value by reference, never by value.
	WriteOnly []WriteOnlyRef
	// Alerts is every alert the file declares, in the order it declares them.
	Alerts []DeclaredAlert
	// Environments are the declared environment blocks, validated.
	Environments []string
	// Environment is the one this compile is for, or "" for the default slice.
	Environment string
	// Domains is how many custom domains the file declares.
	Domains int
	// Addresses maps each booking key to the address the plan prints it under
	// ("post:rpc" → "pulse POST /rpc").
	Addresses map[string]string
}

// WriteOnlyRef is one value the platform stores and never returns.
type WriteOnlyRef struct {
	Address string // "secret STRIPE_KEY", "pulse POST /admin/reset", "site_auth drift"
	Kind    string // "secret", "api_key", "site_password", "alert_webhook"
	Env     string // the environment variable it is read from
}

// DeclaredAlert is one alert a pulse declares. The webhook it notifies is not
// here: it is a write-only value, read from Env when the alert is applied.
type DeclaredAlert struct {
	Name string
	// Function is the key the slice watches: the pulse's route, without its method.
	Function string
	// Pulse is the pulse as the file labels it, for the plan.
	Pulse     string
	On        string
	Threshold int
	WindowSec int
	// Env is the variable the webhook URL is read from.
	Env string
}

// Defaults an alert has when the file leaves them out; the document states them
// too (the format's `default`).
const (
	defaultAlertOn        = "errors"
	defaultAlertThreshold = 1
	defaultAlertWindowSec = 300
	minAlertWindowSec     = 60
)

// docRange ties a JSON pointer in the compiled document to where the file
// wrote it, so a schema refusal is reported at a line and column.
type docRange struct {
	rng  hcl.Range
	what string
}

type compiler struct {
	ev     *dfEval
	base   string
	doc    Node
	ranges map[string]docRange
	out    *Compiled
	crons  int
	// alertNames is every alert declared so far and the pulse that declared it:
	// an alert's name is the slice's, not its pulse's.
	alertNames map[string]string
}

// CompileDriftfile reads and compiles the Driftfile at path, for its default
// slice.
func CompileDriftfile(path string) (*Compiled, error) {
	return CompileDriftfileEnv(path, "")
}

// CompileDriftfileEnv compiles the file for one of its environments: its
// variables take the environment's values, and the result names the
// environment's slice and billing. env "" is the default slice.
func CompileDriftfileEnv(path, env string) (*Compiled, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	file, ev, err := readDriftfileEnv(path, data, env)
	if err != nil {
		return nil, err
	}
	c := &compiler{
		ev:     ev,
		base:   filepath.Dir(path),
		doc:    Node{},
		ranges: map[string]docRange{},
		out:    &Compiled{Addresses: map[string]string{}},
	}
	c.compile(file)
	if ev.diags.HasErrors() {
		return nil, diagError(ev.diags)
	}

	// The document is held to the platform's own schema, and every refusal is
	// moved back to where this file wrote it.
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

// Use chooses which shape the file is applied as. A file that does not fit the
// free grant cannot be chosen free.
func (c *Compiled) Use(free bool) {
	c.Free = free && c.FitsFree
	if c.Free {
		c.Shape = c.FreeShape
	} else {
		c.Shape = c.PricedShape
	}
}

func (c *compiler) at(ptr string, rng hcl.Range, what string) {
	c.ranges[ptr] = docRange{rng: rng, what: what}
}

func (c *compiler) compile(f *dfFile) {
	c.compileSlice(f)
	c.compileElements(f.Elements)
	c.compileBackbone(f)
	c.compileSites(f.Sites)
	c.compileEdge(f.Edge)
	c.compileHooksAndTests(f.Hooks, f.Tests)
	c.compileEnvironments(f.Environments)
	c.compileLimits(f)
}

// ─── slice ──────────────────────────────────────────────────────────────────

// billingGone is the refusal of the old `billing` key, one sentence naming what
// to do. Nothing rewrites the file: deleting the line is the whole migration.
const billingGone = "a Driftfile no longer says how a slice is paid for. Delete this line: a slice whose shape " +
	"fits the free grant can take one of the account's free slices (`drift apply` asks), and any other shape " +
	"is priced, quoted by `drift plan` and agreed to with `drift apply`."

func (c *compiler) compileSlice(f *dfFile) {
	switch len(f.Slice) {
	case 0:
		start := hcl.Range{Filename: c.ev.file, Start: hcl.InitialPos, End: hcl.InitialPos}
		c.ev.errorf(start, "Missing slice block", `a Driftfile names its slice: slice "my-app" {}.`)
		return
	case 1:
	default:
		c.ev.errorf(f.Slice[1].DefRange, "Duplicate slice block", "one file declares one slice.")
		return
	}
	s := f.Slice[0]
	name, nameRange := s.Name, s.NameRange
	if e := c.ev.env; e != nil {
		// An environment is its own slice: the one it names, or
		// `<slice>-<env>`.
		c.out.Environment = e.name
		name = deriveSliceName(s.Name, e.name)
		if e.hasSlice {
			name, nameRange = e.slice, e.sliceRange
		}
		if re := NamePattern(); re != nil && !re.MatchString(name) {
			c.ev.errorf(nameRange, "Not a slice name", "environment %q is applied to slice %q, which is not a slice name.", e.name, name)
		}
	}
	c.doc["slice"] = name
	c.at("", nameRange, fmt.Sprintf("slice %q", name))
	c.at("/slice", nameRange, fmt.Sprintf("slice %q", name))

	// There is no billing key. Whether a slice is free is a fact about its
	// shape and the account, decided when the file is applied, so a file that
	// still says how it is paid for is refused rather than half-honoured.
	if present(s.Billing) {
		c.ev.errorf(s.Billing.Range(), "billing is gone", billingGone)
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

func (c *compiler) compileElements(elements []dfElement) {
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
// element's own position. The element named "default" is ./atomic itself,
// where `drift atomic new` writes without --element; every other is
// ./atomic/<name>.
func (c *compiler) checkDir(dir string, el dfElement, what string) {
	switch {
	case dir != "":
	case el.Name == atomic_cmd.DefaultElementName:
		dir = "atomic"
	default:
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

func (c *compiler) compilePulse(p dfPulse, ptr string) Node {
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
	c.optDir(entry, p.Dir, ptr, what)
	if cron, ok := c.ev.str(p.Cron, what+": cron"); ok {
		entry["cron"] = cron
		c.at(ptr+"/cron", p.Cron.Range(), what)
		c.crons++
	}
	if b, ok := c.ev.boolean(p.Concurrent, what+": concurrent"); ok {
		entry["concurrent"] = b
		c.at(ptr+"/concurrent", p.Concurrent.Range(), what)
	}
	c.compileAlerts(entry, p, route, ptr, what)
	c.queueTuning(entry, p, method, ptr, what)
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

// compileAlerts reads a pulse's `alert` blocks: a condition on the pulse, and the
// webhook told when it holds. The webhook URL carries whatever credential the
// endpoint needs (a chat hook's path is one), so it is write-only like every
// other value of that kind: written env("NAME"), read when the alert is applied,
// and never in the document or the plan.
func (c *compiler) compileAlerts(entry Node, p dfPulse, route, ptr, what string) {
	var list []any
	for i, a := range p.Alerts {
		aptr := fmt.Sprintf("%s/alerts/%d", ptr, i)
		awhat := fmt.Sprintf("%s alert %q", what, a.Name)
		c.at(aptr, a.NameRange, awhat)

		if !alertNameRe.MatchString(a.Name) {
			c.ev.errorf(a.NameRange, "Not an alert name",
				"%s: an alert name is 1 to 64 lowercase letters, digits and hyphens.", awhat)
			continue
		}
		if other, taken := c.alertNames[a.Name]; taken {
			c.ev.errorf(a.NameRange, "Duplicate alert",
				"%s: %s already declares an alert of that name; an alert's name is the slice's.", awhat, other)
			continue
		}
		if c.alertNames == nil {
			c.alertNames = map[string]string{}
		}
		c.alertNames[a.Name] = what

		on := defaultAlertOn
		if v, ok := c.ev.str(a.On, awhat+": on"); ok {
			if v != defaultAlertOn {
				c.ev.errorf(a.On.Range(), "Not a condition",
					"%s: on is %q; \"errors\" is the one condition there is.", awhat, v)
				continue
			}
			on = v
		}
		threshold := defaultAlertThreshold
		if n, ok := c.ev.integer(a.Threshold, awhat+": threshold"); ok {
			if n < 1 {
				c.ev.errorf(a.Threshold.Range(), "Out of range", "%s: threshold is %d; it is at least 1.", awhat, n)
				continue
			}
			threshold = n
		}
		window := defaultAlertWindowSec
		if raw, ok := c.ev.str(a.Window, awhat+": window"); ok {
			secs, err := parseDuration(raw)
			switch {
			case err != nil:
				c.ev.errorf(a.Window.Range(), "Not a duration", "%s: window %q is not a duration; write one such as \"5m\" or \"1h\".", awhat, raw)
				continue
			case secs < minAlertWindowSec:
				c.ev.errorf(a.Window.Range(), "Out of range", "%s: window is %s; it is at least 1m.", awhat, raw)
				continue
			}
			window = secs
		}
		env, ok := c.ev.envReference(a.Webhook, awhat+": webhook")
		if !ok {
			if !present(a.Webhook) {
				c.ev.errorf(a.DefRange, "Missing webhook", `%s names where it notifies: webhook = env("NAME"), a variable holding an http(s) URL.`, awhat)
			}
			continue
		}

		list = append(list, Node{"name": a.Name, "on": on, "threshold": threshold, "window_seconds": window})
		c.at(aptr+"/name", a.NameRange, awhat)
		c.out.WriteOnly = append(c.out.WriteOnly, WriteOnlyRef{Address: "alert " + a.Name, Kind: "alert_webhook", Env: env})
		c.out.Alerts = append(c.out.Alerts, DeclaredAlert{
			Name: a.Name, Function: route, Pulse: strings.TrimPrefix(what, "pulse "),
			On: on, Threshold: threshold, WindowSec: window, Env: env,
		})
	}
	if len(list) > 0 {
		entry["alerts"] = list
	}
}

var alertNameRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// Bounds the queue grammar carries: the format publishes the same figures, and a
// poll under the floor is a spin against the queue.
const (
	minPollMS     = 50
	maxPollMS     = 60000
	maxQueueRetry = 100
)

// queueTuning reads how often a queue pulse polls and how many times a failing
// message is delivered again. Both belong to the queue a pulse drains, so on any
// other pulse they are refused rather than ignored.
func (c *compiler) queueTuning(entry Node, p dfPulse, method, ptr, what string) {
	if method != "queue" {
		for _, a := range []struct {
			name string
			expr hcl.Expression
		}{{"poll", p.Poll}, {"max_retries", p.MaxRetries}} {
			if present(a.expr) {
				c.ev.errorf(a.expr.Range(), "Not a queue pulse",
					"%s: %s tunes the queue a pulse drains, so it belongs on a pulse labelled QUEUE and the queue it drains, \"QUEUE orders\".", what, a.name)
			}
		}
		return
	}
	if raw, ok := c.ev.str(p.Poll, what+": poll"); ok {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			c.ev.errorf(p.Poll.Range(), "Not a duration", "%s: poll %q is not a duration; write one such as \"250ms\" or \"2s\".", what, raw)
		case d.Milliseconds() < minPollMS || d.Milliseconds() > maxPollMS:
			c.ev.errorf(p.Poll.Range(), "Out of range",
				"%s: poll is %s; it is between %dms and %ds (below that the slice spins against its own queue).", what, raw, minPollMS, maxPollMS/1000)
		default:
			entry["poll_ms"] = int(d.Milliseconds())
			c.at(ptr+"/poll_ms", p.Poll.Range(), what)
		}
	}
	if n, ok := c.ev.integer(p.MaxRetries, what+": max_retries"); ok {
		if n < 1 || n > maxQueueRetry {
			c.ev.errorf(p.MaxRetries.Range(), "Out of range", "%s: max_retries is %d; it is between 1 and %d.", what, n, maxQueueRetry)
		} else {
			entry["max_retry"] = n
			c.at(ptr+"/max_retry", p.MaxRetries.Range(), what)
		}
	}
}

func (c *compiler) compileField(f dfField, ptr string) Node {
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
	c.optDir(entry, f.Dir, ptr, what)
	c.optEnvSecrets(entry, f.Env, f.Secrets, ptr, what)

	key := "field:" + f.Name
	c.out.Addresses[key] = "field " + f.Name
	if bytes := c.booking(key, f.Name, "field", f.Memory, f.DefRange, what); bytes > 0 {
		// A Field runs Go and nothing else, so its language is known here and
		// the compiled floor binds now, as tier.ValidateAtomicMemory binds it.
		if floor := CompiledMemoryFloor(); floor.Bytes > 0 && bytes < floor.Bytes {
			c.ev.errorf(f.Memory.Range(), "Below the compiled floor",
				"%s books %s; a Field runs Go, and a compiled slot needs at least %s.", what, sizeLabel(bytes), sizeLabel(floor.Bytes))
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
	bytes, err := parseSize(raw)
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

// memoryPattern is the platform's booking range, read from the schema rather
// than restated. Nil when this machine holds no schema; the plan refuses to
// run then.
//
// The range lives in the schema's root `x-drift` block as `memory-pattern`,
// because the compiled document has no `memory` key for a property to carry
// it. A machine whose cached schema predates that move holds it on
// `atomicEntry.properties.memory` instead, so that is read when the root block
// has none: returning nil there would silently skip the check rather than
// fail it. Both carry the same pattern.
func memoryPattern() *regexp.Regexp {
	raw, err := common.LocalDriftfileSchema()
	if err != nil {
		return nil
	}
	var doc struct {
		XDrift struct {
			MemoryPattern string `json:"memory-pattern"`
		} `json:"x-drift"`
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
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	pattern := doc.XDrift.MemoryPattern
	if pattern == "" {
		pattern = doc.Definitions.AtomicEntry.Properties.Memory.Pattern
	}
	if pattern == "" {
		return nil
	}
	re, cerr := regexp.Compile(pattern)
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

// optDir reads a slot's own folder, when it names one, and refuses one that is
// not on this machine at the attribute.
func (c *compiler) optDir(entry Node, expr hcl.Expression, ptr, what string) {
	if v, ok := c.ev.str(expr, what+": dir"); ok {
		entry["dir"] = v
		c.at(ptr+"/dir", expr.Range(), what)
		c.checkPath(v, true, expr.Range(), what+": dir")
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

func (c *compiler) compileBackbone(f *dfFile) {
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

	c.compileRealtime(f.Realtime, bb)

	if len(bb) > 0 {
		c.doc["backbone"] = map[string]any(bb)
	}
}

// compileRealtime writes the client-channel list. The block is the list, so a
// block that names none says nothing and is refused rather than read as "close
// everything": that is `client_channels = []`.
func (c *compiler) compileRealtime(blocks []dfRealtime, bb Node) {
	if len(blocks) == 0 {
		return
	}
	if len(blocks) > 1 {
		c.ev.errorf(blocks[1].DefRange, "Duplicate realtime block", "a slice has one realtime block.")
		return
	}
	r := blocks[0]
	c.at("/backbone/realtime", r.DefRange, "realtime")
	if !present(r.ClientChannels) {
		c.ev.errorf(r.DefRange, "Missing client_channels",
			`realtime names the channels a visitor may publish to: client_channels = ["chat"], or [] to close every channel.`)
		return
	}
	if l, ok := c.ev.strList(r.ClientChannels, "realtime: client_channels"); ok {
		bb["realtime"] = map[string]any{"client_channels": toAnyList(l)}
		c.at("/backbone/realtime/client_channels", r.ClientChannels.Range(), "realtime")
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
	bytes, err := parseSize(raw)
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

func (c *compiler) compileSites(sites []dfSite) {
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
		// The root is written with no path: that is the spelling the slice
		// stores under the slug `default`.
		if s.Path != "/" {
			e["path"] = s.Path
		}
		list = append(list, e)
	}
	if len(list) > 0 {
		c.doc["canvas"] = map[string]any{"sites": list}
	}
}

func (c *compiler) compileEdge(edges []dfEdge) {
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
				c.ev.errorf(e.RateLimit.Range(), "Not a rate", "%v.", err)
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
		// The gate says which paths it covers, always. A key that defaulted to
		// "every path" would be a gate that locks the whole site because a line
		// was left out, and one that defaulted to "none" would be a gate that
		// protects nothing, so neither is a default.
		if !present(sa.Paths) {
			c.ev.errorf(sa.DefRange, "Missing paths", `edge.site_auth names the paths it gates: paths = ["*"] for the whole site, or paths = ["/admin"] for one part of it.`)
		} else if l, ok := c.ev.strList(sa.Paths, "edge.site_auth: paths"); ok {
			if norm, err := normaliseSitePaths(l); err != nil {
				c.ev.errorf(sa.Paths.Range(), "Not a path list", "edge.site_auth: paths: %v.", err)
			} else {
				c.at("/edge/site_auth/paths", sa.Paths.Range(), "edge.site_auth: paths")
				gate["paths"] = toAnyList(norm)
			}
		}
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
		// Empty rather than nil: `hosts = {}` is a declaration that clears the
		// slice's fronted hosts, and the document must carry it as an empty list.
		list := []any{}
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

func (c *compiler) compileHooksAndTests(hooks []dfHook, tests []dfTest) {
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

// compileEnvironments validates every environment block, whichever one the
// file is compiled for: each is its own slice, planned and applied by name.
func (c *compiler) compileEnvironments(envs []dfEnvironment) {
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
		if present(e.Billing) {
			c.ev.errorf(e.Billing.Range(), "billing is gone", "%s: %s", what, billingGone)
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
	expr func(*dfLimits) hcl.Expression
	unit limitUnit
	at   func(*SliceConfig) *int
}

// shapeLimits is the whole mapping from `limits` to the shape.
var shapeLimits = []shapeLimit{
	{"requests_per_minute", func(l *dfLimits) hcl.Expression { return l.RequestsPerMinute }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfRequestsPerMinute }},
	{"timeout", func(l *dfLimits) hcl.Expression { return l.Timeout }, unitSeconds, func(c *SliceConfig) *int { return &c.Atomic.MaxFunctionRuntimeInSeconds }},
	{"log_retention", func(l *dfLimits) hcl.Expression { return l.LogRetention }, unitHours, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfHoursForLogRetention }},
	{"deploy_history", func(l *dfLimits) hcl.Expression { return l.DeployHistory }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfDeploymentsInHistory }},
	{"backup_retention", func(l *dfLimits) hcl.Expression { return l.BackupRetention }, unitDays, func(c *SliceConfig) *int { return &c.Backbone.BackupRetentionDays }},
	{"scheduled_jobs", func(l *dfLimits) hcl.Expression { return l.ScheduledJobs }, unitCount, func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfScheduledJobs }},
	{"atomic_storage", func(l *dfLimits) hcl.Expression { return l.AtomicStorage }, unitSize, func(c *SliceConfig) *int { return &c.Atomic.MaxStorageBytes }},
	{"canvas_storage", func(l *dfLimits) hcl.Expression { return l.CanvasStorage }, unitSize, func(c *SliceConfig) *int { return &c.Canvas.TotalMaxSizeInBytes }},
	{"realtime_connections", func(l *dfLimits) hcl.Expression { return l.RealtimeConnections }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Realtime.MaxConcurrentConnections }},
	{"secrets", func(l *dfLimits) hcl.Expression { return l.Secrets }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxCount }},
	{"secret_size", func(l *dfLimits) hcl.Expression { return l.SecretSize }, unitSize, func(c *SliceConfig) *int { return &c.Backbone.Secrets.MaxSizeInBytesEach }},
	{"locks", func(l *dfLimits) hcl.Expression { return l.Locks }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Locks.MaxConcurrent }},
	{"blob_object_size", func(l *dfLimits) hcl.Expression { return l.BlobObjectSize }, unitSize, func(c *SliceConfig) *int { return &c.Backbone.Blobs.MaxSizeInBytesEach }},
	{"queue_depth", func(l *dfLimits) hcl.Expression { return l.QueueDepth }, unitCount, func(c *SliceConfig) *int { return &c.Backbone.Queues.MaxDepthEach }},
	{"vault_entry_size", func(l *dfLimits) hcl.Expression { return l.VaultEntrySize }, unitSize, func(c *SliceConfig) *int { return &c.Deed.Vault.MaxSizeInBytesEach }},
	{"vault_entries", func(l *dfLimits) hcl.Expression { return l.VaultEntries }, unitCount, func(c *SliceConfig) *int { return &c.Deed.Vault.MaxEntriesPerUID }},
	{"pocket_record_size", func(l *dfLimits) hcl.Expression { return l.PocketRecordSize }, unitSize, func(c *SliceConfig) *int { return &c.Deed.Pocket.MaxSizeInBytesEach }},
}

// compileLimits settles the slice-wide part of the shape twice, because the file
// does not say how the slice is paid for and both readings are real: as a
// configured slice (a limit left out is 0, the slot and scheduled-job counts are
// the file's), and as a slice inside the free grant (a limit left out is the
// free preset's figure). Which one is applied is chosen later, by Use, from the
// account's free slots; whether the second is available at all is FitsFree.
//
// Both passes read the same text, so the second one's refusals are the first
// one's again and are dropped.
func (c *compiler) compileLimits(f *dfFile) {
	if len(f.Slice) != 1 {
		return
	}
	base := c.out.Shape
	priced := c.limitsFor(f, base, false)
	seen := len(c.ev.diags)
	free := c.limitsFor(f, base, true)
	c.ev.diags = c.ev.diags[:seen]

	c.out.PricedShape, c.out.FreeShape, c.out.Shape = priced, free, priced
	if !c.ev.diags.HasErrors() {
		c.out.FreeMisfit = freeEnvelopeViolation(free, c.out.Domains)
		// The second pass's own refusals were dropped above, and one of them is a
		// fact about the free reading rather than a fault in the file: more
		// scheduled pulses than the free slice allows jobs for. That is a shape
		// the free slice cannot hold.
		if c.out.FreeMisfit == nil && free.Atomic.MaxNumberOfScheduledJobs < c.crons {
			c.out.FreeMisfit = fmt.Errorf("%d pulses carry a cron, and the free slice grants %d scheduled job%s",
				c.crons, free.Atomic.MaxNumberOfScheduledJobs, map[bool]string{true: "", false: "s"}[free.Atomic.MaxNumberOfScheduledJobs == 1])
		}
		c.out.FitsFree = c.out.FreeMisfit == nil
	}
}

// limitsFor is the shape the file declares when it is applied as a free slice
// (free) or as a configured one.
func (c *compiler) limitsFor(f *dfFile, shape SliceConfig, free bool) SliceConfig {
	sh := &shape
	rateFromEdge := sh.Atomic.MaxNumberOfRequestsPerMinute

	// A free slice is provisioned from the free preset: what the file leaves
	// out is the preset's figure. The slot and database counts and the shared
	// pool are the preset's too, because the free create path takes every
	// scalar from it.
	written := map[string]bool{}
	if free {
		for _, l := range freeLimits {
			*l.at(sh) = *l.at(&freePreset)
		}
	}

	var lim *dfLimits
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

	if !free {
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
	return shape
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
		b, err := parseSize(raw)
		if err != nil {
			c.ev.errorf(expr.Range(), "Not a size", "%s %s.", what, err)
			return 0, false
		}
		return b, true
	}
	secs, err := parseDuration(raw)
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
		key := strings.TrimPrefix(strings.TrimPrefix(loc, blockPointer(c.ranges, best, loc)), "/")
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

// blockPointer is the pointer of the block a refusal belongs to: the shortest
// one recorded for the same block that leads to loc. What follows it in loc is
// the key the refusal is about, so an attribute's own refusal names the
// attribute.
func blockPointer(ranges map[string]docRange, want docRange, loc string) string {
	shortest, found := "", false
	for ptr, r := range ranges {
		if r.what != want.what || (loc != ptr && !strings.HasPrefix(loc, ptr+"/")) {
			continue
		}
		if !found || len(ptr) < len(shortest) {
			shortest, found = ptr, true
		}
	}
	return shortest
}

// ─── units ──────────────────────────────────────────────────────────────────

var sizeRe = regexp.MustCompile(`^([0-9]+)(KiB|MiB|GiB)$`)

// parseSize reads a Driftfile size: whole KiB, MiB or GiB. "MB" is refused
// rather than read as MiB, so a size means one number whoever reads it.
func parseSize(s string) (int, error) {
	m := sizeRe.FindStringSubmatch(s)
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

var durationRe = regexp.MustCompile(`^([0-9]+)(s|m|h|d)$`)

// parseDuration reads "30s", "24h", "3d" as seconds.
func parseDuration(s string) (int, error) {
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a duration: write a whole number of s, m, h or d, e.g. \"24h\"", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1<<24 {
		return 0, fmt.Errorf("%q is too long", s)
	}
	return n * map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400}[m[2]], nil
}

// sizeLabel renders bytes the way a Driftfile writes them.
func sizeLabel(b int) string {
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
