package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// resizeform.go — changing a slice's shape without leaving the terminal.
//
// The same screen `drift slice create` draws, opened on what the slice already
// is. Resizing is the half of shape-ownership that had no terminal path at all:
// every spelling of `drift slice resize` handed off to a web page, so a slice
// could be made from a prompt and only ever changed from a browser.
//
// Two things make a resize more than a create with the fields filled in, and
// both are refusals the platform answers with a question rather than an error:
//
//   - Booking memory per function for the first time reprices the whole slice,
//     and is refused until the new figure is sent back.
//   - A change that takes something away is refused until the slice is named.
//
// Both are answered by acting on the form again, which is why the submit
// happens inside the form's own loop (shapeForm.onSubmit) rather than after it.

// sliceRecord is the part of GET /ops/slice/get this form reads.
type sliceRecord struct {
	Name             string         `json:"name"`
	Tier             string         `json:"tier"`
	MonthlyCostCents int            `json:"monthly_cost_cents"`
	Config           map[string]any `json:"config"`
}

// fetchSliceRecord reads one slice, config included.
func fetchSliceRecord(name string) (*sliceRecord, error) {
	resp, err := common.DoRequest(http.MethodGet,
		common.APIBaseURL+"/ops/slice/get?name="+name, nil)
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

// resizeRefusal is the answerable half of a 409.
type resizeRefusal struct {
	Error       string `json:"error"`
	PriceChange *struct {
		CurrentMonthlyCents int `json:"current_monthly_cents"`
		NewMonthlyCents     int `json:"new_monthly_cents"`
	} `json:"price_change"`
	ConfirmationRequired *struct {
		ConfirmSliceName bool                `json:"confirm_slice_name"`
		Destroys         []map[string]string `json:"destroys"`
	} `json:"confirmation_required"`
	Violations []struct {
		Resource string `json:"resource"`
		Used     int    `json:"used"`
		NewLimit int    `json:"new_limit"`
	} `json:"violations"`
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

// resizeFromPrompts opens the form on a slice and applies what comes back.
func resizeFromPrompts(name string, billingMonths int) error {
	if !interactive() {
		return fmt.Errorf(
			"drift slice resize draws a form, which needs a terminal.\n" +
				"  In CI or a script there is no non-interactive resize: a resize can " +
				"reprice a slice or destroy what it holds, and both are answered at the form")
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
		return fmt.Errorf("drift slice resize draws a form, which needs a terminal")
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

		// A refusal that carries a question. Each sets what the next press will
		// send and says, on the form, what pressing it now means.
		switch {
		case refusal.PriceChange != nil:
			cents := refusal.PriceChange.NewMonthlyCents
			agreedCents = &cents
			f.status = fmt.Sprintf("This resize moves what you pay, from %s to %s a month. "+
				"Press Apply again to agree to it.",
				euros(refusal.PriceChange.CurrentMonthlyCents), euros(cents))
			f.createNode.label = fmt.Sprintf("Agree to %s/mo and apply", euros(cents))

		case refusal.ConfirmationRequired != nil && refusal.ConfirmationRequired.ConfirmSliceName:
			// Named before the act, not after: the list is what the tenant is
			// agreeing to, so it is printed while the screen is down and can be
			// read as ordinary output.
			fmt.Println("This resize takes something away:")
			for _, d := range refusal.ConfirmationRequired.Destroys {
				fmt.Printf("  %s: %s → %s (%s)\n",
					d["resource"], d["current"], d["wanted"], d["destroys"])
			}
			fmt.Println()
			confirmName = rec.Name
			f.status = fmt.Sprintf("Listed above. Press Apply again to confirm %q.", rec.Name)
			f.createNode.label = "Confirm and apply"

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
