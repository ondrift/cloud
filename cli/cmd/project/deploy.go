package project

// deploy.go is the deploy pipeline `drift apply` runs once the slice holds the
// Driftfile's shape: it checks the slice is ready, then applies Backbone, then
// Atomic and Canvas together, then edge and labels, every declared resource via
// the api gateway.
//
// It does NOT create, grow or price a slice. apply.go does that, from the
// shape the Driftfile compiles to, before it deploys through here.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// driftfileName is the canonical filename. Per the spec, the CLI
// looks for `./Driftfile` and nowhere else.
const driftfileName = "Driftfile"

// atomicForce, when set by `drift apply --force`, makes applyAtomic redeploy
// every slot regardless of whether its source digest matches what is already
// deployed. Read in applyAtomic, which runs concurrently under
// applySliceInOrder and can't easily take extra parameters.
var atomicForce bool

// preflightManifest is everything that can refuse a file before the network is
// asked, returned with the build it read so the caller can hold the bookings to
// the live slice.
func preflightManifest(m *Manifest) ([]atomic_cmd.Backend, error, error) {
	// Route collisions, before the slice diff and the cost-confirm, so a path
	// clash fails at once rather than mid-deploy.
	if err := checkRouteCollisions(m); err != nil {
		return nil, nil, err
	}

	// Two sites landing on one slug overwrite each other in the slice's layout,
	// and the survivor is whichever uploaded last.
	if _, err := canvasSites(m); err != nil {
		return nil, nil, err
	}

	// The build, so the caller can hold each booking to the floor its LANGUAGE
	// needs (CheckSlotBookings), which the Driftfile cannot say.
	//
	// A build failure is not reported here: applyAtomic surfaces a missing
	// handler or a mixed-language backend with the message built for it. A
	// Worker written in anything but Go, and a concurrency the Function cannot
	// have, ARE: BuildBackends refuses each before anything is built, and that
	// refusal is the message.
	els, berr := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if errors.Is(berr, atomic_cmd.ErrWorkerLanguage) || errors.Is(berr, atomic_cmd.ErrConcurrency) {
		return nil, nil, berr
	}

	// The `edge` keys: one this CLI cannot apply refuses the file here, before
	// anything reaches the slice, rather than after the rest of it is live.
	for _, preflight := range []func(*Manifest) error{
		preflightEdgeSiteAuth,
		preflightEdgeLinks,
		preflightEdgeAllow,
		preflightEdgeHosts,
		preflightClientChannels,
	} {
		if err := preflight(m); err != nil {
			return nil, nil, err
		}
	}
	return els, berr, nil
}

// deployManifest applies a compiled Driftfile into its slice, which exists and
// holds the file's shape. The shape is the caller's; this deploys what runs on
// it.
func deployManifest(m *Manifest, out io.Writer) error {
	els, berr, err := preflightManifest(m)
	if err != nil {
		return err
	}
	liveSlice, err := FetchLiveSlice(m.Name())
	if err != nil {
		return err
	}
	if liveSlice == nil {
		return fmt.Errorf("slice %q does not exist after it was created", m.Name())
	}
	if berr == nil {
		if err := CheckSlotBookings(m, els, liveSlice); err != nil {
			return err
		}
	}
	// The slice was just brought to the file's shape, so the reconcile
	// re-points nothing, and it refuses rather than asks.
	return deployChecked(m, liveSlice, false, out)
}

// deployChecked deploys a checked manifest into its slice: the reference
// reconcile, Backbone, Atomic and Canvas, edge and labels, then the report of the
// slots the slice declares and the file does not name.
func deployChecked(m *Manifest, liveSlice *LiveSlice, reconcileYes bool, out io.Writer) error {
	// Does the slice actually hold what the manifest names? A write to an
	// undeclared collection or bucket is refused by the slice with a 400, at
	// runtime and after the upload, and a function the config does not name
	// silently draws on the shared pool. Both become a refusal here instead.
	//
	// RECONCILE, not check: this is the applying path, and it is the only one
	// allowed to rename an idle slot to make room. It reconciles IDENTITY and
	// never CAPACITY: re-pointing a slot the slice already holds is shown and
	// confirmed; needing a slot it does not hold is a refusal naming `drift
	// slice resize`, with no prompt that could be answered into a bigger bill.
	if err := ReconcileSliceReferences(m, liveSlice, reconcileYes); err != nil {
		return err
	}

	// The slice exists and holds what the manifest names, so this process acts on
	// it, BEFORE anything that resolves a slice: the deployed-function read below
	// sends it as `X-Slice`. Nothing is stored; the next command names its own.
	if err := os.Setenv(common.SliceEnv, m.Name()); err != nil {
		return fmt.Errorf("target slice %s: %w", m.Name(), err)
	}

	// What the slice serves, which the idle-slot report at the end reads. A
	// function the file no longer declares is not reported here: the plan named
	// it as a removal, and the resize that follows deletes it. A fetch failure
	// is STATED rather than swallowed: silence would let that report read as a
	// clean slice.
	deployedFns, derr := atomic_cmd.DeployedFunctions()
	if derr != nil {
		fmt.Fprintf(out, "  %s couldn't read which functions are deployed, so the idle-slot report below is incomplete: %v\n",
			common.Hint("·"), derr)
	}

	// A slice can be mid-provision or mid-Recreate for reasons this command did
	// not cause. Without this, Backbone, Atomic and Canvas fire at a runner that
	// is still coming up, and every one fails with "runner unreachable". On a
	// healthy slice it returns on the first poll.
	fmt.Fprintln(out, "  Checking the slice is ready...")
	if err := waitForSliceReady(m.Name()); err != nil {
		return fmt.Errorf("slice %q is not ready to deploy into: %w", m.Name(), err)
	}

	start := time.Now()
	fmt.Fprintf(out, "\n  Deploying %s...\n\n", common.Highlight(m.Name()))

	// Backbone first and alone, then Atomic and Canvas together. See
	// applySliceInOrder for why the order is what it is.
	if err := applySliceInOrder(m); err != nil {
		return err
	}
	if err := applyDomains(m); err != nil {
		return err
	}
	// Alerts are not reconciled here: `drift apply` plans them, asks about the
	// ones the file no longer names, and sets them once the functions they watch
	// are deployed (see applyAlerts).
	if err := applyEgress(m); err != nil {
		return err
	}
	// The rest of `edge`, one reconciler per key; the rate limit is part of the
	// shape and was applied with the resize. Links come before hosts: a fronted
	// host names a slice this one must already link.
	for _, apply := range []func(*Manifest) error{
		applyEdgeSiteAuth,
		applyEdgeLinks,
		applyEdgeAllow,
		applyEdgeHosts,
	} {
		if err := apply(m); err != nil {
			return err
		}
	}
	// The tenant's own labels. They change no limit and no price, which is the
	// only reason a deploy may touch them at all; see labels.go.
	if err := applyLabels(m, liveSlice, out); err != nil {
		return err
	}

	fmt.Fprintf(out, "  %s\n", common.Hint(fmt.Sprintf("Done in %.1fs!", time.Since(start).Seconds())))

	// What the slice declares, and is billed for, that this file does not
	// name: a report, never a question (idle.go). The slice is read AGAIN: the
	// reconcile may have re-pointed idle slots, and the earlier copy would
	// report a slot under the name it no longer has.
	if after, ferr := FetchLiveSlice(m.Name()); ferr != nil {
		fmt.Fprintf(out, "  %s couldn't check for slots the slice declares and the Driftfile does not name: %v\n",
			common.Hint("·"), ferr)
	} else {
		ReportIdleDeclarations(m, after, deployedFns, out)
	}
	return nil
}

// runHooks runs one Driftfile lifecycle phase: each command in order, via the
// shell, from the project root, streaming output. A non-zero exit aborts with
// the failing command surfaced. An empty list is a no-op. Commands are the
// user's own (same trust as a Makefile or package.json script) and run on the
// user's machine, not in a slice — the one-subprocess sandbox rule is a slice
// runtime constraint and does not apply here.
func runHooks(phase string, cmds []string, dir string) error {
	if len(cmds) == 0 {
		return nil
	}
	fmt.Printf("\n  %s %s\n", common.Hint("hooks ·"), phase)
	for _, c := range cmds {
		fmt.Printf("    %s %s\n", common.Hint("$"), c)
		h := exec.Command("sh", "-c", c) // #nosec G204 — the user's own Driftfile, run on the user's machine
		h.Dir = dir
		h.Stdout = os.Stdout
		h.Stderr = os.Stderr
		h.Stdin = os.Stdin
		if err := h.Run(); err != nil {
			return fmt.Errorf("%s hook failed (%s): %w", phase, c, err)
		}
	}
	return nil
}

// waitForSliceReady polls /ops/slice/status until all components
// report Ready, or until 60s elapses. Returns the last error if any.
func waitForSliceReady(name string) error { return common.WaitForSliceReady(name) }

// ─── Atomic ─────────────────────────────────────────────────────────

// checkRouteCollisions rejects a manifest in which two functions share a deploy
// identity. Two entries with the same name would shadow each other on the
// slice: the last one deployed wins, and the survivor answers for both. It is
// surfaced here, up front and offline, instead of becoming a quiet mis-route in
// production.
//
// It reads the manifest and nothing else, so it costs no filesystem access and
// runs before anything is built.
func checkRouteCollisions(m *Manifest) error {
	return atomic_cmd.CheckCollisions(AtomicSpecs(m))
}

// applyAtomic ships every Function and Worker the Driftfile declares, backend by
// backend.
//
// There is ONE path. Which slots exist, which backend each belongs to and how
// each is guarded all come from the manifest, so the deploy no longer has a
// layout to detect: a backend is a group of declared slots, and its directory
// follows from its name. A Worker is built in its backend beside the Functions,
// with its own entry point.
func applyAtomic(m *Manifest, out io.Writer) error {
	// Publish the Driftfile's declared schedules before anything ships. Every
	// deploy path reads them while building its artifact, and the operator —
	// not the slice — is what registers them, so they pass the scheduled-job
	// envelope on the way in.
	atomic_cmd.SetDeclaredSchedules(declaredSchedules(m))

	specs := AtomicSpecs(m)
	if len(specs) == 0 {
		return nil
	}
	backends, err := atomic_cmd.BuildBackends(specs)
	if err != nil {
		return err // a missing handler, a mixed-language backend — surface loudly
	}
	return applyAtomicBackends(backends, out)
}

// backendUnchanged reports whether every function in el already matches the
// deployed digest (so the whole backend can be skipped — no stage, no build).
//
// PER FUNCTION, against DeployDigest, which is the backend's build digest plus
// that function's own declaration. The comparison was against the bare backend
// digest, so a function whose source was untouched skipped even when its gate,
// its secrets, its env or its `cron:` had changed — and a skip sends nothing at
// all, so the change never reached the operator. The deploy printed
// `(unchanged)` beside it, which was true of the code and false of the function.
func backendUnchanged(el atomic_cmd.Backend, digest string, deployed map[string]string) bool {
	if digest == "" {
		return false
	}
	for _, f := range el.Funcs {
		if deployed[f.DeployKey()] != atomic_cmd.DeployDigest(digest, f.Spec) {
			return false
		}
	}
	return true
}

// applyAtomicBackends deploys the project's Atomic functions Backend by Backend.
// Each Go backend is staged + dependency-resolved once, then every function is
// compiled and shipped; an unchanged backend is skipped wholesale.
func applyAtomicBackends(backends []atomic_cmd.Backend, out io.Writer) error {
	fmt.Fprintf(out, "  %s\n", common.AtomicHeader())

	// (Route collisions are caught pre-network in checkRouteCollisions, run by
	// the deploy command before any reconcile.)

	deployed := map[string]string{}
	if !atomicForce {
		if d, err := atomic_cmd.DeployedDigests(); err == nil {
			deployed = d
		} else {
			fmt.Fprintf(out, "  %s couldn't check which functions and workers are unchanged, so deploying all of them\n", common.Hint("·"))
		}
	}

	deployedCount, skippedCount := 0, 0
	for _, el := range backends {
		// One flat digest per backend — any top-level source change rebuilds all
		// its functions; named-backend subdirs never bleed into a Default digest.
		digest, _ := atomic_cmd.BackendDigest(el.Dir, el.Name)

		// Header the backend only when the layout is non-trivial (>1 backend or
		// a named one) — a lone Default backend shouldn't add visual noise. An
		// unchanged backend is headed too, so every slot line sits under the
		// backend it belongs to.
		if len(backends) > 1 || el.Name != atomic_cmd.DefaultBackendName {
			fmt.Fprintf(out, "  %s\n", common.Hint(fmt.Sprintf("backend %s (%s)", el.Name, el.Lang)))
		}

		if !atomicForce && backendUnchanged(el, digest, deployed) {
			for _, f := range el.Funcs {
				fmt.Fprintf(out, "    %s %s %s\n", common.Check(), f.MethodPath(), common.Hint("(unchanged)"))
				skippedCount++
			}
			continue
		}

		// The slot lines go to `out` like every other line of this section. The
		// section is buffered and printed under its heading once the deploy is
		// done, so a line written anywhere else lands above the heading.
		switch {
		case el.Lang == "go":
			if err := atomic_cmd.DeployGoBackend(el, digest, out); err != nil {
				return fmt.Errorf("atomic deploy failed in backend %q: %w", el.Name, err)
			}
			deployedCount += len(el.Funcs)
		case el.Lang == "python" || el.Lang == "node" || el.Lang == "ruby" || el.Lang == "php":
			if err := atomic_cmd.DeployInterpretedBackend(el, digest, out); err != nil {
				return fmt.Errorf("atomic deploy failed in backend %q: %w", el.Name, err)
			}
			deployedCount += len(el.Funcs)
		case len(el.Funcs) == 1:
			// One function on its own — every language can be built this way.
			if err := atomic_cmd.DeployFunction(el.Funcs[0].Spec, true); err != nil {
				return fmt.Errorf("atomic deploy failed for %q: %w", el.Funcs[0].MethodPath(), err)
			}
			fmt.Fprintf(out, "    %s %s\n", common.Check(), el.Funcs[0].MethodPath())
			deployedCount++
		default:
			return fmt.Errorf("backend %q is %s with %d functions — multi-function %s "+
				"backends aren't built yet; keep one function per folder for %s until it lands",
				el.Name, el.Lang, len(el.Funcs), el.Lang, el.Lang)
		}
	}

	if skippedCount > 0 {
		fmt.Fprintf(out, "    %s\n", common.Hint(fmt.Sprintf("%d deployed, %d unchanged", deployedCount, skippedCount)))
	}
	fmt.Fprintln(out)
	return nil
}

// applySliceInOrder applies what the Driftfile declares into the slice in two
// steps.
//
//  1. Backbone, alone: secrets, cache seeds, NoSQL collections and their seeds,
//     queues, and every SQL database's schema, migrations and seed.
//  2. Atomic and Canvas, concurrently.
//
// # Why Backbone goes first
//
// An Atomic slot runs the moment it is deployed. A Worker is started by the
// slice's supervisor at once, and a Function can be called as soon as it is
// routed, so whatever it reads and writes has to exist by then. Run together,
// a Worker's first query could meet no table, and a seed that only runs into an
// empty database was skipped for good once the Worker had written its first row.
//
// The order is safe because nothing in Backbone depends on Atomic. Every step
// above reads only the Driftfile and the files it names, and talks only to the
// slice's Backbone; a seed is SQL or JSONL the project ships, never the output
// of a function. The thing that does depend on Atomic stays after it: the idle
// report reads what Atomic deployed.
//
// A Backbone failure stops the apply before Atomic, for the same reason: a
// slot deployed after its table or queue failed to apply starts against the
// resource that is missing.
//
// Atomic and Canvas share nothing (each makes its own stateless HTTP calls), so
// they run together and the wall-clock is the slower of the two. The spinner is
// single-line by contract and concurrent phases cannot animate it, so each step
// buffers its own section and prints it whole when it is done, in a stable
// order. Of Atomic and Canvas both are attempted, so every failure is visible,
// and the first in that order is returned with its real error.
func applySliceInOrder(m *Manifest) error {
	var backbone bytes.Buffer
	sp := common.StartSpinner("  ", "Applying Backbone…")
	err := applyBackboneSection(m, &backbone)
	sp.Stop()
	fmt.Print(backbone.String())
	if err != nil {
		return err
	}

	type phase struct {
		fn  func(*Manifest, io.Writer) error
		buf bytes.Buffer
		err error
	}
	phases := []*phase{
		{fn: applyAtomic},
		{fn: applyCanvas},
	}

	sp = common.StartSpinner("  ", "Deploying Atomic & Canvas…")
	var wg sync.WaitGroup
	for _, p := range phases {
		wg.Add(1)
		go func(p *phase) {
			defer wg.Done()
			p.err = p.fn(m, &p.buf)
		}(p)
	}
	wg.Wait()
	sp.Stop()

	var firstErr error
	for _, p := range phases {
		fmt.Print(p.buf.String())
		if p.err != nil && firstErr == nil {
			firstErr = p.err
		}
	}
	return firstErr
}

// ─── Backbone ───────────────────────────────────────────────────────

// applyBackboneSection writes the Backbone section: its heading when the file
// declares anything Backbone holds, then every resource, then the SQL
// databases.
func applyBackboneSection(m *Manifest, out io.Writer) error {
	b := m.Slice().Sub("backbone")
	declared := len(b.List("nosql"))+len(b.List("queues"))+len(b.Sub("cache"))+
		len(b.Sub("secrets"))+len(b.List("sql"))+len(b.Sub("realtime")) > 0
	if declared {
		fmt.Fprintf(out, "  %s\n", common.BackboneHeader())
	}
	if err := applyBackbone(m, out); err != nil {
		return err
	}
	if err := applySQL(m, out); err != nil {
		return err
	}
	if declared {
		fmt.Fprintln(out)
	}
	return nil
}

// applyBackbone applies every Backbone resource but the SQL databases, which
// applySQL owns.
func applyBackbone(m *Manifest, out io.Writer) error {
	b := m.Slice().Sub("backbone")

	for key, e := range b.EntryMap("file", "cache") {
		label := fmt.Sprintf("Cache: %s", key)
		var value string
		var hint string
		if e.Str("file") != "" {
			raw, err := os.ReadFile(m.ResolvePath(e.Str("file"))) // #nosec G304
			if err != nil {
				return fmt.Errorf("cache %q: read file %s: %w", key, e.Str("file"), err)
			}
			value = string(raw)
			hint = fmt.Sprintf("(seeded from %s)", filepath.Base(e.Str("file")))
		} else {
			value = e.Str("value")
		}
		if err := cacheSet(key, value, e.Int("ttl")); err != nil {
			return fmt.Errorf("cache set %q failed: %w", key, err)
		}
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if hint != "" {
			line += " " + common.Hint(hint)
		}
		fmt.Fprintln(out, line)
	}

	for _, c := range b.Entries("slot", "nosql") {
		collection := c.Str("slot")
		label := fmt.Sprintf("NoSQL: %s", collection)
		var ttlSecs int64
		if c.Str("ttl") != "" {
			var err error
			ttlSecs, err = parseTTLSeconds(c.Str("ttl"))
			if err != nil {
				return fmt.Errorf("nosql %q ttl: %w", collection, err)
			}
		}
		unique := c.Strings("unique")
		if err := nosqlInit(collection, ttlSecs, unique); err != nil {
			return fmt.Errorf("nosql init %q failed: %w", collection, err)
		}
		seeded := 0
		if c.Str("seed") != "" {
			n, err := nosqlSeedJSONL(collection, m.ResolvePath(c.Str("seed")), ttlSecs, unique)
			if err != nil {
				return fmt.Errorf("nosql seed %q failed: %w", collection, err)
			}
			seeded = n
		}
		if c.Str("ttl") != "" {
			label += fmt.Sprintf(" (ttl %s)", c.Str("ttl"))
		}
		// Named in the deploy output, because a constraint that is now being
		// enforced on writes is a thing the person deploying should see land.
		if len(unique) > 0 {
			label += fmt.Sprintf(" (unique %s)", strings.Join(unique, ", "))
		}
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if seeded > 0 {
			line += " " + common.Hint(fmt.Sprintf("(seeded %d docs)", seeded))
		}
		fmt.Fprintln(out, line)
	}

	for _, name := range DeclaredQueues(m) {
		label := fmt.Sprintf("Queue: %s", name)
		if err := queueInit(name); err != nil {
			return fmt.Errorf("queue init %q failed: %w", name, err)
		}
		fmt.Fprintf(out, "    %s %s\n", common.Check(), label)
	}

	if len(b.Sub("secrets")) > 0 {
		injected := 0
		for k, v := range b.StrMap("secrets") {
			if err := secretSet(k, v); err != nil {
				return fmt.Errorf("secret set %q failed: %w", k, err)
			}
			injected++
		}
		if injected > 0 {
			fmt.Fprintf(out, "    %s Secrets: %d injected\n", common.Check(), injected)
		}
	}

	return applyClientChannels(m, out)
}

// ─── Canvas ─────────────────────────────────────────────────────────

func applyCanvas(m *Manifest, out io.Writer) error {
	sites := m.Slice().Entries("dir", "canvas", "sites")
	if len(sites) == 0 {
		return nil
	}

	resolved, err := canvasSites(m)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "  %s\n", common.CanvasHeader())
	for _, s := range resolved {
		if err := deployCanvas(m.ResolvePath(s.Dir), s.Slug, s.Route); err != nil {
			return fmt.Errorf("canvas deploy failed for %s: %w", s.Dir, err)
		}
		fmt.Fprintf(out, "    %s %s → %s\n", common.Check(), s.Dir, s.Route)
	}
	fmt.Fprintln(out)
	return nil
}

// canvasSite is one resolved `canvas.sites[]` entry — the directory as the
// document writes it, where it mounts, and the slug the slice stores it under.
type canvasSite struct {
	Dir   string
	Route string
	Slug  string
}

// siteMount reads where one `canvas.sites[]` entry mounts, and the slug the
// slice stores it under: its `path`, which the compiler writes for every `site`
// block not at the root.
//
// An entry with no `path` mounts at the root, which is `site "/"`. An entry
// naming one and leaving it empty is refused, because a mount path nobody can
// read is how every site on a project ends up sharing one slug.
func siteMount(s Node) (route, slug string, err error) {
	// An entry with NO mount key at all is the root, and that is the common case.
	if !s.Has("path") {
		return "/", "default", nil
	}

	route, err = common.CanonicalRoute(s.Str("path"))
	if err != nil {
		return "", "", fmt.Errorf("canvas site %q: path: %w", s.Str("dir"), err)
	}
	slug, err = common.SlugifyRoute(route)
	if err != nil {
		return "", "", fmt.Errorf("canvas site %q: path: %w", s.Str("dir"), err)
	}
	return route, slug, nil
}

// canvasSites resolves every declared site, and refuses a document in which two
// of them land on one slug. Two sites sharing a slug overwrite each other in
// the slice's layout, so the refusal has to come before the first upload rather
// than after the site it replaced is already gone.
func canvasSites(m *Manifest) ([]canvasSite, error) {
	entries := m.Slice().Entries("dir", "canvas", "sites")
	out := make([]canvasSite, 0, len(entries))
	bySlug := map[string][]string{}

	for _, s := range entries {
		route, slug, err := siteMount(s)
		if err != nil {
			return nil, err
		}
		out = append(out, canvasSite{Dir: s.Str("dir"), Route: route, Slug: slug})
		bySlug[slug] = append(bySlug[slug], fmt.Sprintf("%s → %s", s.Str("dir"), route))
	}

	var collisions []string
	for slug, sites := range bySlug {
		if len(sites) > 1 {
			sort.Strings(sites)
			collisions = append(collisions,
				fmt.Sprintf("%q is served by %d sites: %s", slug, len(sites), strings.Join(sites, ", ")))
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return nil, fmt.Errorf("canvas site collision — these sites share a slug and would "+
			"overwrite each other on the slice:\n  - %s", strings.Join(collisions, "\n  - "))
	}
	return out, nil
}

// ─── API gateway calls (resource-application path) ─────────────────

func deployCanvas(dir, slug, route string) error {
	zipData, err := common.ZipFolder(dir)
	if err != nil {
		return fmt.Errorf("zip folder: %w", err)
	}
	q := url.Values{}
	q.Set("site", slug)
	q.Set("route", route)
	resp, err := common.DoRequestWithHeaders(
		http.MethodPost,
		common.APIBaseURL+"/ops/canvas?"+q.Encode(),
		zipData,
		map[string]string{"Content-Type": "application/zip"},
	)
	if err != nil {
		return common.TransportError("deploy canvas site", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "deploy canvas site")
	return err
}

func cacheSet(key, value string, ttl int) error {
	payload, _ := json.Marshal(map[string]any{"key": key, "value": value, "ttl": ttl})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/cache/set", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("seed cache key", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "seed cache key")
	return err
}

// nosqlInit creates the collection if it doesn't already exist. Used to
// be a {_setup: true} sentinel write — replaced with /nosql/ensure so
// no visible artefact lands in the collection. Also cleans up legacy
// sentinel rows left behind by older deploys, so templates can stop
// filtering them out at read time.
//
// ttlSecs is the Driftfile-declared per-collection TTL, or 0 for none.
// ensure is authoritative on TTL: passing 0 CLEARS a previously-set TTL,
// matching how removing `ttl:` from the Driftfile and redeploying should
// behave (the collection reverts to "kept forever", not "stuck at
// whatever TTL was last set").
//
// unique is the Driftfile-declared `unique:` field list, and follows exactly
// the same authoritative rule: an empty list clears a constraint the collection
// previously carried, because a constraint that could only ever be added is one
// no redeploy can remove — a typo would need the collection dropped to undo.
func nosqlInit(collection string, ttlSecs int64, unique []string) error {
	target := fmt.Sprintf("%s/ops/backbone/nosql/ensure?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	if ttlSecs > 0 {
		target += fmt.Sprintf("&ttl=%d", ttlSecs)
	}
	if len(unique) > 0 {
		target += "&unique=" + url.QueryEscape(strings.Join(unique, ","))
	}
	resp, err := common.DoJSONRequest(http.MethodPost, target, nil)
	if err != nil {
		return common.TransportError("initialise NoSQL collection", err)
	}
	defer resp.Body.Close()
	if _, err := common.CheckResponse(resp, "initialise NoSQL collection"); err != nil {
		return err
	}
	return purgeLegacySentinels(collection)
}

// purgeLegacySentinels removes any {_setup: true} rows left in the
// collection by previous deploys (the older nosqlInit wrote one per
// invocation and never cleaned up). Idempotent — a no-op once the
// collection is clean.
func purgeLegacySentinels(collection string) error {
	listURL := fmt.Sprintf("%s/ops/backbone/nosql/list?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	listResp, err := common.DoJSONRequest(http.MethodGet, listURL, nil)
	if err != nil {
		return common.TransportError("list nosql for sentinel sweep", err)
	}
	defer listResp.Body.Close()
	body, err := common.CheckResponse(listResp, "list nosql for sentinel sweep")
	if err != nil {
		return err
	}
	var rows []map[string]any
	if len(body) == 0 || json.Unmarshal(body, &rows) != nil {
		return nil
	}
	for _, row := range rows {
		if setup, _ := row["_setup"].(bool); !setup {
			continue
		}
		key, _ := row["_key"].(string)
		if key == "" {
			continue
		}
		delURL := fmt.Sprintf("%s/ops/backbone/nosql/delete?collection=%s&key=%s",
			common.APIBaseURL, url.QueryEscape(collection), url.QueryEscape(key))
		dResp, derr := common.DoJSONRequest(http.MethodPost, delURL, nil)
		if derr != nil {
			return common.TransportError("purge legacy sentinel", derr)
		}
		dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	}
	return nil
}

// nosqlSeedJSONL drops the named collection and re-seeds it from the
// JSONL file. Drop-then-seed is the right semantic for seed data
// because the JSONL IS the canonical state of that collection — there's
// no notion of "merge with prior". Going forward the platform's write
// path upserts by `_id`, so even within a single seed run repeated
// `_id`s get the right end-state. Apps that want runtime-mutable data
// should use a separate (non-seeded) collection.
//
// `unique` has to be threaded through here as well as into the caller's own
// nosqlInit, and forgetting it is a silent bug rather than a compile error in a
// world where it defaulted: this path DROPS the collection first, which takes
// the marker — and therefore the constraint — with it. A seeded collection
// would lose its uniqueness on every single deploy, and only under seeding.
func nosqlSeedJSONL(collection, path string, ttlSecs int64, unique []string) (int, error) {
	// The whole file is read before the collection is touched, so a line that does
	// not parse stops the apply with nothing written.
	docs, errs := readNoSQLSeed(collection, path)
	if len(errs) > 0 {
		return 0, errs
	}
	// Drop, then ensure, then seed — guarantees the JSONL fully describes
	// the collection's state, with no carry-over from prior deploys.
	dropURL := fmt.Sprintf("%s/ops/backbone/nosql/drop?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	dResp, err := common.DoJSONRequest(http.MethodPost, dropURL, nil)
	if err != nil {
		return 0, common.TransportError("drop seeded collection", err)
	}
	if dResp.StatusCode != http.StatusNoContent && dResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(dResp.Body)
		dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		return 0, fmt.Errorf("drop seeded collection: HTTP %d: %s", dResp.StatusCode, string(body))
	}
	dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	if err := nosqlInit(collection, ttlSecs, unique); err != nil {
		return 0, err
	}
	count := 0
	for _, d := range docs {
		body, _ := json.Marshal(d.Doc)
		resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/write", bytes.NewBuffer(body))
		if err != nil {
			return count, common.TransportError("seed nosql doc", err)
		}
		_, cerr := common.CheckResponse(resp, "seed nosql doc")
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		if cerr != nil {
			return count, cerr
		}
		count++
	}
	return count, nil
}

// seedDoc is one record of a NoSQL seed file: where it stands in the file, the _id
// the platform replaces a document by, and the document as apply writes it.
type seedDoc struct {
	Line int
	ID   string
	// Doc is what is sent to the slice: the record with the collection it is
	// written to. The plan compares the slice's documents with this one, so what is
	// compared is what is written.
	Doc map[string]any
}

// maxSeedIDBytes is the longest _id the platform replaces a document by. A write
// whose _id is longer, or is not a string, is added to the collection as a new
// document, so a seed carrying one would add a copy at every apply.
const maxSeedIDBytes = 255

// readNoSQLSeed reads the JSONL seed file of a collection: the one reader of it,
// for the parse of the Driftfile, for apply and for the plan. Every record that
// cannot be loaded is reported with the file and the line it is on, and the
// records it could read come back beside the errors.
func readNoSQLSeed(collection, path string) ([]seedDoc, ParseErrors) {
	data, err := os.ReadFile(path) // #nosec G304 — manifest-declared path
	if err != nil {
		return nil, ParseErrors{fmt.Sprintf("nosql %q seed: read %s: %v", collection, path, err)}
	}

	var docs []seedDoc
	var errs ParseErrors
	for i, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		at := fmt.Sprintf("nosql %q seed: %s:%d:", collection, path, i+1)
		var doc map[string]any
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			errs = append(errs, fmt.Sprintf("%s invalid JSON: %v", at, err))
			continue
		}
		id, problem := seedID(doc)
		if problem != "" {
			errs = append(errs, at+" "+problem)
			continue
		}
		doc["collection"] = collection
		docs = append(docs, seedDoc{Line: i + 1, ID: id, Doc: doc})
	}
	return docs, errs
}

// seedID is the _id a record is replaced by, or what is wrong with it.
func seedID(doc map[string]any) (id, problem string) {
	raw, ok := doc["_id"]
	switch v := raw.(type) {
	case nil:
		if !ok {
			return "", "missing _id"
		}
		return "", "empty _id"
	case string:
		switch {
		case v == "":
			return "", "empty _id"
		case len(v) > maxSeedIDBytes:
			return "", fmt.Sprintf("_id is %d bytes: the platform replaces a document by its _id only up to %d bytes, "+
				"and adds a copy at every apply beyond that", len(v), maxSeedIDBytes)
		}
		return v, ""
	}
	return "", fmt.Sprintf("_id must be a string, not %s: the platform replaces a document by a string _id, "+
		"and adds a copy at every apply for any other", jsonKind(raw))
}

// jsonKind names what a decoded JSON value is, as a person says it.
func jsonKind(v any) string {
	switch v.(type) {
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return "that"
}

func queueInit(name string) error {
	payload, _ := json.Marshal(map[string]any{"queue": name, "body": map[string]any{"_setup": true}})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/queue/push", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("initialise queue", err)
	}
	defer resp.Body.Close()
	if _, err = common.CheckResponse(resp, "initialise queue"); err != nil {
		return err
	}

	popURL := fmt.Sprintf("%s/ops/backbone/queue/pop?queue=%s", common.APIBaseURL, url.QueryEscape(name))
	popResp, err := common.DoJSONRequest(http.MethodPost, popURL, nil)
	if err == nil {
		popResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	}
	return nil
}

func secretSet(name, value string) error {
	payload, _ := json.Marshal(map[string]string{"name": name, "value": value})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/secret/set", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("store secret", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "store secret")
	return err
}

// ─── URL builder ────────────────────────────────────────────────────

func buildSiteURL() string {
	username := common.GetUsername()
	slice := common.GetActiveSlice()
	if username == "" || slice == "" {
		return ""
	}
	return common.SliceURL(username, slice)
}
