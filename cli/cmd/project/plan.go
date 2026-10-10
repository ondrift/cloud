package project

// plan.go — `drift plan`: what applying this Driftfile would do to the slice.
//
// The plan is a real diff. It compiles the file, reads the live slice (its
// shape, the code each slot runs, its sites, secrets, keys and edge), asks the
// platform to price the compiled shape, and prints every difference with what
// it costs, whether the slice restarts, and what it removes. A slice that does
// not exist yet is "would create", priced.
//
// # Read-only, and checked
//
// Every request a plan makes is a GET, except one: POST /ops/slice/price,
// which quotes a shape and writes nothing (the platform's own handler says so:
// it does not touch the database, record usage, or bind a quote). The live
// slice is the state, so there is nothing local to write either.
// TestPlan_IssuesNoWriteButThePriceQuote holds every request to that.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// GetPlanCmd builds `drift plan`.
func GetPlanCmd() *cobra.Command {
	var (
		vars      []string
		secrets   []string
		noEnvFile bool
		outFile   string
	)
	cmd := &cobra.Command{
		Use:   "plan [environment]",
		Short: "Show what applying the Driftfile would change on its slice, and what it would cost",
		Long: `Compare the Driftfile with the live slice and print every difference: what is
added, changed and removed, the monthly price before and after (quoted by the
platform), whether the slice restarts, and what data a removal could lose.

Nothing is changed.`,
		Example: "  drift plan\n  drift plan staging\n  drift plan --var RESET_KEY=...",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := requireDriftfile()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			overrides := append(append([]string{}, vars...), secrets...)
			env := ""
			if len(args) == 1 {
				env = args[0]
			}

			sum, err := applyVariableSources(filepath.Dir(path), overrides, !noEnvFile, env)
			if err != nil {
				return err
			}
			sum.report()

			common.RefreshDriftfileSchema()
			if !SchemaAvailable() {
				return ErrNoSchema
			}
			c, err := CompileDriftfileEnv(path, env)
			if err != nil {
				return err
			}
			if env != "" {
				fmt.Printf("  %s environment %s → slice %s\n", common.Hint("·"), env, common.Highlight(c.Manifest.Name()))
			}

			// Every read below names this file's slice. The ones that resolve a
			// slice from the session read the active one, so for this process
			// the active slice is the file's. Nothing is written to the session.
			if err := os.Setenv(common.SliceEnv, c.Manifest.Name()); err != nil {
				return err
			}

			p, err := gatherPlan(c)
			if err != nil {
				return err
			}
			p.render(os.Stdout)
			if outFile != "" {
				if err := writePlanFile(outFile, data, p, p.diff()); err != nil {
					return fmt.Errorf("write the plan file: %w", err)
				}
				fmt.Printf("  Plan written to %s: `drift apply %s` applies exactly this, or refuses if anything moved.\n\n", outFile, outFile)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outFile, "out", "", "Write the plan to this file, for `drift apply <file>` to apply exactly it")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Set a variable env() reads: KEY=value (repeatable). Yields to the terminal's environment; beats the .env file.")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "The same as --var.")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file beside the Driftfile")
	return cmd
}

// ─── what the plan reads ────────────────────────────────────────────────────

type liveAPIKey struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

// planState is everything a plan compares: the compiled file, and each live
// fact with the error that read it, so a fact that could not be read is said
// to be unknown rather than shown as absent.
type planState struct {
	c    *Compiled
	user string

	live *LiveSlice // nil: the slice does not exist

	deployed    map[string]string // deployed key → digest; present = deployed
	deployedErr error
	local       map[string]string // booking key → the digest this file would deploy
	buildErr    error

	sites      []liveCanvasSite
	sitesErr   error
	secrets    []string
	secretsErr error
	keys       map[string][]liveAPIKey // booking key → live keys
	keysErr    error
	gate       siteAuthConfig
	gateErr    error
	links      []string
	linksErr   error
	allow      []string
	allowErr   error
	hosts      []frontedHost
	hostsErr   error
	egress     liveEgressView
	egressErr  error
	domains    []liveDomain
	domainsErr error

	priceNew, priceOld       int
	priceNewErr, priceOldErr error
}

// gatherPlan reads the live slice. Every read is a GET but the price quote.
func gatherPlan(c *Compiled) (*planState, error) {
	m := c.Manifest
	p := &planState{c: c, user: common.GetUsername()}

	// The build reads the source beside the file: which language each element
	// is, where each handler lives. A failure here is what an apply would hit,
	// so it is reported; the digests it would have given are then unknown.
	specs := AtomicSpecs(m)
	els, berr := atomic_cmd.BuildElements(specs)
	if errors.Is(berr, atomic_cmd.ErrFieldLanguage) || errors.Is(berr, atomic_cmd.ErrConcurrency) {
		return nil, berr
	}
	p.buildErr = berr
	if berr == nil {
		// The compiled floor needs the LANGUAGE, which only the build knows.
		// The bookings it checks are the file's own.
		if err := CheckSlotBookings(m, els, &LiveSlice{Config: c.Shape}); err != nil {
			return nil, err
		}
		atomic_cmd.SetDeclaredSchedules(declaredSchedules(m))
		p.local = map[string]string{}
		for _, el := range els {
			digest, derr := atomic_cmd.ElementDigest(el.Dir, el.Name)
			if derr != nil {
				continue
			}
			for _, f := range el.Funcs {
				p.local[f.Spec.Name] = atomic_cmd.DeployDigest(digest, f.Spec)
			}
		}
	}

	live, err := FetchLiveSlice(m.Name())
	if err != nil {
		return nil, err
	}
	p.live = live

	newPrice := func() {
		if c.Billing == "free" {
			return // the free slice costs nothing, by its tier
		}
		p.priceNew, p.priceNewErr = quotePrice(c.Shape)
	}
	if live == nil {
		newPrice()
		return p, nil
	}

	if fns, ferr := atomic_cmd.DeployedFunctions(); ferr != nil {
		p.deployedErr = ferr
	} else {
		p.deployed = map[string]string{}
		for _, f := range fns {
			p.deployed[f.Key] = f.Digest
		}
	}

	p.sites, p.sitesErr = fetchLiveCanvasSites()
	p.secrets, p.secretsErr = fetchSecretNames()
	p.keys, p.keysErr = fetchAPIKeys(c)
	if m.Slice().Has("edge", "site_auth") {
		p.gate, p.gateErr = fetchSiteAuth(m.Name())
	}
	if m.Slice().Has("edge", "links") {
		if ls, lerr := slice.ListLinks(m.Name()); lerr != nil {
			p.linksErr = lerr
		} else {
			for _, l := range ls {
				p.links = append(p.links, strings.ToLower(strings.TrimSpace(l.Target)))
			}
		}
	}
	if m.Slice().Has("edge", "allow") {
		var a struct {
			CIDRs []string `json:"cidrs"`
		}
		body, aerr := callSliceEdge(m, http.MethodGet, "allow", "read the inbound allowlist", nil)
		if aerr == nil {
			aerr = json.Unmarshal(body, &a)
		}
		p.allow, p.allowErr = canonicalCIDRs(a.CIDRs), aerr
	}
	if m.Slice().Has("edge", "hosts") {
		var h struct {
			Hosts []frontedHost `json:"hosts"`
		}
		body, herr := callSliceEdge(m, http.MethodGet, "hosts", "read the fronted hosts", nil)
		if herr == nil {
			herr = json.Unmarshal(body, &h)
		}
		p.hosts, p.hostsErr = h.Hosts, herr
	}
	if m.Slice().Has("edge", "egress") {
		p.egress, p.egressErr = fetchLiveEgress()
	}
	if m.Slice().Has("edge", "domains") {
		p.domains, p.domainsErr = fetchLiveDomains()
	}

	newPrice()
	if !liveIsFree(live) {
		p.priceOld, p.priceOldErr = quotePrice(live.Config)
	}
	return p, nil
}

// liveIsFree reports whether the live slice is the free slice.
func liveIsFree(live *LiveSlice) bool { return live != nil && live.Tier == "hacker" }

// quotePrice asks the platform what a shape costs a month. The quote is the
// platform's: the CLI never prices.
func quotePrice(cfg SliceConfig) (int, error) {
	body, err := json.Marshal(map[string]any{"config": cfg, "billing_period_months": 1})
	if err != nil {
		return 0, err
	}
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/price", bytes.NewReader(body))
	if err != nil {
		return 0, common.TransportError("price the shape", err)
	}
	defer resp.Body.Close()
	raw, err := common.CheckResponse(resp, "price the shape")
	if err != nil {
		return 0, err
	}
	var q struct {
		MonthlyCents int `json:"monthly_cents"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		return 0, fmt.Errorf("price the shape: the platform sent a reply this CLI cannot read")
	}
	return q.MonthlyCents, nil
}

func fetchSecretNames() ([]string, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/backbone/secret/list", nil)
	if err != nil {
		return nil, common.TransportError("list secrets", err)
	}
	defer resp.Body.Close()
	raw, err := common.CheckResponse(resp, "list secrets")
	if err != nil {
		return nil, err
	}
	var names []string
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &names); err != nil {
			return nil, fmt.Errorf("list secrets: the platform sent a reply this CLI cannot read")
		}
	}
	return names, nil
}

// fetchAPIKeys reads the live keys of every Pulse the file gates with one.
//
// The slice stores a key under the path `drift atomic auth set` was given,
// and its gate accepts two spellings: `<element>/<route>` and the bare route.
// Its list matches the stored path exactly, so both are asked.
func fetchAPIKeys(c *Compiled) (map[string][]liveAPIKey, error) {
	elementOf := map[string]string{}
	for _, p := range PulseEntries(c.Manifest.Slice()) {
		elementOf[p.Str("name")] = p.Str("element")
	}
	out := map[string][]liveAPIKey{}
	for _, w := range c.WriteOnly {
		if w.Kind != "api_key" {
			continue
		}
		key := bookingKeyOf(c, w.Address)
		_, route, _ := strings.Cut(key, ":")
		paths := []string{route}
		if el := elementOf[key]; el != "" {
			paths = append(paths, el+"/"+route)
		}
		for _, path := range paths {
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/auth?function="+url.QueryEscape(path), nil)
			if err != nil {
				return nil, common.TransportError("list API keys", err)
			}
			raw, cerr := common.CheckResponse(resp, "list API keys")
			resp.Body.Close() // #nosec G104 -- the body has been read in full
			if cerr != nil {
				return nil, cerr
			}
			var keys []liveAPIKey
			if len(bytes.TrimSpace(raw)) > 0 {
				if jerr := json.Unmarshal(raw, &keys); jerr != nil {
					return nil, fmt.Errorf("list API keys: the platform sent a reply this CLI cannot read")
				}
			}
			out[key] = append(out[key], keys...)
		}
	}
	return out, nil
}

// keyFingerprint is the slice's own fingerprint of a key: "..." and its last
// four bytes (slice/src/atomic/auth.rs). It is only ever compared, never
// printed for a key of four bytes or fewer, where it is the whole key.
func keyFingerprint(key string) string {
	if len(key) > 4 {
		return "..." + key[len(key)-4:]
	}
	return key
}

// bookingKeyOf finds the booking key an address was compiled from.
func bookingKeyOf(c *Compiled, address string) string {
	for k, a := range c.Addresses {
		if a == address {
			return k
		}
	}
	return ""
}

// ─── the diff ───────────────────────────────────────────────────────────────

type planLine struct {
	mark, address, detail string
}

// planDiff is the comparison, before it is printed.
type planDiff struct {
	lines    []planLine
	notes    []string
	removes  []string
	dataLoss []string
	changed  bool
	// shapeChanged is whether the slice's shape differs from the file's: a
	// booking, a storage size or a limit. Apply resizes only then.
	shapeChanged bool
	// billingChanged is whether the file moves the slice between free and
	// monthly.
	billingChanged bool
}

func (d *planDiff) add(mark, address, format string, args ...any) {
	d.lines = append(d.lines, planLine{mark, address, fmt.Sprintf(format, args...)})
	if mark != " " {
		d.changed = true
	}
}

// addressOf renders a booking key the way the file addresses it, for a slot
// the file names and for one only the live slice has.
func addressOf(c *Compiled, key string) string {
	if a, ok := c.Addresses[key]; ok {
		return a
	}
	method, route, _ := strings.Cut(key, ":")
	switch method {
	case "field":
		return "field " + route
	case "queue":
		return "pulse QUEUE " + route
	}
	return "pulse " + strings.ToUpper(method) + " /" + route
}

func (p *planState) diff() *planDiff {
	d := &planDiff{}
	c := p.c
	var live SliceConfig
	if p.live != nil {
		live = p.live.Config
	}
	creating := p.live == nil

	// Slots: the booking, then the code.
	want := map[string]AtomicFunction{}
	for _, f := range c.Shape.Atomic.Functions {
		want[f.Name] = f
	}
	have := map[string]AtomicFunction{}
	for _, f := range live.Atomic.Functions {
		have[f.Name] = f
	}
	for _, key := range unionKeys(want, have) {
		w, inFile := want[key]
		h, inLive := have[key]
		addr := addressOf(c, key)
		deployedDigest, deployed := p.deployed[deployedKeyOf(key)]
		if inFile != inLive || w.MemoryBytes != h.MemoryBytes {
			d.shapeChanged = true
		}
		switch {
		case inFile && !inLive:
			d.add("+", addr, "books %s, and deploys", sizeLabel(w.MemoryBytes))
		case !inFile && inLive:
			d.removes = append(d.removes, addr)
			switch {
			case p.deployedErr != nil:
				d.add("-", addr, "booking removed (whether code is deployed in it is unknown: %v)", p.deployedErr)
			case deployed:
				d.add("-", addr, "deployed code, triggers and history are deleted")
			default:
				d.add("-", addr, "booking removed (nothing is deployed in it)")
			}
		default:
			var changes []string
			if w.MemoryBytes != h.MemoryBytes {
				changes = append(changes, fmt.Sprintf("memory %s → %s", sizeLabel(h.MemoryBytes), sizeLabel(w.MemoryBytes)))
				if w.MemoryBytes < h.MemoryBytes {
					d.removes = append(d.removes, addr+" (booking shrinks)")
				}
			}
			code, codeChanged := p.codeStatus(key, deployed, deployedDigest)
			if codeChanged {
				changes = append(changes, code)
			}
			if len(changes) > 0 {
				d.add("~", addr, "%s", strings.Join(changes, "; "))
			} else {
				d.add(" ", addr, "%s", code)
			}
		}
	}

	// Storage: databases, collections, buckets, queues.
	for _, set := range []struct {
		noun        string
		want, have  map[string]int
		depth, data bool
	}{
		{"sql", c.Shape.Backbone.SQL.Databases, live.Backbone.SQL.Databases, false, true},
		{"nosql", c.Shape.Backbone.NoSQL.Collections, live.Backbone.NoSQL.Collections, false, true},
		{"blobs", c.Shape.Backbone.Blobs.Buckets, live.Backbone.Blobs.Buckets, false, true},
		{"queue", c.Shape.Backbone.Queues.Queues, live.Backbone.Queues.Queues, true, true},
	} {
		label := func(v int) string {
			if set.depth {
				if v <= 0 {
					return "the slice's queue depth"
				}
				return fmt.Sprintf("%d messages", v)
			}
			return sizeLabel(v)
		}
		for _, name := range unionKeys(set.want, set.have) {
			w, inFile := set.want[name]
			h, inLive := set.have[name]
			addr := set.noun + " " + name
			if inFile != inLive || w != h {
				d.shapeChanged = true
			}
			switch {
			case inFile && !inLive:
				d.add("+", addr, "%s", label(w))
			case !inFile && inLive:
				d.add("-", addr, "%s, and what it holds, are removed", label(h))
				d.removes = append(d.removes, addr)
				d.dataLoss = append(d.dataLoss, addr+" (removed)")
			case w != h:
				d.add("~", addr, "%s → %s", label(h), label(w))
				if w < h && !(set.depth && w <= 0) {
					d.dataLoss = append(d.dataLoss, fmt.Sprintf("%s (shrinks to %s: refused if it holds more)", addr, label(w)))
				}
			default:
				d.add(" ", addr, "unchanged")
			}
		}
	}

	p.diffSites(d, creating)
	p.diffSecrets(d, creating)
	p.diffKeys(d, creating)
	p.diffEdge(d, creating)

	// Limits, one line each where the file and the slice differ.
	for _, l := range planLimits() {
		w, h := *l.at(&c.Shape), *l.at(&live)
		switch {
		case creating:
			if w != 0 {
				d.add("+", l.name, "%s", l.format(w))
			}
		case w != h:
			d.shapeChanged = true
			d.add("~", l.name, "%s → %s", l.format(h), l.format(w))
			if h != 0 && w == 0 {
				// resize_guards lists a limit going to zero as a removal.
				d.removes = append(d.removes, l.name+" (set to the platform default)")
			}
			if l.name == "limits.backup_retention" && w < h {
				d.dataLoss = append(d.dataLoss, fmt.Sprintf("limits.backup_retention (backups older than %s are deleted on the next nightly run)", l.format(w)))
			}
		}
	}

	if !creating {
		liveBilling := "monthly"
		if liveIsFree(p.live) {
			liveBilling = "free"
		}
		if liveBilling != c.Billing {
			d.billingChanged = true
			d.add("~", "billing", "%s → %s", liveBilling, c.Billing)
		}
		for _, ch := range diffLabels(c.Shape.Labels, live.Labels) {
			d.add("~", "label "+ch.Key, "%s", describeLabelChange(ch))
		}
	}
	return d
}

// codeStatus says whether a slot's code and declaration match what is
// deployed, by the same digest the deploy skips unchanged work with.
func (p *planState) codeStatus(key string, deployed bool, digest string) (string, bool) {
	local, known := p.local[key]
	switch {
	case p.deployedErr != nil:
		return fmt.Sprintf("code not compared (%v)", p.deployedErr), false
	case !deployed:
		return "not deployed yet: deploys", true
	case p.buildErr != nil || !known || local == "":
		return "code not compared (the build cannot read it here)", false
	case digest == local:
		return "unchanged (digest " + shortDigest(digest) + ")", false
	}
	return fmt.Sprintf("code or declaration changed (digest %s → %s)", shortDigest(digest), shortDigest(local)), true
}

func shortDigest(d string) string {
	if len(d) > 4 {
		return d[:4] + "…"
	}
	return d
}

// deployedKeyOf is the key /ops/atomic/list reports a slot under, which is
// the booking key for everything but a queue Pulse.
func deployedKeyOf(bookingKey string) string {
	method, name, _ := strings.Cut(bookingKey, ":")
	return atomic_cmd.DeployedKey(method, name)
}

func (p *planState) diffSites(d *planDiff, creating bool) {
	declared, err := canvasSites(p.c.Manifest)
	if err != nil {
		d.notes = append(d.notes, err.Error())
		return
	}
	liveSlugs := map[string]bool{}
	for _, s := range p.sites {
		liveSlugs[s.Slug] = true
	}
	named := map[string]bool{}
	for _, s := range declared {
		named[s.Slug] = true
		addr := "site " + s.Route
		switch {
		case creating:
			d.add("+", addr, "uploads %s", s.Dir)
		case p.sitesErr != nil:
			d.add(" ", addr, "live sites unknown (%v)", p.sitesErr)
		case !liveSlugs[s.Slug]:
			d.add("+", addr, "uploads %s", s.Dir)
		default:
			// The slice keeps no hash of a site's files, so a plan cannot say
			// whether they differ; apply uploads the folder again.
			d.add(" ", addr, "served; uploaded again on apply (its files are not compared)")
		}
	}
	for _, s := range p.sites {
		if !named[s.Slug] {
			d.add("-", "site "+s.Route, "still served: no drift command removes a site yet")
		}
	}
}

func (p *planState) diffSecrets(d *planDiff, creating bool) {
	live := map[string]bool{}
	for _, n := range p.secrets {
		live[n] = true
	}
	named := map[string]bool{}
	for _, w := range p.c.WriteOnly {
		if w.Kind != "secret" {
			continue
		}
		name := strings.TrimPrefix(w.Address, "secret ")
		named[name] = true
		set := envSet(w.Env)
		switch {
		case !set:
			d.add("!", w.Address, "$%s is not set here: apply would refuse", w.Env)
		case creating || (p.secretsErr == nil && !live[name]):
			d.add("+", w.Address, "set from $%s", w.Env)
		case p.secretsErr != nil:
			d.add(" ", w.Address, "live secrets unknown (%v)", p.secretsErr)
		default:
			// A secret's value is never read back, so it is set again.
			d.add(" ", w.Address, "present; set again from $%s on apply (write-only, not compared)", w.Env)
		}
	}
	names := append([]string{}, p.secrets...)
	sort.Strings(names)
	for _, n := range names {
		if !named[n] {
			d.add("-", "secret "+n, "deleted")
			d.removes = append(d.removes, "secret "+n)
		}
	}
}

func (p *planState) diffKeys(d *planDiff, creating bool) {
	for _, w := range p.c.WriteOnly {
		if w.Kind != "api_key" {
			continue
		}
		addr := w.Address + " key"
		key := bookingKeyOf(p.c, w.Address)
		method, _, _ := strings.Cut(key, ":")
		set := envSet(w.Env)
		var live *liveAPIKey
		for i, k := range p.keys[key] {
			if strings.EqualFold(k.Method, method) {
				live = &p.keys[key][i]
			}
		}
		switch {
		case !set:
			d.add("!", addr, "$%s is not set here: apply would refuse", w.Env)
		case creating:
			d.add("+", addr, "set from $%s", w.Env)
		case p.keysErr != nil:
			d.add(" ", addr, "live key unknown (%v)", p.keysErr)
		case live == nil:
			d.add("+", addr, "set from $%s", w.Env)
		case keyFingerprint(os.Getenv(w.Env)) == live.Fingerprint:
			// The slice fingerprints a key by its last four characters, so a
			// match says the key is very likely the same one; it is not rotated.
			d.add(" ", addr, "unchanged (fingerprint matches $%s)", w.Env)
		default:
			d.add("~", addr, "rotates to the key in $%s", w.Env)
		}
	}
}

func (p *planState) diffEdge(d *planDiff, creating bool) {
	m := p.c.Manifest
	sl := m.Slice()

	if sl.Has("edge", "site_auth") {
		realm, entries, err := declaredSiteAuth(m)
		if err != nil {
			d.notes = append(d.notes, err.Error())
		} else {
			liveUsers := map[string]bool{}
			for _, u := range p.gate.Users {
				liveUsers[u.Name] = true
			}
			switch {
			case creating || (p.gateErr == nil && !p.gate.Enabled):
				d.add("+", "site_auth", "the gate is turned on (realm %s)", realmOrNone(realm))
			case p.gateErr != nil:
				d.add(" ", "site_auth", "live gate unknown (%v)", p.gateErr)
			case p.gate.Realm != realm:
				d.add("~", "site_auth", "realm %s → %s", realmOrNone(p.gate.Realm), realmOrNone(realm))
			default:
				d.add(" ", "site_auth", "on (realm %s)", realmOrNone(realm))
			}
			for _, e := range entries {
				addr := "site_auth " + e.User
				switch {
				case !envSet(e.Env):
					d.add("!", addr, "$%s is not set here: apply would refuse", e.Env)
				case creating || !liveUsers[e.User]:
					d.add("+", addr, "password from $%s", e.Env)
				default:
					d.add(" ", addr, "present; password set again from $%s on apply (write-only, not compared)", e.Env)
				}
			}
			if !creating && p.gateErr == nil {
				declared := siteAuthConfig{}
				for _, e := range entries {
					declared.Users = append(declared.Users, siteAuthUser{Name: e.User})
				}
				for _, name := range removedSiteAuthUsers(declared, p.gate) {
					d.add("-", "site_auth "+name, "removed")
					d.removes = append(d.removes, "site_auth "+name)
				}
			}
		}
	}

	if sl.Has("edge", "egress") {
		mode, hosts, _ := desiredEgress(m)
		switch {
		case creating:
			d.add("+", "egress", "%s %s", mode, strings.Join(hosts, ", "))
		case p.egressErr != nil:
			d.add(" ", "egress", "live egress unknown (%v)", p.egressErr)
		case egressInSync(mode, hosts, p.egress):
			d.add(" ", "egress", "unchanged (%s)", mode)
		default:
			liveMode := p.egress.Mode
			if liveMode == "" {
				liveMode = "open"
			}
			d.add("~", "egress", "%s %s → %s %s", liveMode,
				strings.Join(p.egress.DeclaredHosts, ", "), mode, strings.Join(hosts, ", "))
		}
	}

	listDiff := func(noun string, want, have []string, herr error) {
		if herr != nil && !creating {
			d.add(" ", noun, "live %s unknown (%v)", noun, herr)
			return
		}
		haveSet, wantSet := map[string]bool{}, map[string]bool{}
		for _, h := range have {
			haveSet[h] = true
		}
		for _, w := range want {
			wantSet[w] = true
			if haveSet[w] && !creating {
				d.add(" ", noun+" "+w, "unchanged")
			} else {
				d.add("+", noun+" "+w, "added")
			}
		}
		for _, h := range have {
			if !wantSet[h] && !creating {
				d.add("-", noun+" "+h, "removed")
				d.removes = append(d.removes, noun+" "+h)
			}
		}
	}
	if sl.Has("edge", "links") {
		listDiff("link", declaredLinks(m), p.links, p.linksErr)
	}
	if sl.Has("edge", "allow") {
		cidrs, err := declaredAllow(m)
		if err != nil {
			d.notes = append(d.notes, err.Error())
		} else {
			listDiff("allow", canonicalCIDRs(cidrs), p.allow, p.allowErr)
		}
	}
	if sl.Has("edge", "hosts") {
		hs, err := declaredHosts(m)
		if err != nil {
			d.notes = append(d.notes, err.Error())
		} else {
			listDiff("host", hostLines(hs), hostLines(p.hosts), p.hostsErr)
		}
	}
	if sl.Has("edge", "domains") {
		var want, have []string
		for _, e := range sl.Entries("host", "edge", "domains") {
			want = append(want, strings.ToLower(strings.TrimSpace(e.Str("host"))))
		}
		for _, h := range p.domains {
			have = append(have, strings.ToLower(strings.TrimSpace(h.Host)))
		}
		// A domain the file does not name stays attached: apply adds what is
		// declared and reports the rest.
		var known []string
		for _, h := range have {
			for _, w := range want {
				if h == w {
					known = append(known, h)
				}
			}
		}
		listDiff("domain", want, known, p.domainsErr)
	}
}

// ─── limits, as the plan prints them ────────────────────────────────────────

type planLimit struct {
	name   string
	at     func(*SliceConfig) *int
	format func(int) string
}

// planLimits is every scalar of the shape the plan compares: the `limits`
// table, and the two counts the compiler derives.
func planLimits() []planLimit {
	out := []planLimit{
		{"slots", func(c *SliceConfig) *int { return &c.Atomic.MaxNumberOfFunctions }, func(v int) string { return fmt.Sprint(v) }},
		{"databases", func(c *SliceConfig) *int { return &c.Backbone.SQL.MaxDatabases }, func(v int) string { return fmt.Sprint(v) }},
	}
	for _, l := range shapeLimits {
		var f func(int) string
		switch l.unit {
		case unitSize:
			f = func(v int) string { return sizeLabel(v) }
		case unitSeconds:
			f = func(v int) string { return fmt.Sprintf("%ds", v) }
		case unitHours:
			f = func(v int) string { return fmt.Sprintf("%dh", v) }
		case unitDays:
			f = func(v int) string { return fmt.Sprintf("%dd", v) }
		default:
			f = func(v int) string { return fmt.Sprint(v) }
		}
		out = append(out, planLimit{"limits." + l.name, l.at, f})
	}
	return out
}

// LimitLine is one scalar of a compiled shape, named and formatted as the
// plan prints it.
type LimitLine struct {
	Name, Value string
}

// ShapeLimits is every scalar of a compiled shape that is not zero, as the
// plan prints it: the slot and database counts, then the `limits` table.
func ShapeLimits(c *Compiled) []LimitLine {
	var out []LimitLine
	for _, l := range planLimits() {
		if v := *l.at(&c.Shape); v != 0 {
			out = append(out, LimitLine{l.name, l.format(v)})
		}
	}
	return out
}

// ─── printing ───────────────────────────────────────────────────────────────

// render prints the plan, then how to apply it.
func (p *planState) render(out io.Writer) {
	d := p.renderBody(out)
	if !d.changed && p.live != nil {
		fmt.Fprintf(out, "  Nothing to change.\n\n")
		return
	}
	hint := "drift apply"
	flags := p.approvalFlags(d)
	fmt.Fprintf(out, "  Apply with:  %-16s (asks for each of the above)\n", hint)
	if len(flags) > 0 {
		fmt.Fprintf(out, "         or:   %s %s\n", hint, strings.Join(flags, " "))
	}
	fmt.Fprintln(out)
}

// approvalFlags are the flags that answer every question this plan raises,
// for a run with nobody to ask.
func (p *planState) approvalFlags(d *planDiff) []string {
	var flags []string
	if p.priceAsked() {
		flags = append(flags, fmt.Sprintf("--approve-price %d", p.priceNew))
	}
	for _, r := range d.removes {
		flags = append(flags, fmt.Sprintf("--approve-remove %q", guardAddress(r)))
	}
	for _, l := range d.dataLoss {
		flags = append(flags, fmt.Sprintf("--allow-data-loss %q", guardAddress(l)))
	}
	return flags
}

// priceAsked is whether applying this plan agrees to a new monthly figure: a
// slice created on monthly billing, or a monthly price that moves.
func (p *planState) priceAsked() bool {
	if p.c.Billing == "free" {
		return false
	}
	return p.live == nil || liveIsFree(p.live) || p.priceNew != p.priceOld
}

// guardAddress is what a removal or a data loss is named by on the command
// line: its address, without the explanation in brackets.
func guardAddress(s string) string { return strings.SplitN(s, " (", 2)[0] }

// renderBody prints the diff and its summary, and returns the diff.
func (p *planState) renderBody(out io.Writer) *planDiff {
	d := p.diff()
	c := p.c
	who := c.Manifest.Name()
	if p.user != "" {
		who = p.user + "/" + who
	}

	status := "unchanged"
	switch {
	case p.live == nil:
		status = "+ create"
	case d.changed:
		status = "~ change"
	}
	fmt.Fprintf(out, "\n  %-50s %s\n\n", "slice "+who, status)

	width := 24
	for _, l := range d.lines {
		if len(l.address) > width {
			width = len(l.address)
		}
	}
	for _, l := range d.lines {
		fmt.Fprintf(out, "  %s %-*s  %s\n", l.mark, width, l.address, l.detail)
	}
	for _, n := range d.notes {
		fmt.Fprintf(out, "  ! %s\n", n)
	}
	if p.buildErr != nil {
		fmt.Fprintf(out, "  ! the build cannot read this project, and apply would refuse: %v\n", p.buildErr)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "  Price     %s\n", p.priceLine())
	fmt.Fprintf(out, "  Restart   %s\n", p.restartLine())
	if len(d.removes) == 0 {
		fmt.Fprintf(out, "  Removes   nothing\n")
	} else {
		fmt.Fprintf(out, "  Removes   %s\n", strings.Join(d.removes, "\n            "))
	}
	if len(d.dataLoss) > 0 {
		fmt.Fprintf(out, "  Data      could be lost: %s\n", strings.Join(d.dataLoss, "\n                           "))
	}
	fmt.Fprintln(out)
	return d
}

func (p *planState) priceLine() string {
	euro := func(cents int) string { return fmt.Sprintf("€%d.%02d", cents/100, cents%100) }
	newLabel := ""
	switch {
	case p.c.Billing == "free":
		newLabel = "free"
	case p.priceNewErr != nil:
		return fmt.Sprintf("unknown: the platform could not quote the shape (%v)", p.priceNewErr)
	default:
		newLabel = euro(p.priceNew) + " a month"
	}
	if p.live == nil {
		return newLabel + "         (quoted by the platform)"
	}
	oldLabel := ""
	switch {
	case liveIsFree(p.live):
		oldLabel = "free"
	case p.priceOldErr != nil:
		return fmt.Sprintf("unknown → %s (the live shape could not be quoted: %v)", newLabel, p.priceOldErr)
	default:
		oldLabel = euro(p.priceOld) + " a month"
	}
	if oldLabel == newLabel {
		return newLabel + ", unchanged         (quoted by the platform)"
	}
	return oldLabel + " → " + newLabel + "         (quoted by the platform)"
}

// restartLine says whether the slice restarts. It restarts when its pod's
// memory limit changes, and that limit follows the SUM of the slots' bookings
// in whole MiB (slicegen); every other limit reaches the slice without one.
func (p *planState) restartLine() string {
	if p.live == nil {
		return "no: the slice is created"
	}
	sum := func(fs []AtomicFunction) int {
		t := 0
		for _, f := range fs {
			if f.MemoryBytes > 0 {
				t += f.MemoryBytes
			}
		}
		return t / bytesPerMiB
	}
	was, now := sum(p.live.Config.Atomic.Functions), sum(p.c.Shape.Atomic.Functions)
	if was == now {
		return "no"
	}
	return fmt.Sprintf("yes: the slice restarts once, because its total booked memory changes (%dMiB → %dMiB),\n"+
		"            which means a few seconds of errors", was, now)
}

func envSet(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

// unionKeys is the sorted union of two maps' keys.
func unionKeys[A, B any](a map[string]A, b map[string]B) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
