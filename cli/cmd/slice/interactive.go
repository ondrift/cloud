package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	project "github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"golang.org/x/term"
)

// interactive.go — creating a slice without leaving the terminal.
//
// The browser handoff still exists and still works. It is no longer the only
// way to buy a shape, and it is no longer what `drift slice create` does when
// somebody is sitting at a prompt: a tool whose first act is to open a web page
// has handed the job to something the user did not choose, and every failure of
// that page becomes a failure of this command.
//
// What is asked is what the platform prices per unit and cannot guess: the
// name, how many function slots, and each slot's route and booking. Everything
// else has a defensible default the server already owns, so asking would be
// theatre — `drift slice resize` changes any of it afterwards.
//
// The price is NOT computed here. It comes from POST /ops/slice/price, the same
// endpoint the browser form calls, because a client shipping its own rates is a
// second source of truth that disagrees the day a unit price moves.

// The bounds the platform enforces, restated here only to refuse a value before
// a round trip. Every one of them is checked again server-side, which is where
// they are actually owned — these exist so a typo is answered by the prompt that
// caused it rather than by a 400 three questions later.
const (
	bytesPerMiB      = 1024 * 1024
	minMemoryMiB     = 8
	maxMemoryMiB     = 256
	compiledFloorMiB = 16
	maxFunctionSlots = 64
)

var httpMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", "queue"}

// A slice name is what its hostname is built from, so the rule is DNS's, not
// this program's.
var sliceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// A route is the path under /api/. Leading slashes are the commonest thing to
// type and the one the platform will not take, so it is refused by name rather
// than trimmed — trimming it would teach that the two spellings are the same,
// and the booking key says otherwise.
var routeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_:.-]*$`)

func validSliceName(v any) error {
	s := strings.TrimSpace(fmt.Sprint(v))
	if !sliceNameRe.MatchString(s) {
		return fmt.Errorf("lowercase letters, numbers and dashes, starting and ending with one of the first two")
	}
	return nil
}

func validRoute(v any) error {
	s := strings.TrimSpace(fmt.Sprint(v))
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("no leading slash — the route is the path under /api/, so %q is %q", s, strings.TrimLeft(s, "/"))
	}
	if !routeRe.MatchString(s) {
		return fmt.Errorf("letters, numbers and / _ : . -")
	}
	return nil
}

func validMemory(v any) error {
	n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
	if err != nil {
		return fmt.Errorf("a number of MB, please")
	}
	if n < minMemoryMiB || n > maxMemoryMiB {
		return fmt.Errorf("between %d and %d MB", minMemoryMiB, maxMemoryMiB)
	}
	return nil
}

// interactive reports whether there is somebody at the other end to ask.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// euros renders cents the way `drift slice list` already does, so one price
// does not read two ways in one session.
func euros(cents int) string {
	return fmt.Sprintf("EUR %d.%02d", cents/100, cents%100)
}

// functionSlot is one function the tenant declared, in the order they were
// asked for.
type functionSlot struct {
	Method string
	Route  string
	Memory int // MiB
}

// backboneShape is what the Backbone questions collected. Each list is name to
// size in MiB, except queues, whose number is a DEPTH in messages.
type backboneShape struct {
	Collections map[string]int
	Databases   map[string]int
	Buckets     map[string]int
	Queues      map[string]int
}

// buildConfig assembles what the API is sent.
//
// TWO SPELLINGS, and getting them wrong fails silently. models.SliceConfig tags
// its containers and its MAPS for json — `atomic`, `backbone`, `nosql`,
// `collections`, `buckets` — but its scalar limits carry only a bson tag, so
// encoding/json falls back to the Go field name. `MaxStorageBytes` is the key;
// `max_storage_bytes` decodes to zero, and a zero there is not an error
// anywhere: the slice is simply created with no runner volume and the figure
// the tenant typed is gone. The browser form sends the same PascalCase for the
// same reason (it reads cfg.atomic.MaxFunctionMemoryBytes).
//
// Only what was asked is set. Every other limit is left absent so the platform
// fills it from its own defaults — writing a figure here would make this a
// second place those defaults live, and the first one to drift.
func buildConfig(storageMiB int, slots []functionSlot, bb backboneShape, canvasMiB int) map[string]any {
	atomic := configFromSlots(slots)["atomic"].(map[string]any)
	if storageMiB > 0 {
		atomic["MaxStorageBytes"] = storageMiB * bytesPerMiB
	}

	cfg := map[string]any{"atomic": atomic}

	backbone := map[string]any{}
	if len(bb.Collections) > 0 {
		backbone["nosql"] = map[string]any{"collections": bytesOf(bb.Collections)}
	}
	if len(bb.Databases) > 0 {
		backbone["sql"] = map[string]any{"databases": bytesOf(bb.Databases)}
	}
	if len(bb.Buckets) > 0 {
		backbone["blobs"] = map[string]any{"buckets": bytesOf(bb.Buckets)}
	}
	if len(bb.Queues) > 0 {
		// A queue's number is a DEPTH in messages, not bytes. Multiplying it by
		// a MiB would sell somebody a queue a million messages deep.
		backbone["queues"] = map[string]any{"queues": bb.Queues}
	}
	if len(backbone) > 0 {
		cfg["backbone"] = backbone
	}

	if canvasMiB > 0 {
		cfg["canvas"] = map[string]any{"TotalMaxSizeInBytes": canvasMiB * bytesPerMiB}
	}
	return cfg
}

// bytesOf converts a name→MiB map to name→bytes, which is what every declared
// size is stored and enforced in.
func bytesOf(mib map[string]int) map[string]int {
	out := make(map[string]int, len(mib))
	for name, n := range mib {
		out[name] = n * bytesPerMiB
	}
	return out
}

// configFromSlots builds the atomic half.
func configFromSlots(slots []functionSlot) map[string]any {
	functions := make([]map[string]any, 0, len(slots))
	for _, s := range slots {
		functions = append(functions, map[string]any{
			// Both halves, and the composed key. The platform derives the key
			// from route and method, but it is what the runtime looks a pool up
			// by, so sending it explicitly means the string that is booked is the
			// string that was shown at the confirmation.
			"name":         strings.ToLower(s.Method) + ":" + s.Route,
			"route":        s.Route,
			"method":       strings.ToLower(s.Method),
			"memory_bytes": s.Memory * bytesPerMiB,
		})
	}
	return map[string]any{
		"atomic": map[string]any{
			"functions": functions,
		},
	}
}

// priceOf asks the platform what a config costs. The CLI ships no rates.
func priceOf(cfg map[string]any, months int) (int, error) {
	body, _ := json.Marshal(map[string]any{
		"config":                cfg,
		"billing_period_months": months,
	})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/price", bytes.NewBuffer(body))
	if err != nil {
		return 0, common.TransportError("price slice", err)
	}
	defer resp.Body.Close()

	data, err := common.CheckResponse(resp, "price slice")
	if err != nil {
		return 0, err
	}
	var priced struct {
		MonthlyCents int `json:"monthly_cents"`
	}
	if uerr := json.Unmarshal(data, &priced); uerr != nil {
		return 0, fmt.Errorf("the price endpoint answered something this version cannot read: %w", uerr)
	}
	return priced.MonthlyCents, nil
}

// summarise prints the shape and its price, and asks to go ahead.
//
// The price is stated before the question and the question names it, because a
// confirmation that does not repeat what is being agreed to is a keystroke, not
// a decision.
// summarise prints what is about to be created. It does NOT ask.
//
// The asking already happened: the form's Create row carries the price beside
// it, so Enter on "Create   EUR 1.53/mo" is a decision made with the figure
// under the cursor. A second y/N was belt and braces until it became the reason
// the command hung — the form's key reader is still on stdin when it runs, and
// two readers on one terminal means the keystroke goes to whichever asks first,
// which was never the one waiting for it.
func summarise(name string, shape declaredShape, monthlyCents int) {
	slots := shape.Slots
	fmt.Printf("\n  %s\n", name)

	if shape.StorageMiB > 0 {
		fmt.Printf("    code & dependencies          %d MB\n", shape.StorageMiB)
	}
	if len(slots) == 0 {
		fmt.Println("    no functions")
	}
	total := 0
	for _, s := range slots {
		fmt.Printf("    %-6s %-28s %d MB\n", strings.ToUpper(s.Method), s.Route, s.Memory)
		total += s.Memory
	}
	if len(slots) > 0 {
		fmt.Printf("    %d function%s, %d MB booked in total\n",
			len(slots), plural(len(slots)), total)
	}

	// Named, because the name is the resource: an undeclared collection is not
	// a smaller one, it is one the slice refuses every write to.
	printNamed("collection", shape.Backbone.Collections, "MB")
	printNamed("database", shape.Backbone.Databases, "MB")
	printNamed("bucket", shape.Backbone.Buckets, "MB")
	printNamed("queue", shape.Backbone.Queues, "deep")

	if shape.CanvasMiB > 0 {
		fmt.Printf("    site storage                 %d MB\n", shape.CanvasMiB)
	}

	if monthlyCents == 0 {
		fmt.Printf("\n  This is the free slice — %s/month.\n", euros(0))
	} else {
		fmt.Printf("\n  %s/month.\n", euros(monthlyCents))
	}

}

// createFromPrompts is the whole command when nobody asked for a browser: ask,
// price against the server, show what was built, confirm, create.
//
// The price is fetched BEFORE the confirmation and the confirmation repeats it,
// so nothing is bought at a figure that was never on screen. A shape that prices
// at zero is sent as the free tier, which is what the platform would have
// charged for it anyway — the difference is the tier label, and that is what
// counts an account's one free slice.
func createFromPrompts(name string, billingMonths int) error {
	if !interactive() {
		return fmt.Errorf(
			"drift slice create draws a form, which needs a terminal.\n" +
				"  In CI or a script, pass --free for a free slice, or --browser to shape it in " +
				"the configurator")
	}

	shape, chosen, ok, err := runShapeForm(name)
	if err != nil || !ok {
		if err == nil {
			fmt.Println("Nothing was created.")
		}
		return err
	}
	cfg := buildConfig(shape.StorageMiB, shape.Slots, shape.Backbone, shape.CanvasMiB)

	if billingMonths < 1 {
		billingMonths = 1
	}
	monthlyCents, err := priceOf(cfg, billingMonths)
	if err != nil {
		return err
	}

	summarise(chosen, shape, monthlyCents)

	payload := map[string]any{"name": chosen, "config": cfg}
	if monthlyCents == 0 {
		payload["tier"] = "hacker"
	} else {
		payload["tier"] = "custom"
		payload["billing_period_months"] = billingMonths
		// The stub the api still requires outside local dev. Phase G validates
		// it against the real provider; until then sending nothing is a 400 and
		// sending this is what every other paid path already does.
		payload["payment_token"] = "cli"
	}

	body, _ := json.Marshal(payload)
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/create", bytes.NewBuffer(body))
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()
	if _, err := common.CheckResponse(resp, "create slice"); err != nil {
		return err
	}

	// Same wait the --free path does, and for the same reason: the record exists
	// the moment create returns but the components behind it do not, and the
	// documented next command is a deploy. A deploy issued into that gap is
	// refused by something that is not listening yet, and every 5xx renders as
	// the maintenance message — so the reader is told to wait for Drift when what
	// they are waiting for is their own slice.
	fmt.Printf("  Waiting for slice %q to come up...\n", chosen)
	if werr := project.WaitForSliceReady(chosen); werr != nil {
		if serr := common.SaveActiveSlice(chosen); serr != nil {
			fmt.Println("Warning: couldn't mark the new slice as active —", serr)
		}
		return fmt.Errorf("slice %q was created and is still starting: %w", chosen, werr)
	}
	if serr := common.SaveActiveSlice(chosen); serr != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", serr)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", chosen)
	return nil
}

// declaredShape is everything the prompts collected, kept together so the
// summary shows the same thing that was sent rather than a second assembly of
// it.
type declaredShape struct {
	StorageMiB int
	Slots      []functionSlot
	Backbone   backboneShape
	CanvasMiB  int
}

// printNamed lists one declared set in a stable order. Map order is randomised,
// and a summary that reorders itself between two runs of the same answers reads
// as the shape having changed.
func printNamed(noun string, items map[string]int, unit string) {
	if len(items) == 0 {
		return
	}
	names := make([]string, 0, len(items))
	for n := range items {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("    %-10s %-17s %d %s\n", noun, n, items[n], unit)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
