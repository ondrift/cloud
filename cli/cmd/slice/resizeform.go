package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// resizeform.go — changing a slice's shape without leaving the terminal.
//
// The shape form, opened on what the slice already is. Resizing is the half of shape-ownership that had no terminal path at all:
// every spelling of `drift slice resize` handed off to a web page, so a slice
// could be made from a prompt and only ever changed from a browser.
//
// Two things make a resize more than a create with the fields filled in, and
// both are refusals the platform answers with a question rather than an error:
//
//   - A change that moves the monthly figure is refused until the new figure
//     is sent back.
//   - A change that takes something away is refused until the slice is named.
//
// One refusal can ask both. Both are answered by acting on the form again, which is why the submit
// happens inside the form's own loop (shapeForm.onSubmit) rather than after it.

// sliceRecord is the part of GET /ops/slice?name= this form reads.
type sliceRecord struct {
	Name             string         `json:"name"`
	Tier             string         `json:"tier"`
	MonthlyCostCents int            `json:"monthly_cost_cents"`
	Config           map[string]any `json:"config"`
}

// fetchSliceRecord reads one slice, config included.
func fetchSliceRecord(name string) (*sliceRecord, error) {
	resp, err := common.DoRequest(http.MethodGet,
		common.APIBaseURL+"/ops/slice?name="+name, nil)
	if err != nil {
		return nil, common.TransportError("read slice", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "read slice")
	if err != nil {
		return nil, err
	}
	var rec sliceRecord
	if uerr := json.Unmarshal(body, &rec); uerr != nil {
		return nil, fmt.Errorf("the slice record could not be read: %w", uerr)
	}
	return &rec, nil
}

// findNode returns the first node matching pred, depth first.
func findNode(ns []*node, pred func(*node) bool) *node {
	for _, n := range ns {
		if pred(n) {
			return n
		}
		if got := findNode(n.children, pred); got != nil {
			return got
		}
	}
	return nil
}

// intAt reads a whole number out of a decoded config by path. Absent is 0,
// which is what the config itself means by a knob nobody declared.
func intAt(cfg map[string]any, path []string) int {
	var cur any = cfg
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		if cur, ok = obj[key]; !ok {
			return 0
		}
	}
	switch v := cur.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// mapAt reads a declared name→size map out of a decoded config.
func mapAt(cfg map[string]any, path []string) map[string]int {
	var cur any = cfg
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = obj[key]; !ok {
			return nil
		}
	}
	raw, ok := cur.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]int, len(raw))
	for name, v := range raw {
		if n, ok := v.(float64); ok {
			out[name] = int(n)
		}
	}
	return out
}

// seedItems inserts n items into a list, ahead of the "+ Add" row that makes
// them, and hands each to fill.
func seedItems(f *shapeForm, prefix string, n int, fill func(item *node, i int)) {
	add := findNode(f.root, func(x *node) bool { return x.kind == kindAdd && x.prefix == prefix })
	if add == nil || n <= 0 {
		return
	}
	parent := f.parentOf(add)
	if parent == nil {
		return
	}
	at := -1
	for i, c := range parent.children {
		if c == add {
			at = i
		}
	}
	if at < 0 {
		return
	}

	made := make([]*node, 0, n)
	for i := 0; i < n; i++ {
		item := add.make(i + 1)
		item.prefix = add.prefix
		fill(item, i)
		made = append(made, item)
	}
	grown := make([]*node, 0, len(parent.children)+n)
	grown = append(grown, parent.children[:at]...)
	grown = append(grown, made...)
	grown = append(grown, parent.children[at:]...)
	parent.children = grown
}

// seedNamed fills one of the name→size lists from what the slice declares, in a
// stable order so two runs of the same slice read the same way.
func seedNamed(f *shapeForm, prefix string, declared map[string]int, scale int) {
	names := sortedNames(declared)
	seedItems(f, prefix, len(names), func(item *node, i int) {
		item.value = names[i]
		if len(item.children) > 0 {
			item.children[0].value = strconv.Itoa(declared[names[i]] / scale)
		}
	})
}

func sortedNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// fillFromConfig opens the form on what the slice already is.
//
// A blank number means the slice DECLARES nothing there and the platform
// applies its own figure — which is exactly what the create path means by a
// blank, and what buildConfig encodes by leaving the key out. The alternative,
// painting in the figure the platform would apply, turns every default into a
// declaration the moment the form is submitted: the tenant would be buying, and
// then owning, limits they never chose.
func fillFromConfig(f *shapeForm, cfg map[string]any) {
	// Every dial, from the path it names.
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, c := range ns {
			if c.scalar != "" {
				scale := c.scale
				if scale == 0 {
					scale = 1
				}
				if v := intAt(cfg, strings.Split(c.scalar, ".")); v > 0 {
					c.value = strconv.Itoa(v / scale)
				} else {
					c.value = "0"
				}
			}
			walk(c.children)
		}
	}
	walk(f.root)

	// The functions the slice declares, each with its own booking.
	declared := intAt(cfg, []string{"atomic", "MaxNumberOfFunctions"})
	fns := declaredFunctions(cfg)
	if len(fns) > 0 {
		seedItems(f, "Function ", len(fns), func(item *node, i int) {
			item.value = fns[i].Route
			if len(item.children) > 1 {
				item.children[0].value = fns[i].Method
				item.children[1].value = strconv.Itoa(fns[i].MemoryBytes / bytesPerMiB)
			}
		})
	} else if declared > 0 {
		// A slice from before per-function bookings: it has SLOTS and one pool
		// they all draw on, and no function names at all. The pool is shown as a
		// placeholder on each row and never as a value — it belongs to the whole
		// slice, so writing it into every row multiplies it by the slot count and
		// reprices the slice at several times what it costs.
		pool := intAt(cfg, []string{"atomic", "MaxFunctionMemoryBytes"}) / bytesPerMiB
		seedItems(f, "Function ", declared, func(item *node, i int) {
			if len(item.children) > 1 {
				mem := item.children[1]
				mem.value = ""
				if pool > 0 {
					mem.placeholder = strconv.Itoa(pool)
					mem.hint = fmt.Sprintf("These functions share one %d MB pool, which is the "+
						"greyed figure. Leave it blank to keep that, or type a figure to buy "+
						"this route a pool of its own — which reprices the whole slice.", pool)
				}
			}
		})
	}

	seedNamed(f, "Collection ", mapAt(cfg, []string{"backbone", "nosql", "collections"}), bytesPerMiB)
	seedNamed(f, "Database ", mapAt(cfg, []string{"backbone", "sql", "databases"}), bytesPerMiB)
	seedNamed(f, "Bucket ", mapAt(cfg, []string{"backbone", "blobs", "buckets"}), bytesPerMiB)
	// A queue's number is a DEPTH in messages, not bytes, so it is not scaled.
	seedNamed(f, "Queue ", mapAt(cfg, []string{"backbone", "queues", "queues"}), 1)
}

// declaredFunction is one entry of the config's function list.
type declaredFunction struct {
	Route       string `json:"route"`
	Method      string `json:"method"`
	MemoryBytes int    `json:"memory_bytes"`
}

// declaredFunctions pulls the declared functions out of a decoded config.
func declaredFunctions(cfg map[string]any) []declaredFunction {
	atomic, ok := cfg["atomic"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := atomic["functions"]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []declaredFunction
	if uerr := json.Unmarshal(b, &out); uerr != nil {
		return nil
	}
	return out
}

// resizeRefusal is the answerable half of a 409, in the operator's shape
// (applyResize in the platform's operator routes). One refusal can carry both
// questions at once, the price and the removal, and each is answered on its own.
type resizeRefusal struct {
	Error       string `json:"error"`
	PriceChange *struct {
		CurrentMonthlyCents int `json:"current_monthly_cents"`
		NewMonthlyCents     int `json:"new_monthly_cents"`
		// What the slice is billed today, sent only when that is a figure from
		// an older price list rather than today's price of its shape.
		BilledMonthlyCents *int `json:"billed_monthly_cents"`
	} `json:"price_change"`
	ConfirmationRequired *struct {
		// The slice's NAME, which is the exact answer the server will accept in
		// confirm_slice_name. A string: decoding it as a flag failed the whole
		// reply, and every removal fell through to a generic refusal nobody could
		// answer.
		ConfirmSliceName string              `json:"confirm_slice_name"`
		Destroys         []map[string]string `json:"destroys"`
	} `json:"confirmation_required"`
	Violations []struct {
		Resource string `json:"resource"`
		Used     int    `json:"used"`
		NewLimit int    `json:"new_limit"`
	} `json:"violations"`
}

// asksPrice reports whether the refusal asks the price question.
func (r *resizeRefusal) asksPrice() bool { return r != nil && r.PriceChange != nil }

// asksRemoval reports whether the refusal asks the removal question, and with
// what name.
func (r *resizeRefusal) asksRemoval() bool {
	return r != nil && r.ConfirmationRequired != nil && r.ConfirmationRequired.ConfirmSliceName != ""
}

// priceLine states the price question: both figures, and the figure billed
// today when the server says it is from an older price list.
func (r *resizeRefusal) priceLine() string {
	p := r.PriceChange
	line := fmt.Sprintf("This resize changes what the slice costs, from %s to %s a month.",
		euros(p.CurrentMonthlyCents), euros(p.NewMonthlyCents))
	if p.BilledMonthlyCents != nil && *p.BilledMonthlyCents != p.CurrentMonthlyCents {
		line += fmt.Sprintf(" It is billed %s today, a figure from an older price list, and the new figure replaces it.",
			euros(*p.BilledMonthlyCents))
	}
	return line
}

// postResize sends one attempt and separates the answerable refusals from the
// rest. A 409 is not necessarily an error here: it is often the platform asking
// a question the form can carry back.
func postResize(payload map[string]any) (ok bool, refusal *resizeRefusal, err error) {
	body, _ := json.Marshal(payload)
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/slice/resize", bytes.NewBuffer(body))
	if err != nil {
		return false, nil, common.TransportError("resize slice", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil, nil
	}
	if resp.StatusCode == http.StatusConflict {
		var r resizeRefusal
		if json.Unmarshal(raw, &r) == nil {
			return false, &r, nil
		}
	}
	// Anything else is the caller's to fix, and its own message is the best one
	// available — the api relays the operator's verbatim.
	var generic struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &generic)
	if generic.Error == "" {
		generic.Error = strings.TrimSpace(string(raw))
	}
	return false, nil, fmt.Errorf("resize refused: %s", generic.Error)
}

// noTTYResize is the refusal both form entry points give when there is no
// terminal to draw on.
//
// IT NAMES --dump AND --config BECAUSE THEY ARE THE NON-INTERACTIVE RESIZE.
// This said there was no such thing, two flags away from the loop that is
// exactly one — so the caller who had already hit the only wall this message
// exists to explain was told the capability did not exist, and a CI job went
// looking for a workaround it did not need.
//
// One function for one refusal: the two entry points had drifted into two
// different messages for one condition, and the shorter one said nothing at
// all about what to do instead.
func noTTYResize(name string) error {
	return fmt.Errorf(
		"drift slice resize draws a form, which needs a terminal.\n"+
			"  In CI or a script, --dump and --config are the same change:\n"+
			"    drift slice resize %s --dump > shape.json\n"+
			"    $EDITOR shape.json\n"+
			"    drift slice resize %s --config shape.json\n"+
			"  A resize that moves the price or takes something away is still refused\n"+
			"  until answered — with --acknowledge-monthly-cents and --confirm. Each\n"+
			"  refusal says which it needs, and what the figure or the loss is.",
		name, name)
}

// resizeFromPrompts opens the form on a slice and applies what comes back.
func resizeFromPrompts(name string, billingMonths int) error {
	if !interactive() {
		return noTTYResize(name)
	}
	if billingMonths < 1 {
		billingMonths = 1
	}

	rec, err := fetchSliceRecord(name)
	if err != nil {
		return err
	}
	return ResizeWithConfig(rec.Name, rec.Config, billingMonths)
}

// ResizeWithConfig opens the resize form on a config the caller already holds.
//
// `drift project benchmark --apply` uses it to open on the live shape with the
// measured bookings already overlaid, so the recommendation is in the rows and
// still has to be applied. Exported because that caller is another package.
func ResizeWithConfig(name string, cfg map[string]any, billingMonths int) error {
	if !interactive() {
		return noTTYResize(name)
	}
	if billingMonths < 1 {
		billingMonths = 1
	}
	rec := &sliceRecord{Name: name, Config: cfg}

	f := newShapeForm(rec.Name)
	// The name is what is being resized, not a field of the shape.
	f.root[0].locked = true
	f.root[0].hint = "The slice being resized. A resize cannot rename a slice."
	f.createNode.label = "Apply"
	f.createNode.hint = "Applies this shape to the slice at the price shown here."
	fillFromConfig(f, rec.Config)

	// Carried between presses: the figure the platform asked to have agreed to,
	// and whether it asked for the slice's name. Both are sent only once asked
	// for, so an ordinary resize carries neither.
	var agreedCents *int
	var confirmName string

	f.onSubmit = func(shape declaredShape, chosen string) (bool, error) {
		cfg := buildConfig(shape.StorageMiB, shape.Slots, shape.Backbone, shape.CanvasMiB, shape.BackboneScalars)

		payload := map[string]any{
			"name":                  rec.Name,
			"config":                cfg,
			"billing_period_months": billingMonths,
			"payment_token":         "cli",
		}
		if agreedCents != nil {
			payload["acknowledge_monthly_cents"] = *agreedCents
		}
		if confirmName != "" {
			payload["confirm_slice_name"] = confirmName
		}

		ok, refusal, perr := postResize(payload)
		if perr != nil {
			return false, perr
		}
		if ok {
			fmt.Printf("Slice '%s' resized.\n", rec.Name)
			return true, nil
		}

		// A refusal that carries a question, or both at once. Each answer sets
		// what the next press will send, and the form says what pressing it now
		// agrees to.
		if refusal.asksPrice() || refusal.asksRemoval() {
			a := answerRefusal(refusal, os.Stdout)
			if a.ack != nil {
				agreedCents = a.ack
			}
			if a.confirm != "" {
				confirmName = a.confirm
			}
			f.status, f.createNode.label = a.status, a.label
			return false, nil
		}

		switch {
		case len(refusal.Violations) > 0:
			parts := make([]string, 0, len(refusal.Violations))
			for _, v := range refusal.Violations {
				parts = append(parts, fmt.Sprintf("%s holds %d, this asks for %d",
					v.Resource, v.Used, v.NewLimit))
			}
			f.status = "smaller than what the slice already holds — " + strings.Join(parts, "; ")

		default:
			f.status = refusal.Error
		}
		return false, nil
	}

	_, _, applied, rerr := runForm(f)
	if rerr != nil {
		return rerr
	}
	if !applied {
		fmt.Println("Nothing was changed.")
	}
	return nil
}

// formAnswer is what the form sends on its next press, and what it says.
type formAnswer struct {
	ack     *int
	confirm string
	status  string
	label   string
}

// answerRefusal turns a refusal's questions into the form's next press. Both
// are answered when both are asked: the server asks them together, and pressing
// Apply once more agrees to every figure and every loss listed. What goes is
// printed to out while the screen is down, so it can be read as ordinary output
// before it is agreed to.
func answerRefusal(r *resizeRefusal, out io.Writer) formAnswer {
	var a formAnswer
	var says []string
	if r.asksPrice() {
		cents := r.PriceChange.NewMonthlyCents
		a.ack = &cents
		says = append(says, r.priceLine())
	}
	if r.asksRemoval() {
		fmt.Fprintln(out, "This resize takes something away:")
		for _, d := range r.ConfirmationRequired.Destroys {
			fmt.Fprintf(out, "  %s\n", describeDestroy(d))
		}
		fmt.Fprintln(out)
		a.confirm = r.ConfirmationRequired.ConfirmSliceName
		says = append(says, "What it takes away is listed above.")
	}
	switch {
	case a.ack != nil && a.confirm != "":
		a.status = strings.Join(says, " ") + fmt.Sprintf(" Press Apply again to agree to the price and confirm %q.", a.confirm)
		a.label = fmt.Sprintf("Agree to %s/mo, confirm and apply", euros(*a.ack))
	case a.ack != nil:
		a.status = strings.Join(says, " ") + " Press Apply again to agree to it."
		a.label = fmt.Sprintf("Agree to %s/mo and apply", euros(*a.ack))
	default:
		a.status = fmt.Sprintf("Listed above. Press Apply again to confirm %q.", a.confirm)
		a.label = "Confirm and apply"
	}
	return a
}
