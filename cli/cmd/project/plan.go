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
//
// # A match is a claim about every fact
//
// A fact the plan could not read is unread, and a plan with one is never a match.
// The verdict (verdict.go) is the one value that decides the plan's header, its
// closing line and the status `--exit-code` exits with, and VerdictUnread says
// what unread means.

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
		exitCode  bool
	)
	cmd := &cobra.Command{
		Use:   "plan [environment]",
		Short: "Show what applying the Driftfile would change on its slice, and what it would cost",
		Long: `Compare the Driftfile with the live slice and print every difference: what is
added, changed and removed, the monthly price before and after (quoted by the
platform), whether the slice restarts, and what data a removal could lose.

Nothing is changed.

A plan ends one of four ways. The slice line at its top says which, and a plan
with nothing to apply says it again on its last line:

  create   the slice does not exist, and applying creates it
  differs  a line carries a mark, and applying adds, changes or removes
           something, or refuses
  unread   no line carries a mark, but a fact could not be read, so the plan
           says "Nothing differs in what could be read." and not that the slice
           is the file
  match    no line carries a mark and every fact was read: "Nothing to change."

A fact the plan could not read, such as the slice's sites, secrets, keys, alerts,
edge settings or deployed functions, or the build of the project, is unread. The
plan never takes it for a match: when nothing else differs, its last line says so
and lists each unread fact with why. A difference found is a difference even when
another fact is unread.

A value that is write-only by design (a secret, an API key, a gate password)
is not unread: the platform never gives it back, so the plan cannot compare the
value, and its line says what apply does with it.

A script reads the verdict from the exit status with --exit-code: 0 when it is a
match, 2 when it is create or differs, 3 when it is unread, 1 when the plan
fails. Without the flag a difference exits 0.`,
		Example: "  drift plan\n  drift plan staging\n  drift plan --var RESET_KEY=...\n  drift plan --exit-code",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := PlanOptions{
				Variables: append(append([]string{}, vars...), secrets...),
				NoEnvFile: noEnvFile,
			}
			if len(args) == 1 {
				o.Environment = args[0]
			}

			p, data, err := readPlan(o, os.Stdout, os.Stderr)
			if err != nil {
				return err
			}
			v := p.render(os.Stdout)
			if outFile != "" {
				if err := writePlanFile(outFile, data, p, p.diff()); err != nil {
					return fmt.Errorf("write the plan file: %w", err)
				}
				fmt.Printf("  Plan written to %s: `drift apply %s` applies exactly this, or refuses if anything moved.\n\n", outFile, outFile)
			}
			if exitCode {
				return v.exitError()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outFile, "out", "", "Write the plan to this file, for `drift apply <file>` to apply exactly it")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "Exit 0 when the plan is a match, 2 when it is create or differs, 3 when it is unread, 1 when it fails")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Set a variable env() reads: KEY=value (repeatable). Yields to the terminal's environment; beats the .env file.")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "The same as --var.")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file beside the Driftfile")
	return cmd
}

// ─── what the plan reads ────────────────────────────────────────────────────

// PlanOptions say which plan to read: the choices `drift plan` is given.
type PlanOptions struct {
	// Environment names the Driftfile environment to plan, or "" for the
	// default slice.
	Environment string
	// Variables are KEY=value settings for the variables the file reads, as
	// --var and --secret give them. They yield to the terminal's environment
	// and beat the .env file.
	Variables []string
	// NoEnvFile leaves the .env file beside the Driftfile unread.
	NoEnvFile bool
}

// readPlan is every plan's way in, for `drift plan` and for PlanVerdict: it reads
// the Driftfile in the working directory, sets the variables it reads, refreshes
// the schema, compiles the file and reads the live slice. It returns the plan's
// state and the Driftfile's bytes, which a plan file records.
//
// The variables it set are reported to diag and the environment it plans to out.
// Two notices come from further down and go to stderr whatever out and diag are:
// the compiler's warnings about a queue no block declares, and the wait for a
// slice that is warming up.
//
// It leaves the process's active slice set to the file's, which is how every read
// names it; PlanVerdict puts the caller's slice back.
func readPlan(o PlanOptions, out, diag io.Writer) (*planState, []byte, error) {
	path, err := requireDriftfile()
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	sum, err := applyVariableSources(filepath.Dir(path), o.Variables, !o.NoEnvFile, o.Environment)
	if err != nil {
		return nil, nil, err
	}
	sum.reportTo(diag)

	common.RefreshDriftfileSchema()
	if !SchemaAvailable() {
		return nil, nil, ErrNoSchema
	}
	c, err := CompileDriftfileEnv(path, o.Environment)
	if err != nil {
		return nil, nil, err
	}
	if o.Environment != "" {
		fmt.Fprintf(out, "  %s environment %s → slice %s\n", common.Hint("·"), o.Environment, common.Highlight(c.Manifest.Name()))
	}

	// Every read below names this file's slice. The ones that resolve a slice
	// from the session read the active one, so for this process the active slice
	// is the file's. Nothing is written to the session.
	if err := os.Setenv(common.SliceEnv, c.Manifest.Name()); err != nil {
		return nil, nil, err
	}

	p, err := gatherPlan(c, false)
	if err != nil {
		return nil, nil, err
	}
	return p, data, nil
}

type liveAPIKey struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

// planState is everything a plan compares: the compiled file, and each live
// fact with the error that read it. A fact whose error is set is unread: the diff
// records it as such and does not show it as absent or as agreeing.
type planState struct {
	c    *Compiled
	user string

	live *LiveSlice // nil: the slice does not exist

	deployed    map[string]string // deployed key → digest; present = deployed
	deployedErr error
	local       map[string]string // booking key → the digest this file would deploy
	buildErr    error

	sites       []liveCanvasSite
	sitesErr    error
	secrets     []string
	secretsErr  error
	keys        map[string][]liveAPIKey // booking key → live keys
	keysErr     error
	gate        siteAuthConfig
	gateErr     error
	links       []string
	linksErr    error
	allow       []string
	allowErr    error
	hosts       []frontedHost
	hostsErr    error
	egress      liveEgressView
	egressErr   error
	domains     []liveDomain
	domainsErr  error
	alerts      []liveAlert
	alertsErr   error
	channels    []string // the channels the slice's clients may publish to
	channelsErr error

	priceNew, priceOld       int
	priceNewErr, priceOldErr error

	// free is the account's free slices, read only when a slice that does not
	// exist yet fits the free grant (freeErr says it could not be read).
	// declinedFree is the person having chosen a priced slice for a shape that
	// could have been free.
	free         FreeSlots
	freeErr      error
	declinedFree bool
}

// gatherPlan reads the live slice. Every read is a GET but the price quote.
//
// The file does not say how the slice is paid for; this decides, and Use records
// it on c. An existing slice stays the tier it is. A new slice that fits the free
// grant is a free slice unless the person chose to pay (declinedFree): when the
// account has no free slot left it is still planned as free, and the apply
// refuses with what the account holds, because a refusal must name the free
// slices and not quietly turn into a price.
func gatherPlan(c *Compiled, declinedFree bool) (*planState, error) {
	m := c.Manifest
	p := &planState{c: c, user: common.GetUsername(), declinedFree: declinedFree}

	// The build reads the source beside the file: which language each backend
	// is, where each handler lives. A failure here is what an apply would hit,
	// so it is reported, and the code of each slot is then unread.
	specs := AtomicSpecs(m)
	els, berr := atomic_cmd.BuildBackends(specs)
	if errors.Is(berr, atomic_cmd.ErrWorkerLanguage) || errors.Is(berr, atomic_cmd.ErrConcurrency) {
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
			digest, derr := atomic_cmd.BackendDigest(el.Dir, el.Name)
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
	if live != nil {
		c.Use(liveIsFree(live))
	} else {
		if c.FitsFree && !declinedFree {
			p.free, p.freeErr = fetchFreeSlots()
		}
		c.Use(!declinedFree)
	}

	newPrice := func() {
		if c.Free {
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
	p.alerts, p.alertsErr = fetchLiveAlerts()
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
	p.channels, p.channelsErr = fetchClientChannels(m)

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

// fetchAPIKeys reads the live keys of every Function the file gates with one.
//
// The slice stores a key under the path `drift atomic auth set` was given,
// and its gate accepts two spellings: `<backend>/<route>` and the bare route.
// Its list matches the stored path exactly, so both are asked.
func fetchAPIKeys(c *Compiled) (map[string][]liveAPIKey, error) {
	backendOf := map[string]string{}
	for _, p := range FunctionEntries(c.Manifest.Slice()) {
		backendOf[p.Str("name")] = p.Str("backend")
	}
	out := map[string][]liveAPIKey{}
	for _, w := range c.WriteOnly {
		if w.Kind != "api_key" {
			continue
		}
		key := bookingKeyOf(c, w.Address)
		_, route, _ := strings.Cut(key, ":")
		paths := []string{route}
		if el := backendOf[key]; el != "" {
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
	// unreadFacts is every fact the diff met that is unread (see VerdictUnread),
	// in the order it met them. The verdict is built from it and from the marked
	// lines.
	unreadFacts []unreadFact
	// attention is what a person has to do before the slice is the file, though
	// the plan has no difference to show for it: one entry, "address  what to
	// do", for each line recorded by attend, in the order the diff met them.
	attention []string
}

func (d *planDiff) add(mark, address, format string, args ...any) {
	d.lines = append(d.lines, planLine{mark, address, fmt.Sprintf(format, args...)})
	if mark != " " {
		d.changed = true
	}
}

// unreadFact is a fact that is unread: the plan line it belongs to, what kind of
// fact it is ("sites", "secrets", "code"), and why it could not be read.
type unreadFact struct {
	address, what string
	err           error
}

// unread adds the blank-marked line for a live fact that is unread and records
// it. The line sets no mark: a fact the plan never saw is no difference, and is
// no match either.
func (d *planDiff) unread(address, what string, err error) {
	d.add(" ", address, "live %s unknown (%v)", what, err)
	d.noteUnread(address, what, err)
}

// noteUnread records an unread fact, for a line that says so in its own words or
// for none. It is the one place a fact enters unreadFacts.
func (d *planDiff) noteUnread(address, what string, err error) {
	d.unreadFacts = append(d.unreadFacts, unreadFact{address, what, err})
}

// attend adds the blank-marked line for something only a person can do, and
// records it as the verdict's attention. The line sets no mark: applying has
// nothing to change there. It is the one place an entry enters attention, and the
// entry is the line as the plan prints it.
func (d *planDiff) attend(address, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	d.add(" ", address, "%s", detail)
	d.attention = append(d.attention, address+"  "+detail)
}

// addressOf renders a booking key the way the file addresses it, for a slot
// the file names and for one only the live slice has.
func addressOf(c *Compiled, key string) string {
	if a, ok := c.Addresses[key]; ok {
		return a
	}
	method, route, _ := strings.Cut(key, ":")
	switch method {
	case "worker":
		return "worker " + route
	case "queue":
		return "function QUEUE " + route
	}
	return "function " + strings.ToUpper(method) + " /" + route
}

// diff compares the compiled file with the live slice, one line for each thing
// the plan compares. A line is marked + for what applying adds, ~ for what it
// changes, - for what it removes and ! for what it refuses, and is blank when the
// two agree or when the plan could not read the live fact. An unread fact gets a
// blank line and is recorded on the diff, so it is neither shown as a difference
// nor counted as agreeing; the verdict is built from the diff.
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
				d.noteUnread(addr, "code", p.deployedErr)
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
			code, codeChanged := p.codeStatus(d, addr, key, deployed, deployedDigest)
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
	p.diffAlerts(d, creating)
	p.diffEdge(d, creating)
	p.diffClientChannels(d, creating)

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
		liveTier, wantTier := "priced", "priced"
		if liveIsFree(p.live) {
			liveTier = "free"
		}
		if c.Free {
			wantTier = "free"
		}
		if liveTier != wantTier {
			// A live free slice whose file no longer fits the free grant: that
			// is a move off the free tier, which apply does not make yet.
			d.billingChanged = true
			d.add("~", "tier", "%s → %s", liveTier, wantTier)
		}
		for _, ch := range diffLabels(c.Shape.Labels, live.Labels) {
			d.add("~", "label "+ch.Key, "%s", describeLabelChange(ch))
		}
	}
	return d
}

// errBuildCannotRead is why a slot's code is not compared when the build gave no
// digest for it and no error of its own.
var errBuildCannotRead = errors.New("the build cannot read it here")

// codeStatus says whether a slot's code and declaration match what is
// deployed, by the same digest the deploy skips unchanged work with. A slot
// whose code it could not compare is recorded on d as unread.
func (p *planState) codeStatus(d *planDiff, addr, key string, deployed bool, digest string) (string, bool) {
	local, known := p.local[key]
	switch {
	case p.deployedErr != nil:
		d.noteUnread(addr, "code", p.deployedErr)
		return fmt.Sprintf("code not compared (%v)", p.deployedErr), false
	case !deployed:
		return "not deployed yet: deploys", true
	case p.buildErr != nil || !known || local == "":
		reason := errBuildCannotRead
		if p.buildErr != nil {
			reason = fmt.Errorf("the build cannot read this project: %w", p.buildErr)
		}
		d.noteUnread(addr, "code", reason)
		return fmt.Sprintf("code not compared (%v)", errBuildCannotRead), false
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

// deployedKeyOf is the key GET /ops/atomic reports a slot under, which is
// the booking key for everything but a queue Function.
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
			d.unread(addr, "sites", p.sitesErr)
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
	recorded := len(d.unreadFacts)
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
			d.unread(w.Address, "secrets", p.secretsErr)
		default:
			// A secret's value is never read back, so it is set again.
			d.add(" ", w.Address, "present; set again from $%s on apply (write-only, not compared)", w.Env)
		}
	}
	// The slice's list is also how a secret the file does not name is found, so a
	// plan that could not read it cannot say that none would be deleted, whether
	// or not the file declares a secret to hang a line on.
	if !creating && p.secretsErr != nil && len(d.unreadFacts) == recorded {
		d.noteUnread("secrets", "secrets", p.secretsErr)
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
			d.unread(addr, "key", p.keysErr)
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
		realm, paths, entries, err := declaredSiteAuth(m)
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
				d.unread("site_auth", "gate", p.gateErr)
			case p.gate.Realm != realm:
				d.add("~", "site_auth", "realm %s → %s", realmOrNone(p.gate.Realm), realmOrNone(realm))
			default:
				d.add(" ", "site_auth", "on (realm %s)", realmOrNone(realm))
			}
			// What the gate covers is its own line, so a change of paths shows beside
			// the gate and not folded into its realm.
			switch livePaths := describeSitePaths(p.gate.Paths); {
			case creating || (p.gateErr == nil && !p.gate.Enabled):
				d.add("+", "site_auth paths", "%s", describeSitePaths(paths))
			case p.gateErr != nil:
			case !samePaths(p.gate.Paths, paths):
				d.add("~", "site_auth paths", "%s → %s", livePaths, describeSitePaths(paths))
			default:
				d.add(" ", "site_auth paths", "%s", livePaths)
			}
			for _, e := range entries {
				addr := "site_auth " + e.User
				switch {
				case !envSet(e.Env):
					d.add("!", addr, "$%s is not set here: apply would refuse", e.Env)
				case !creating && p.gateErr != nil:
					// The gate's own line says it could not be read. A user is not
					// absent from a gate nobody saw, so none is added to it.
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
			d.unread("egress", "egress", p.egressErr)
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
			d.unread(noun, noun, herr)
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
	p.diffDomains(d, creating)
}

// diffDomains says, for each host the file declares, where it stands on the
// slice. A host the slice lacks is added. A host it holds is not changed by
// apply, whatever it says: apply attaches what is missing and the platform's add
// has no update. So the line carries no mark, and a host that is not live yet is
// recorded as attention.
func (p *planState) diffDomains(d *planDiff, creating bool) {
	declared := declaredDomains(p.c.Manifest)
	if len(declared) == 0 {
		return
	}
	if !creating && p.domainsErr != nil {
		d.unread("domain", "domain", p.domainsErr)
		return
	}
	held := map[string]liveDomain{}
	for _, l := range p.domains {
		held[strings.ToLower(strings.TrimSpace(l.Host))] = l
	}
	for _, w := range declared {
		address := "domain " + w.Host
		l, attached := held[w.Host]
		if creating || !attached {
			d.add("+", address, "added")
			continue
		}
		state, needsYou := domainState(l, w.Host)
		if needsYou {
			d.attend(address, "%s", state)
		} else {
			d.add(" ", address, "%s", state)
		}
	}
}

// domainState is what the plan says of a domain the slice holds, and whether a
// person has to act on it. Only a live domain serves traffic: every other state
// is one the person moves with `drift slice domain verify`, and a state this CLI
// does not know is not known to be live.
func domainState(l liveDomain, host string) (string, bool) {
	verify := "drift slice domain verify " + host
	switch l.Status {
	case "live":
		return "live", false
	case "verified":
		return "verified, not live yet: run " + verify, true
	case "pending":
		return "not verified: set the DNS record the add printed, then run " + verify, true
	case "failed":
		reason := l.StatusDetail
		if reason == "" {
			reason = "the platform gave no reason"
		}
		return "failed: " + reason + "; once the record is right, run " + verify, true
	}
	return fmt.Sprintf("status %q is not one this CLI knows: drift slice domain list shows what the platform says", l.Status), true
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

// render prints the plan, then how to apply it, and returns the verdict it
// printed. A plan with nothing to apply ends on what the verdict says: that the
// slice is the file, or that nothing differs in what could be read, with each fact
// that could not be.
func (p *planState) render(out io.Writer) Verdict {
	d := p.renderBody(out)
	v := p.verdict(d)
	switch v.State {
	case VerdictMatch:
		fmt.Fprintf(out, "  Nothing to change.\n\n")
		return v
	case VerdictUnread:
		fmt.Fprintf(out, "  Nothing differs in what could be read.\n")
		for _, line := range v.Unread {
			fmt.Fprintf(out, "    %s\n", line)
		}
		fmt.Fprintln(out)
		return v
	}
	hint := "drift apply"
	flags := p.approvalFlags(d)
	fmt.Fprintf(out, "  Apply with:  %-16s (asks for each of the above)\n", hint)
	if len(flags) > 0 {
		fmt.Fprintf(out, "         or:   %s %s\n", hint, strings.Join(flags, " "))
	}
	fmt.Fprintln(out)
	return v
}

// approvalFlags are the flags that answer every question this plan raises,
// for a run with nobody to ask.
func (p *planState) approvalFlags(d *planDiff) []string {
	var flags []string
	if p.choosesFree() && !p.freeFull() {
		flags = append(flags, "--use-free-slice")
	}
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
	if p.c.Free {
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

	fmt.Fprintf(out, "\n  %-50s %s\n\n", "slice "+who, p.verdict(d).State.header())

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
	if line := p.freeLine(); line != "" {
		fmt.Fprintf(out, "  Free      %s\n", line)
	}
	fmt.Fprintf(out, "  Restart   %s\n", p.restartLine())
	if len(d.removes) == 0 {
		fmt.Fprintf(out, "  Removes   nothing\n")
	} else {
		fmt.Fprintf(out, "  Removes   %s\n", strings.Join(d.removes, "\n            "))
	}
	if len(d.dataLoss) > 0 {
		fmt.Fprintf(out, "  Data      could be lost: %s\n", strings.Join(d.dataLoss, "\n                           "))
	}
	if len(d.attention) > 0 {
		fmt.Fprintf(out, "  Needs you %s\n", strings.Join(d.attention, "\n            "))
	}
	fmt.Fprintln(out)
	return d
}

// choosesFree is whether applying this plan creates a free slice: a slice that
// does not exist yet, whose shape fits the free grant, and that the person has
// not chosen to pay for.
func (p *planState) choosesFree() bool { return p.live == nil && p.c.Free }

// freeFull is whether the account holds every free slice it may, so a free
// slice cannot be created.
func (p *planState) freeFull() bool { return p.choosesFree() && p.freeErr == nil && p.free.Full() }

// freeLine says, for a slice that does not exist yet, whether it is one of the
// account's free slices and why. It is empty for a slice that exists: its tier
// is what it is.
func (p *planState) freeLine() string {
	if p.live != nil {
		return ""
	}
	switch {
	case !p.c.FitsFree:
		return fmt.Sprintf("no, so it is priced: %v", p.c.FreeMisfit)
	case p.declinedFree:
		return "not used: it fits the free slice, and you chose to pay for it"
	case p.freeErr != nil:
		return fmt.Sprintf("it fits the free slice; the platform could not say how many free slices you hold (%v)", p.freeErr)
	case p.free.Full():
		names := make([]string, 0, len(p.free.Held))
		for _, s := range p.free.Held {
			names = append(names, s.Name)
		}
		return fmt.Sprintf("it fits the free slice, and you hold all %d (%s): apply refuses until one is emptied", p.free.Limit, strings.Join(names, ", "))
	}
	return fmt.Sprintf("it fits the free slice: this would be your %s of %d", ordinal(len(p.free.Held)+1), p.free.Limit)
}

func (p *planState) priceLine() string {
	euro := func(cents int) string { return fmt.Sprintf("€%d.%02d", cents/100, cents%100) }
	newLabel := ""
	switch {
	case p.c.Free:
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
