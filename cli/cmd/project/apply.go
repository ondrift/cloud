package project

// apply.go — `drift apply`: make the slice what its Driftfile says.
//
// Apply does exactly what `drift plan` shows, and only that. It builds the same
// plan, prints it, asks its questions, and then:
//
//  1. No slice yet: creates it with the file's shape.
//  2. The shape changed: resizes it, sending back the figure the plan quoted
//     and, when the plan removes something, the slice's name. The platform
//     refuses a resize whose answers no longer match, which is what makes the
//     plan the thing that was agreed to.
//  3. Deploys what the file declares: Backbone, then Atomic and Canvas, then
//     edge (deploy.go).
//  4. Sets the API keys the file names, where their fingerprint differs.
//
// # Three questions
//
// Money, removal and data loss are each asked on their own, because agreeing to
// one is not agreeing to another:
//
//   - the price, when the monthly figure moves: --approve-price <cents>, which
//     must be the figure the platform quoted;
//   - each removal: --approve-remove "<address>", once per resource;
//   - each loss of data: --allow-data-loss "<address>", by name, never by a
//     blanket flag.
//
// At a terminal each is asked as a question. Without one, an unanswered
// question stops the apply before anything is sent, and says which flags
// answer it. A plan file (`drift plan --out`) carries the answers of a plan a
// person reviewed, and is refused if the file or the slice moved since.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// GetApplyCmd builds `drift apply`.
func GetApplyCmd() *cobra.Command {
	var (
		vars       []string
		secrets    []string
		noEnvFile  bool
		approve    int
		removes    []string
		dataLoss   []string
		force      bool
		approveSet bool
	)
	cmd := &cobra.Command{
		Use:   "apply [environment | plan.json]",
		Short: "Make the slice what its Driftfile says: create or resize it, then deploy",
		Long: `Make the slice exactly what the Driftfile declares, and nothing else.

Apply prints the same plan 'drift plan' prints, asks before the monthly price
moves, before anything is removed, and before any data could be lost, then
creates or resizes the slice and deploys what the file declares.

Without a terminal each question is answered by a flag: --approve-price with the
quoted figure, --approve-remove and --allow-data-loss with each resource's
address. A plan file written by 'drift plan --out' carries the answers of the
plan it records, and is refused if the Driftfile or the slice changed since.

An environment's name applies the file to that environment's own slice, with
its variables and its billing.`,
		Example: "  drift apply\n" +
			"  drift apply staging\n" +
			"  drift apply --approve-price 768 --approve-remove \"pulse GET /bench\"\n" +
			"  drift plan --out plan.json && drift apply plan.json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			approveSet = cmd.Flags().Changed("approve-price")
			a := applyAnswers{removes: map[string]bool{}, dataLoss: map[string]bool{}, price: -1}
			if approveSet {
				a.price = approve
			}
			for _, r := range removes {
				a.removes[r] = true
			}
			for _, l := range dataLoss {
				a.dataLoss[l] = true
			}
			// The argument is a plan file when it names one, and an
			// environment otherwise.
			planFile, env := "", ""
			if len(args) == 1 {
				if isPlanFile(args[0]) {
					planFile = args[0]
				} else {
					env = args[0]
				}
			}
			return runApply(applyOptions{
				overrides: append(append([]string{}, vars...), secrets...),
				noEnvFile: noEnvFile,
				answers:   a,
				planFile:  planFile,
				env:       env,
				force:     force,
				ask:       term.IsTerminal(int(os.Stdin.Fd())),
				out:       os.Stdout,
			})
		},
	}
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Set a variable env() reads: KEY=value (repeatable). Yields to the terminal's environment; beats the .env file.")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "The same as --var.")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file beside the Driftfile")
	cmd.Flags().IntVar(&approve, "approve-price", 0, "Agree to the new monthly price, in cents: it must be the figure the plan quoted")
	cmd.Flags().StringArrayVar(&removes, "approve-remove", nil, "Agree to remove a resource, by its address as the plan prints it (repeatable)")
	cmd.Flags().StringArrayVar(&dataLoss, "allow-data-loss", nil, "Agree that a resource's data may be lost, by its address (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "Redeploy every pulse and field even if its source is unchanged")
	return cmd
}

// applyAnswers are the answers given before the apply asks anything.
type applyAnswers struct {
	price    int // -1: none given
	removes  map[string]bool
	dataLoss map[string]bool
}

type applyOptions struct {
	overrides []string
	noEnvFile bool
	answers   applyAnswers
	planFile  string
	// env is the environment to apply, or "" for the default slice. A plan
	// file carries its own.
	env   string
	force bool
	// ask is whether there is someone at a terminal to put a question to.
	ask bool
	in  io.Reader
	out io.Writer
}

// deployInto applies a compiled manifest into a slice that exists and holds
// its shape. A variable so a test can watch what apply hands the pipeline
// without building and shipping the fixture's code.
var deployInto = deployManifest

// runApply is `drift apply`.
func runApply(o applyOptions) error {
	if o.in == nil {
		o.in = os.Stdin
	}
	path, err := requireDriftfile()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dir := filepath.Dir(path)

	if o.planFile != "" {
		rec, err := readPlanRecord(o.planFile)
		if err != nil {
			return err
		}
		o.env = rec.Environment
	}

	sum, err := applyVariableSources(dir, o.overrides, !o.noEnvFile, o.env)
	if err != nil {
		return err
	}
	sum.report()

	// pre_deploy hooks build what the file then names, so they run before the
	// compile checks those paths exist.
	pre, post, err := ParseHooks(path)
	if err != nil {
		return err
	}
	if err := runHooks("pre_deploy", pre, dir); err != nil {
		return err
	}

	common.RefreshDriftfileSchema()
	if !SchemaAvailable() {
		return ErrNoSchema
	}
	c, err := CompileDriftfileEnv(path, o.env)
	if err != nil {
		return err
	}
	name := c.Manifest.Name()
	if o.env != "" {
		fmt.Fprintf(o.out, "  %s environment %s → slice %s\n", common.Hint("·"), o.env, common.Highlight(name))
	}

	// Everything that can refuse the file without the network, before the
	// plan reads the slice: route collisions, two sites on one slug, and an
	// edge key this CLI cannot apply. A refusal here is reached before
	// anything is created, resized or deployed.
	if _, _, err := preflightManifest(c.Manifest); err != nil {
		return err
	}

	if err := os.Setenv(common.SliceEnv, name); err != nil {
		return err
	}

	p, err := gatherPlan(c)
	if err != nil {
		return err
	}
	d := p.renderBody(o.out)

	if o.planFile != "" {
		if err := checkPlanFile(o.planFile, data, p, d, &o.answers); err != nil {
			return err
		}
	}

	if p.buildErr != nil {
		return fmt.Errorf("the build cannot read this project, so nothing was applied: %w", p.buildErr)
	}
	if d.billingChanged {
		return fmt.Errorf("the file moves slice %q between free and monthly billing, which `drift apply` does not do yet; "+
			"nothing was applied", name)
	}
	if p.priceAsked() && p.priceNewErr != nil {
		return fmt.Errorf("the platform could not quote the new shape, so its price cannot be agreed to: %w", p.priceNewErr)
	}
	if err := writeOnlyValuesSet(c); err != nil {
		return err
	}
	if err := o.settle(p, d); err != nil {
		return err
	}

	// 1 and 2: the shape.
	switch {
	case p.live == nil:
		fmt.Fprintf(o.out, "  Creating slice %s...\n", common.Highlight(name))
		if err := slice.CreateWithShape(name, c.Shape, c.Billing == "free"); err != nil {
			return err
		}
		if c.Billing == "free" {
			// The free preset ships placeholder names; the file's own replace
			// them. Nothing is deployed in a placeholder, and the person asked
			// for this slice a moment ago, so the removal is the create's own.
			if err := resizeToFile(name, c, -1, name); err != nil {
				return err
			}
		}
	case d.shapeChanged:
		ack := -1
		if p.priceAsked() {
			ack = p.priceNew
		}
		confirm := ""
		if len(d.removes) > 0 {
			confirm = name
		}
		fmt.Fprintf(o.out, "  Resizing slice %s...\n", common.Highlight(name))
		if err := resizeToFile(name, c, ack, confirm); err != nil {
			return err
		}
	}

	// 3: what runs on it.
	if err := resolveSecretEnvRefs(c.Manifest); err != nil {
		return err
	}
	atomicForce = o.force
	if err := deployInto(c.Manifest, o.out); err != nil {
		return err
	}

	// 4: the keys, last, and only where they differ.
	if err := applyAPIKeys(c, p, o.out); err != nil {
		return err
	}

	if err := runHooks("post_deploy", post, dir); err != nil {
		fmt.Fprintf(o.out, "\n  %s the slice is applied and live, but a post_deploy hook failed\n", common.Cross())
		return err
	}
	if siteURL := buildSiteURL(); siteURL != "" {
		fmt.Fprintf(o.out, "\n  %s  %s\n", common.Check(), siteURL)
	}
	fmt.Fprintln(o.out)
	return nil
}

// resizeToFile resizes a slice to the file's shape with the plan's answers.
// A refusal here means the slice moved after the plan: the answers were for
// the plan, so they are not stretched to cover something else.
func resizeToFile(name string, c *Compiled, ack int, confirm string) error {
	err := slice.ResizeTo(name, c.Shape, ack, confirm)
	var refused *slice.ResizeRefused
	if errors.As(err, &refused) {
		return fmt.Errorf("%w.\n  The slice changed after the plan was made, or the platform asks something the plan "+
			"did not show. Nothing was resized; run `drift plan` again", refused)
	}
	return err
}

// settle asks, or checks the answers to, every question the plan raises, and
// returns an error naming each one left unanswered. Nothing has been sent when
// it returns.
func (o *applyOptions) settle(p *planState, d *planDiff) error {
	var missing []string

	if p.priceAsked() {
		switch {
		case o.answers.price == p.priceNew:
		case o.answers.price >= 0:
			return fmt.Errorf("the platform quotes %s a month for this shape, not the %s agreed to with --approve-price; "+
				"nothing was applied", euro(p.priceNew), euro(o.answers.price))
		case o.ask && o.confirm(fmt.Sprintf("Agree to %s a month?", euro(p.priceNew))):
		default:
			missing = append(missing, fmt.Sprintf("--approve-price %d", p.priceNew))
		}
	}
	for _, r := range uniq(d.removes) {
		addr := guardAddress(r)
		switch {
		case o.answers.removes[addr]:
		case o.ask && o.confirm(fmt.Sprintf("Remove %s?", r)):
		default:
			missing = append(missing, fmt.Sprintf("--approve-remove %q", addr))
		}
	}
	for _, l := range uniq(d.dataLoss) {
		addr := guardAddress(l)
		switch {
		case o.answers.dataLoss[addr]:
		case o.ask && o.confirmTyped(l, addr):
		default:
			missing = append(missing, fmt.Sprintf("--allow-data-loss %q", addr))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("nothing was applied: this plan needs an answer to each of these, and none was given:\n    %s\n"+
			"  Re-run with them once you have read the plan above", strings.Join(missing, "\n    "))
	}
	return nil
}

func (o *applyOptions) confirm(prompt string) bool {
	fmt.Fprintf(o.out, "  %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Fscanln(o.in, &ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// confirmTyped asks for a data loss to be agreed to by typing the resource's
// address, as a removal of a slice is agreed to by typing its name: a loss
// cannot be undone, and a y is too easy to give.
func (o *applyOptions) confirmTyped(what, addr string) bool {
	fmt.Fprintf(o.out, "  Data could be lost: %s.\n  Type %q to agree: ", what, addr)
	line, _ := readLine(o.in)
	return strings.TrimSpace(line) == addr
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func euro(cents int) string { return fmt.Sprintf("€%d.%02d", cents/100, cents%100) }

// writeOnlyValuesSet refuses an apply that would send a write-only value its
// environment does not hold, before anything is sent.
func writeOnlyValuesSet(c *Compiled) error {
	var missing []string
	for _, w := range c.WriteOnly {
		if !envSet(w.Env) {
			missing = append(missing, fmt.Sprintf("%s reads $%s, which is not set", w.Address, w.Env))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("nothing was applied: %s. Set each in the terminal, with --var, or in a .env file beside the Driftfile",
			strings.Join(missing, "; "))
	}
	return nil
}

// applyAPIKeys sets each key the file names where the slice holds none, or one
// whose fingerprint is not this key's. An unchanged key is not rotated.
func applyAPIKeys(c *Compiled, p *planState, out io.Writer) error {
	wrote := false
	for _, w := range c.WriteOnly {
		if w.Kind != "api_key" {
			continue
		}
		key := bookingKeyOf(c, w.Address)
		method, route, _ := strings.Cut(key, ":")
		value := os.Getenv(w.Env)
		current := false
		for _, k := range p.keys[key] {
			if strings.EqualFold(k.Method, method) && k.Fingerprint == keyFingerprint(value) {
				current = true
			}
		}
		if current && p.keysErr == nil {
			continue
		}
		body, err := json.Marshal(map[string]string{"function": route, "method": strings.ToUpper(method), "key": value})
		if err != nil {
			return err
		}
		resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/atomic/auth", strings.NewReader(string(body)))
		if err != nil {
			return common.TransportError("set the API key of "+w.Address, err)
		}
		_, cerr := common.CheckResponse(resp, "set the API key of "+w.Address)
		resp.Body.Close() // #nosec G104 -- the body has been read in full
		if cerr != nil {
			return cerr
		}
		if !wrote {
			fmt.Fprintf(out, "  %s\n", common.AtomicHeader())
			wrote = true
		}
		fmt.Fprintf(out, "    %s %s key set from $%s\n", common.Check(), w.Address, w.Env)
	}
	return nil
}

// ─── plan files ─────────────────────────────────────────────────────────────

// planRecord is what `drift plan --out` writes: what the plan was made from,
// and the answers it carries. No value of any write-only key is in it.
type planRecord struct {
	Version     int      `json:"version"`
	Slice       string   `json:"slice"`
	Environment string   `json:"environment,omitempty"`
	Driftfile   string   `json:"driftfile_sha256"`
	Live        string   `json:"live_sha256"`
	Price       int      `json:"price_monthly_cents"`
	Removes     []string `json:"removes"`
	DataLoss    []string `json:"data_loss"`
}

const planRecordVersion = 1

// liveHash is the live slice's shape as one hash, so a plan file can tell
// whether the slice moved since it was written. "none" for a slice that does
// not exist.
func liveHash(p *planState) string {
	if p.live == nil {
		return "none"
	}
	raw, _ := json.Marshal(p.live.Config)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// recordPlan builds the record of a plan.
func recordPlan(data []byte, p *planState, d *planDiff) planRecord {
	r := planRecord{
		Version: planRecordVersion, Slice: p.c.Manifest.Name(), Environment: p.c.Environment,
		Driftfile: fileHash(data), Live: liveHash(p), Price: -1,
		Removes: []string{}, DataLoss: []string{},
	}
	if p.priceAsked() {
		r.Price = p.priceNew
	}
	for _, x := range uniq(d.removes) {
		r.Removes = append(r.Removes, guardAddress(x))
	}
	for _, x := range uniq(d.dataLoss) {
		r.DataLoss = append(r.DataLoss, guardAddress(x))
	}
	return r
}

// writePlanFile writes the plan's record for `drift apply <file>`.
func writePlanFile(path string, data []byte, p *planState, d *planDiff) error {
	raw, err := json.MarshalIndent(recordPlan(data, p, d), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// checkPlanFile holds an apply to the plan a file recorded: the same Driftfile,
// the same live slice, and so the same questions, whose recorded answers it
// then gives. Anything that moved is a refusal, before anything is sent.
func checkPlanFile(path string, data []byte, p *planState, d *planDiff, a *applyAnswers) error {
	rec, err := readPlanRecord(path)
	if err != nil {
		return err
	}
	now := recordPlan(data, p, d)
	switch {
	case rec.Slice != now.Slice:
		return fmt.Errorf("the plan file is for slice %q, and this Driftfile names %q; nothing was applied", rec.Slice, now.Slice)
	case rec.Driftfile != now.Driftfile:
		return fmt.Errorf("the Driftfile changed after the plan was made; run `drift plan --out` again. Nothing was applied")
	case rec.Live != now.Live:
		return fmt.Errorf("slice %q changed after the plan was made; run `drift plan --out` again. Nothing was applied", now.Slice)
	case rec.Price != now.Price || strings.Join(rec.Removes, "\n") != strings.Join(now.Removes, "\n") ||
		strings.Join(rec.DataLoss, "\n") != strings.Join(now.DataLoss, "\n"):
		return fmt.Errorf("this plan no longer asks what the recorded one did; run `drift plan --out` again. Nothing was applied")
	}
	if rec.Price >= 0 {
		a.price = rec.Price
	}
	for _, r := range rec.Removes {
		a.removes[r] = true
	}
	for _, l := range rec.DataLoss {
		a.dataLoss[l] = true
	}
	return nil
}

// readPlanRecord reads a plan file `drift plan --out` wrote.
func readPlanRecord(path string) (planRecord, error) {
	raw, err := os.ReadFile(path) // #nosec G304 — a plan file the caller names
	if err != nil {
		return planRecord{}, fmt.Errorf("read the plan file: %w", err)
	}
	var rec planRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Version != planRecordVersion {
		return planRecord{}, fmt.Errorf("%s is not a plan file this CLI wrote (drift plan --out)", path)
	}
	return rec, nil
}

// isPlanFile reports whether apply's argument names a plan file rather than
// an environment: a .json name, or a file that exists. An environment's name
// is a slice-name fragment, which is never either.
func isPlanFile(arg string) bool {
	if strings.HasSuffix(arg, ".json") {
		return true
	}
	info, err := os.Stat(arg)
	return err == nil && !info.IsDir()
}
