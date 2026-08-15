package project

// run.go is the deploy driver. Reads a Driftfile, optionally fetches
// the live slice + diffs, prompts for cost-confirm, grows the slice
// envelope when needed, then walks atomic → backbone → canvas
// applying every declared resource via the api gateway.
//
// Flags:
//   --plan                Print the diff (resources + envelope + cost),
//                         exit non-zero if oversized, never apply.
//                         Skips file-existence checks for canvas dirs
//                         so it works in CI where canvas isn't mounted.
//   --no-slice-reconcile  Skip the slice diff entirely; deploy code
//                         only. Used as the escape hatch when the
//                         abort path fires and the user wants to
//                         leave the slice alone.
//   --yes                 Auto-confirm the cost prompt. For CI use.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// driftfileName is the canonical filename. Per the spec, the CLI
// looks for `./Driftfile` and nowhere else.
const driftfileName = "Driftfile"

// atomicForce, when set by --force, makes applyAtomic redeploy every function
// regardless of whether its source digest matches what's already deployed.
// Bound directly to the flag in getDeployCmd; read in applyAtomic, which runs
// concurrently under applySliceTriad and can't easily take extra parameters.
var atomicForce bool

func getApplyCmd() *cobra.Command {
	var (
		planOnly        bool
		noReconcile     bool
		autoYes         bool
		billingMonths   int
		envName         string
		secretOverrides []string
		noEnvFile       bool
	)

	cmd := &cobra.Command{
		// `apply` rather than `deploy`: a Driftfile is a destination, not a
		// script, and the verb should say so — you apply the file to an
		// environment. `deploy` still resolves, as a deprecated spelling.
		Use:   "apply [environment]",
		Short: "Apply a Driftfile to a slice (optionally for a named environment)",
		Long: `Deploy every resource declared in the project's Driftfile.

If the Driftfile declares environments, pass one as the positional argument to
deploy that environment's merged shape (its overrides on top of the base);
prod/production deploys under the bare project name, others under <name>-<env>.
With no argument the deploy targets prod/production when declared, or the
single slice otherwise.`,
		Example: "  drift file apply\n  drift file apply staging\n  drift file apply prod --yes\n  drift file apply --plan",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return fmt.Errorf("resolve manifest path: %w", err)
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}
			projectDir := filepath.Dir(manifestPath)

			// Environment selection: a positional argument is the primary
			// selector; the --env flag is a fallback (and the legacy ${ENV}
			// setter for projects that declare no environments).
			positionalEnv := ""
			if len(args) == 1 {
				positionalEnv = args[0]
			}
			selectedEnv := positionalEnv
			if selectedEnv == "" {
				selectedEnv = envName
			}

			// Variable origin hierarchy (highest first): Driftfile-hardcoded
			// literals > terminal environment > --secret/--env overrides >
			// .env.<env> > .env. Applied before parsing so ${VAR}/$ENVREF
			// resolve against it and hook shells inherit ENV. ENV binds to the
			// selected environment.
			overrides := secretOverrides
			if selectedEnv != "" {
				overrides = append([]string{"ENV=" + selectedEnv}, overrides...)
			}
			vars, err := applyVariableSources(projectDir, overrides, !noEnvFile, selectedEnv)
			if err != nil {
				return err
			}
			vars.report()

			// pre_deploy hooks run BEFORE the full parse so a build can produce
			// the artifacts (e.g. a canvas dist dir) the parse then validates.
			// Skipped in --plan (a dry run never builds).
			preHooks, postHooks := ParseHooks(manifestPath)
			if !planOnly {
				if err := runHooks("pre_deploy", preHooks, projectDir); err != nil {
					return err
				}
			}

			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}

			// Merge the selected environment's overrides onto the base slice
			// and derive its slice name. After this, m.Slice is the effective
			// slice and every downstream step is environment-agnostic.
			resolvedEnv, err := m.SelectEnvironment(selectedEnv, positionalEnv != "")
			if err != nil {
				return err
			}
			if resolvedEnv != "" {
				fmt.Printf("  %s environment %s → slice %s\n", common.Hint("·"), resolvedEnv, common.Highlight(m.Name()))
			}

			// Loud, pre-network preflight: reject route collisions before the
			// slice diff / cost-confirm, so a path clash fails immediately
			// rather than mid-deploy after you've already paid the ceremony.
			if err := checkRouteCollisions(m); err != nil {
				return err
			}

			// Same preflight, for a booking the function's LANGUAGE refuses. The
			// api is the enforcing owner and answers 400, but it answers it after
			// the cost-confirm and the upload — so the same verdict is reached
			// here, from the floor the schema publishes.
			//
			// A build failure is not reported here: applyAtomic already surfaces a
			// missing handler or a mixed-language element with the message built
			// for it, and pre-empting that would report the wrong cause.
			if els, berr := atomic_cmd.BuildElements(FunctionSpecs(m)); berr == nil {
				if err := CheckCompiledBookings(m, els); err != nil {
					return err
				}
			}

			// In --plan mode, the slice diff drives output and we
			// never call code-deploy paths. Fetch live + classify
			// + render, and exit with the appropriate status.
			if planOnly {
				return runPlan(m)
			}

			// Reconcile the slice's shape unless explicitly skipped.
			if !noReconcile {
				if err := reconcileSlice(m, autoYes, billingMonths); err != nil {
					return err
				}
			}

			// At this point the slice exists at >= the declared shape.
			// Set it as the active slice for subsequent api calls.
			if err := common.SaveActiveSlice(m.Name()); err != nil {
				return fmt.Errorf("set active slice: %w", err)
			}

			start := time.Now()
			fmt.Printf("\n  Deploying %s...\n\n", common.Highlight(m.Name()))

			// Atomic, Backbone, and Canvas are independent slice subsystems —
			// deploy all three concurrently (wall-clock = slowest, not sum).
			if err := applySliceTriad(m); err != nil {
				return err
			}
			if err := applyDomains(m); err != nil {
				return err
			}
			if err := applyAlerts(m); err != nil {
				return err
			}
			if err := applySQL(m); err != nil {
				return err
			}
			if err := applyEgress(m); err != nil {
				return err
			}

			elapsed := time.Since(start).Seconds()
			fmt.Printf("  %s\n", common.Hint(fmt.Sprintf("Done in %.1fs!", elapsed)))

			// post_deploy hooks run against the now-live slice (typically a
			// smoke test). A failure leaves the slice deployed — it's already
			// live — but returns non-zero so CI and the user see it.
			if err := runHooks("post_deploy", postHooks, projectDir); err != nil {
				fmt.Printf("\n  %s the slice is deployed and live, but a post_deploy hook failed\n", common.Cross())
				return err
			}

			if siteURL := buildSiteURL(); siteURL != "" {
				fmt.Printf("\n  %s  %s\n", common.Check(), siteURL)
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&planOnly, "plan", false, "Print the slice diff and exit; do not deploy")
	cmd.Flags().BoolVar(&noReconcile, "no-slice-reconcile", false, "Skip the slice diff; deploy code into the active slice as-is")
	cmd.Flags().BoolVar(&atomicForce, "force", false, "Redeploy every function even if its source is unchanged")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period for new slices and grow operations")
	cmd.Flags().StringVar(&envName, "env", "", "Environment to deploy (same as the positional argument); also sets ${ENV}")
	cmd.Flags().StringArrayVar(&secretOverrides, "secret", nil, "Override a variable for ${VAR}/$ENVREF resolution: KEY=value (repeatable). Yields to a variable already set in the environment; beats the .env file.")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file sitting next to the Driftfile")
	return cmd
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

// ─── Plan mode ──────────────────────────────────────────────────────

func runPlan(m *Manifest) error {
	d, err := computeDiff(m)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(RenderDiff(d))
	if d.Verdict == VerdictAbort {
		return fmt.Errorf("deploy would abort (slice oversized vs manifest)")
	}
	return nil
}

// ─── Reconcile (used by run, not plan) ──────────────────────────────

func reconcileSlice(m *Manifest, autoYes bool, billingMonths int) error {
	d, err := computeDiff(m)
	if err != nil {
		return err
	}

	switch d.Verdict {
	case VerdictMatch:
		// Nothing to do. Continue silently.
		return nil

	case VerdictAbort:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		return fmt.Errorf("deploy aborted (slice oversized vs manifest)")

	case VerdictCreate:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		if !confirm(autoYes, "Apply?") {
			return fmt.Errorf("aborted by user")
		}
		// Pick the cheapest tier that fits: hacker if zero cost, custom otherwise.
		tier := "custom"
		if d.WantedCostCents == 0 {
			tier = "hacker"
		}
		manifestCfg, err := ManifestToSliceConfig(m)
		if err != nil {
			return err
		}
		fmt.Printf("\n  Creating slice %q...\n", m.Name())
		if err := CreateSlice(m.Name(), tier, manifestCfg, billingMonths); err != nil {
			return err
		}
		// Wait for the slice to provision before we deploy code into it.
		if err := waitForSliceReady(m.Name()); err != nil {
			return fmt.Errorf("slice did not become ready: %w", err)
		}
		return nil

	case VerdictGrow:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		if !confirm(autoYes, "Apply?") {
			return fmt.Errorf("aborted by user")
		}
		manifestCfg, err := ManifestToSliceConfig(m)
		if err != nil {
			return err
		}
		fmt.Printf("\n  Growing slice %q...\n", m.Name())
		if err := ResizeSlice(m.Name(), manifestCfg, billingMonths); err != nil {
			return err
		}
		// Wait before deploying functions into it (#PLATFORM-CORE-OPERATOR-KG3TKF).
		//
		// A resize no longer recreates the pod for its own sake — the platform
		// stopped stamping a fresh pod-template annotation on every apply, and
		// pushes new tier limits into the running slice instead. So the common
		// grows (rate limit, function COUNT, backbone.secrets) leave the runner
		// alone entirely and this returns on its first poll.
		//
		// One grow still replaces the pod: a change to function_memory, which is
		// a term of the container's memory limit and therefore genuinely changes
		// the rendered spec. Without this wait, that case does what it always did
		// — deploys functions into a runner that is still coming up and fails with
		// "the platform is having trouble — runner unreachable", which reads as a
		// platform fault and is really our own ordering.
		// Phrased as a check, not a wait: on the common grow it returns on the
		// first poll and announcing a wait that did not happen is its own small
		// lie.
		fmt.Println("  Checking the slice is ready...")
		if err := waitForSliceReady(m.Name()); err != nil {
			return fmt.Errorf("slice did not become ready after the grow: %w", err)
		}
		return nil
	}

	return fmt.Errorf("unexpected verdict: %s", d.Verdict)
}

// computeDiff is shared by plan and reconcile. It builds the
// manifest's SliceConfig, fetches the live slice (if any), prices
// both, and classifies via Diff().
func computeDiff(m *Manifest) (DiffResult, error) {
	manifestCfg, err := ManifestToSliceConfig(m)
	if err != nil {
		return DiffResult{}, err
	}
	wantedCost, wantedItems, err := PriceConfig(manifestCfg)
	if err != nil {
		return DiffResult{}, fmt.Errorf("price manifest config: %w", err)
	}

	live, err := FetchLiveSlice(m.Name())
	if err != nil {
		return DiffResult{}, fmt.Errorf("fetch live slice: %w", err)
	}

	var (
		liveCfg  *SliceConfig
		liveTier string
		liveCost int
	)
	if live != nil {
		liveCfg = &live.Config
		liveTier = live.Tier
		liveCost = live.MonthlyCostCents
	}

	d := Diff(m.Name(), manifestCfg, liveCfg, liveTier, liveCost, wantedCost)
	d.WantedItems = wantedItems
	return d, nil
}

// confirm prompts the user with [y/N]. autoYes short-circuits the
// prompt — used by CI flags. Returns true if the user accepts.
func confirm(autoYes bool, prompt string) bool {
	if autoYes {
		fmt.Printf("  %s [y/N] (auto-yes)\n", prompt)
		return true
	}
	fmt.Printf("  %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// WaitForSliceReady is waitForSliceReady for callers outside this package —
// `drift slice create --from Driftfile`, which provisions a slice the user is
// about to deploy into. Without it the very next `project deploy` races the
// slice's own provisioning and fails with "runner unreachable", which is
// indistinguishable from a platform fault (see HQ PLATFORM-CORE-OPERATOR-KG3TKF
// for the same race on the grow path). Observed on alpha 2026-07-27.
func WaitForSliceReady(name string) error { return waitForSliceReady(name) }

// waitForSliceReady polls /ops/slice/status until all components
// report Ready, or until 60s elapses. Returns the last error if any.
func waitForSliceReady(name string) error {
	deadline := time.Now().Add(60 * time.Second)
	u := common.APIBaseURL + "/ops/slice/status?name=" + url.QueryEscape(name)
	for time.Now().Before(deadline) {
		resp, err := common.DoJSONRequest(http.MethodGet, u, nil)
		if err == nil {
			body, cerr := common.CheckResponse(resp, "slice status")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if cerr == nil {
				var s struct {
					Ready bool `json:"ready"`
				}
				if jerr := json.Unmarshal(body, &s); jerr == nil && s.Ready {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("slice %q not ready after 60s", name)
}

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
	return atomic_cmd.CheckCollisions(FunctionSpecs(m))
}

// applyAtomic ships every function the Driftfile declares, element by element.
//
// There is ONE path. Which functions exist, which element each belongs to and
// how each is guarded all come from the manifest, so the deploy no longer has a
// layout to detect: an element is a group of declared functions, and its
// directory follows from its name.
func applyAtomic(m *Manifest, out io.Writer) error {
	// Publish the Driftfile's declared schedules before anything ships. Every
	// deploy path reads them while building its artifact, and the operator —
	// not the slice — is what registers them, so they pass the scheduled-job
	// envelope on the way in.
	atomic_cmd.SetDeclaredSchedules(declaredSchedules(m))

	specs := FunctionSpecs(m)
	if len(specs) == 0 {
		return nil
	}
	elements, err := atomic_cmd.BuildElements(specs)
	if err != nil {
		return err // a missing handler, a mixed-language element — surface loudly
	}
	return applyAtomicElements(elements, out)
}

// elementUnchanged reports whether every function in el already matches the
// deployed digest (so the whole element can be skipped — no stage, no build).
func elementUnchanged(el atomic_cmd.Element, digest string, deployed map[string]string) bool {
	if digest == "" {
		return false
	}
	for _, f := range el.Funcs {
		if deployed[f.DeployKey()] != digest {
			return false
		}
	}
	return true
}

// applyAtomicElements deploys the project's Atomic functions Element by Element.
// Each Go element is staged + dependency-resolved once, then every function is
// compiled and shipped; an unchanged element is skipped wholesale.
func applyAtomicElements(elements []atomic_cmd.Element, out io.Writer) error {
	fmt.Fprintf(out, "  %s\n", common.AtomicHeader())

	// (Route collisions are caught pre-network in checkRouteCollisions, run by
	// the deploy command before any reconcile.)

	deployed := map[string]string{}
	if !atomicForce {
		if d, err := atomic_cmd.DeployedDigests(); err == nil {
			deployed = d
		} else {
			fmt.Fprintf(out, "  %s couldn't check which functions are unchanged — deploying all\n", common.Hint("·"))
		}
	}

	deployedCount, skippedCount := 0, 0
	for _, el := range elements {
		// One flat digest per element — any top-level source change rebuilds all
		// its functions; named-element subdirs never bleed into a Default digest.
		digest, _ := atomic_cmd.ElementDigest(el.Dir, el.Name)

		if !atomicForce && elementUnchanged(el, digest, deployed) {
			for _, f := range el.Funcs {
				fmt.Fprintf(out, "    %s %s %s\n", common.Check(), f.MethodPath(), common.Hint("(unchanged)"))
				skippedCount++
			}
			continue
		}

		// Header the element only when the layout is non-trivial (>1 element or
		// a named one) — a lone Default element shouldn't add visual noise.
		if len(elements) > 1 || el.Name != atomic_cmd.DefaultElementName {
			fmt.Fprintf(out, "  %s\n", common.Hint(fmt.Sprintf("element %s (%s)", el.Name, el.Lang)))
		}

		switch {
		case el.Lang == "go":
			if err := atomic_cmd.DeployGoElement(el, digest, false); err != nil {
				return fmt.Errorf("atomic deploy failed in element %q: %w", el.Name, err)
			}
			deployedCount += len(el.Funcs)
		case el.Lang == "python" || el.Lang == "node" || el.Lang == "ruby" || el.Lang == "php":
			if err := atomic_cmd.DeployInterpretedElement(el, digest, false); err != nil {
				return fmt.Errorf("atomic deploy failed in element %q: %w", el.Name, err)
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
			return fmt.Errorf("element %q is %s with %d functions — multi-function %s "+
				"elements aren't built yet; keep one function per folder for %s until it lands",
				el.Name, el.Lang, len(el.Funcs), el.Lang, el.Lang)
		}
	}

	if skippedCount > 0 {
		fmt.Fprintf(out, "    %s\n", common.Hint(fmt.Sprintf("%d deployed, %d unchanged", deployedCount, skippedCount)))
	}
	fmt.Fprintln(out)
	return nil
}

// applySliceTriad deploys the three independent slice subsystems — Atomic
// functions, Backbone data, and Canvas sites — CONCURRENTLY. They touch
// different parts of the slice and share no mutable state (each makes its own
// stateless HTTP calls), so the wall-clock becomes the slowest of the three
// instead of their sum. The spinner is single-line by contract, so concurrent
// phases can't animate it: each phase buffers its own section, one aggregate
// spinner runs while they work, then the sections print in a stable order
// (Atomic → Backbone → Canvas). The first failure in that order is returned
// with its real error; every phase is attempted so all failures are visible.
func applySliceTriad(m *Manifest) error {
	type phase struct {
		fn  func(*Manifest, io.Writer) error
		buf bytes.Buffer
		err error
	}
	phases := []*phase{
		{fn: applyAtomic},
		{fn: applyBackbone},
		{fn: applyCanvas},
	}

	sp := common.StartSpinner("  ", "Deploying Atomic, Backbone & Canvas…")
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

func applyBackbone(m *Manifest, out io.Writer) error {
	b := m.Slice().Sub("backbone")
	if len(b.List("nosql"))+len(b.List("queues"))+len(b.Sub("cache"))+len(b.Sub("secrets")) == 0 {
		return nil
	}

	fmt.Fprintf(out, "  %s\n", common.BackboneHeader())

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

	for _, c := range b.Entries("name", "nosql") {
		label := fmt.Sprintf("NoSQL: %s", c.Str("name"))
		var ttlSecs int64
		if c.Str("ttl") != "" {
			var err error
			ttlSecs, err = parseTTLSeconds(c.Str("ttl"))
			if err != nil {
				return fmt.Errorf("nosql %q ttl: %w", c.Str("name"), err)
			}
		}
		if err := nosqlInit(c.Str("name"), ttlSecs); err != nil {
			return fmt.Errorf("nosql init %q failed: %w", c.Str("name"), err)
		}
		seeded := 0
		if c.Str("seed") != "" {
			n, err := nosqlSeedJSONL(c.Str("name"), m.ResolvePath(c.Str("seed")), ttlSecs)
			if err != nil {
				return fmt.Errorf("nosql seed %q failed: %w", c.Str("name"), err)
			}
			seeded = n
		}
		if c.Str("ttl") != "" {
			label += fmt.Sprintf(" (ttl %s)", c.Str("ttl"))
		}
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if seeded > 0 {
			line += " " + common.Hint(fmt.Sprintf("(seeded %d docs)", seeded))
		}
		fmt.Fprintln(out, line)
	}

	for _, q := range b.Entries("name", "queues") {
		label := fmt.Sprintf("Queue: %s", q.Str("name"))
		if err := queueInit(q.Str("name")); err != nil {
			return fmt.Errorf("queue init %q failed: %w", q.Str("name"), err)
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

	fmt.Fprintln(out)
	return nil
}

// ─── Canvas ─────────────────────────────────────────────────────────

func applyCanvas(m *Manifest, out io.Writer) error {
	sites := m.Slice().Entries("dir", "canvas", "sites")
	if len(sites) == 0 {
		return nil
	}

	fmt.Fprintf(out, "  %s\n", common.CanvasHeader())
	keep := make([]string, 0, len(sites))
	for _, s := range sites {
		dir := m.ResolvePath(s.Str("dir"))
		route := canonicalRoute(s.Str("route"))
		slug := SlugifyRoute(route)
		label := fmt.Sprintf("%s → %s", s.Str("dir"), route)
		if err := deployCanvas(dir, slug, route); err != nil {
			return fmt.Errorf("canvas deploy failed for %s: %w", s.Str("dir"), err)
		}
		fmt.Fprintf(out, "    %s %s\n", common.Check(), label)
		keep = append(keep, slug)
	}
	if err := pruneCanvas(keep); err != nil {
		return fmt.Errorf("canvas prune failed: %w", err)
	}
	fmt.Fprintln(out)
	return nil
}

// canonicalRoute normalises the Driftfile's optional route value. Empty or
// missing means "/", trailing slash is stripped (except the bare "/").
func canonicalRoute(route string) string {
	if route == "" {
		return "/"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	route = strings.TrimRight(route, "/")
	if route == "" {
		return "/"
	}
	return route
}

// SlugifyRoute derives the per-site directory name from a route. The slug
// is what the slice uses to lay sites out under /data/canvas/<slug>/.
//
//	"/"               -> "default"
//	"/reviewer"       -> "reviewer"
//	"/admin/portal"   -> "admin-portal"
func SlugifyRoute(route string) string {
	r := canonicalRoute(route)
	if r == "/" {
		return "default"
	}
	r = strings.TrimPrefix(r, "/")
	return strings.ReplaceAll(r, "/", "-")
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

func pruneCanvas(keep []string) error {
	body, _ := json.Marshal(map[string]any{"keep": keep})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/canvas/prune", bytes.NewBuffer(body))
	if err != nil {
		return common.TransportError("prune canvas sites", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "prune canvas sites")
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
func nosqlInit(collection string, ttlSecs int64) error {
	target := fmt.Sprintf("%s/ops/backbone/nosql/ensure?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	if ttlSecs > 0 {
		target += fmt.Sprintf("&ttl=%d", ttlSecs)
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
func nosqlSeedJSONL(collection, path string, ttlSecs int64) (int, error) {
	data, err := os.ReadFile(path) // #nosec G304 — manifest-declared path, validated at parse
	if err != nil {
		return 0, fmt.Errorf("read seed: %w", err)
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
	if err := nosqlInit(collection, ttlSecs); err != nil {
		return 0, err
	}
	count := 0
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			return count, fmt.Errorf("parse seed line %d: %w", count+1, err)
		}
		doc["collection"] = collection
		body, _ := json.Marshal(doc)
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
	apiURL := common.APIBaseURL
	scheme := "http://"
	if strings.HasPrefix(apiURL, "https://") {
		scheme = "https://"
	}
	host := strings.TrimPrefix(apiURL, scheme)
	host = strings.TrimPrefix(host, "api.")
	// Every slice — including the one named "default" — is reached at
	// <username>-<slice>.<root>. There is no bare <username>.<root> shortcut.
	return scheme + username + "-" + slice + "." + host
}
