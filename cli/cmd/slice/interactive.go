package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	project "github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/AlecAivazis/survey/v2"
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

// createInteractive walks the prompts and returns the config to create, or
// ok=false when the user backed out at the confirmation.
//
// name may arrive pre-filled from the positional argument; it is still shown,
// because a value the user typed one command ago is not a value they are
// looking at now.
func runPrompts(name string) (map[string]any, string, []functionSlot, bool, error) {
	if !interactive() {
		return nil, "", nil, false, fmt.Errorf(
			"drift slice create needs a terminal to ask what to build.\n" +
				"  In CI or a script, pass --free for a free slice, or --browser to shape it in " +
				"the configurator")
	}

	if err := survey.AskOne(
		&survey.Input{Message: "Slice name:", Default: name},
		&name,
		survey.WithValidator(survey.Required),
		survey.WithValidator(validSliceName),
	); err != nil {
		return nil, "", nil, false, err
	}
	name = strings.TrimSpace(name)

	count, err := askCount()
	if err != nil {
		return nil, "", nil, false, err
	}

	slots := make([]functionSlot, 0, count)
	for i := 0; i < count; i++ {
		slot, err := askSlot(i + 1)
		if err != nil {
			return nil, "", nil, false, err
		}
		slots = append(slots, slot)
	}

	return configFromSlots(slots), name, slots, true, nil
}

// askCount asks how many function slots, and takes zero for an answer.
//
// A slice with no functions is a real and common shape — a canvas-only site, a
// slice bought for its storage — so this is not validated upwards. It is
// validated at the ceiling the platform enforces, because learning about that
// after typing eight routes is learning it too late.
func askCount() (int, error) {
	var raw string
	if err := survey.AskOne(
		&survey.Input{Message: "Atomic functions:", Default: "0"},
		&raw,
		survey.WithValidator(func(v any) error {
			n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
			if err != nil {
				return fmt.Errorf("a number, please")
			}
			if n < 0 {
				return fmt.Errorf("a slice cannot have fewer than no functions")
			}
			if n > maxFunctionSlots {
				return fmt.Errorf("%d is more than a slice may hold (%d)", n, maxFunctionSlots)
			}
			return nil
		}),
	); err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	return n, nil
}

// askSlot asks for one function's identity and its booking.
//
// The method is a Select rather than an Input: it is a closed set the platform
// composes the booking key from, and a typo in it produces a key no invocation
// reaches rather than an error.
func askSlot(n int) (functionSlot, error) {
	fmt.Printf("\nFunction %d\n", n)

	var slot functionSlot
	if err := survey.AskOne(
		&survey.Select{Message: "  method:", Options: httpMethods, Default: "post"},
		&slot.Method,
	); err != nil {
		return slot, err
	}

	if err := survey.AskOne(
		&survey.Input{Message: "  route:", Help: "The path under /api/, e.g. auth/challenge"},
		&slot.Route,
		survey.WithValidator(survey.Required),
		survey.WithValidator(validRoute),
	); err != nil {
		return slot, err
	}
	slot.Route = strings.TrimSpace(slot.Route)

	// No default. A booking nobody chose is a bill nobody chose, and this is the
	// one number on the form that is billed per unit — the platform refuses to
	// invent it in the Driftfile for the same reason.
	var raw string
	if err := survey.AskOne(
		&survey.Input{
			Message: "  memory (MB):",
			Help: fmt.Sprintf("What this function's simultaneous invocations share. %d-%d. "+
				"Go and Rust need at least %d.", minMemoryMiB, maxMemoryMiB, compiledFloorMiB),
		},
		&raw,
		survey.WithValidator(survey.Required),
		survey.WithValidator(validMemory),
	); err != nil {
		return slot, err
	}
	slot.Memory, _ = strconv.Atoi(strings.TrimSpace(raw))
	return slot, nil
}

// configFromSlots builds the config to send.
//
// Only what was asked is set. Every other limit is left at its zero value so
// the platform fills it from its own defaults — writing a figure here would
// make this a second place the defaults live, and the first one to drift.
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
func summarise(name string, slots []functionSlot, monthlyCents int) (bool, error) {
	fmt.Printf("\n  %s\n", name)
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

	if monthlyCents == 0 {
		fmt.Printf("\n  This is the free slice — %s/month.\n", euros(0))
	} else {
		fmt.Printf("\n  %s/month.\n", euros(monthlyCents))
	}

	question := fmt.Sprintf("Create %q at %s/month?", name, euros(monthlyCents))
	var ok bool
	if err := survey.AskOne(&survey.Confirm{Message: question, Default: false}, &ok); err != nil {
		return false, err
	}
	return ok, nil
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
	cfg, chosen, slots, ok, err := runPrompts(name)
	if err != nil || !ok {
		return err
	}

	if billingMonths < 1 {
		billingMonths = 1
	}
	monthlyCents, err := priceOf(cfg, billingMonths)
	if err != nil {
		return err
	}

	proceed, err := summarise(chosen, slots, monthlyCents)
	if err != nil {
		return err
	}
	if !proceed {
		fmt.Println("Nothing was created.")
		return nil
	}

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

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
