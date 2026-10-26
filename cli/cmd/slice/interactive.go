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

	"github.com/ondrift/cloud/cli/common"

	"golang.org/x/term"
)

// interactive.go — creating a slice without leaving the terminal.
//
// The terminal is the whole surface for buying a shape. A tool whose first act
// is to open a web page has handed the job to something the user did not
// choose, and every failure of that page becomes a failure of this command.
//
// What is asked is what the platform prices per unit and cannot guess: the
// name, how many function slots, and each slot's route and booking. Everything
// else has a defensible default the server already owns, so asking would be
// theatre — `drift slice resize` changes any of it afterwards.
//
// The price is NOT computed here. It comes from POST /ops/slice/price, because
// a client shipping its own rates is a second source of truth that disagrees
// the day a unit price moves.

// The bounds the platform enforces, restated here only to refuse a value before
// a round trip. Every one of them is checked again server-side, which is where
// they are actually owned — these exist so a typo is answered by the prompt that
// caused it rather than by a 400 three questions later.
const (
	bytesPerMiB      = 1024 * 1024
	minMemoryMiB     = 8
	maxMemoryMiB     = 256
	compiledFloorMiB = 16
)

// MaxFunctionSlots is the most Functions and Fields one slice may declare. The
// form stops adding slots here, and a Driftfile declaring more is refused when
// it is compiled.
const MaxFunctionSlots = 64

// httpMethods is every method a slot can be booked under: the HTTP verbs a
// function answers, `queue` for a function a queue drives, and `field` for a field,
// which nothing calls and whose route is its name.
var httpMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", "queue", "field"}

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

// A queue name is NOT a path, and the platform says so in as many words. Its
// own rule is tier.bookingQueuePattern; this is the same expression, so the
// refusal happens on the row being typed rather than after the slice is priced,
// summarised and posted.
var queueNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

func validQueueName(v any) error {
	s := strings.TrimSpace(fmt.Sprint(v))
	if !queueNameRe.MatchString(s) {
		return fmt.Errorf("lowercase letters, digits and interior hyphens only, up to 32 — " +
			"a queue name is not a path, so no slashes and no underscores")
	}
	return nil
}

// A field's name is its whole identity, booked as `field:<name>`, and the
// Driftfile's `atomic.fields[].name` pattern is the rule; this is the same
// expression, so the refusal happens on the row being typed.
var fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validFieldName(v any) error {
	s := strings.TrimSpace(fmt.Sprint(v))
	if !fieldNameRe.MatchString(s) {
		return fmt.Errorf("a lowercase letter, then lowercase letters, digits and hyphens, up to 63; " +
			"a field's name is not a path")
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
// the tenant typed is gone.
//
// Only what was asked is set. Every other limit is left absent so the platform
// fills it from its own defaults — writing a figure here would make this a
// second place those defaults live, and the first one to drift.
func buildConfig(storageMiB int, slots []functionSlot, bb backboneShape, canvasMiB int, scalars map[string]int) map[string]any {
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
		// MaxDatabases is a ceiling the platform enforces and does not derive
		// from the list beside it, so it is stated from what was declared. Left
		// at zero it is filled from the free preset and the last database is
		// refused — after the shape has been priced and paid for.
		backbone["sql"] = map[string]any{
			"MaxDatabases": len(bb.Databases),
			"databases":    bytesOf(bb.Databases),
		}
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

	// The dials, each written at the whole dotted path it names.
	//
	// A dial's path is absolute from the top of the config and names its pillar
	// first — `atomic.MaxNumberOfScheduledJobs`, `backbone.secrets.MaxCount`,
	// `deed.vault.MaxEntriesPerUID` — because a dial belongs to whichever pillar
	// the config keeps it under, and three of the four have some. Depth varies
	// with it: `backbone.BackupRetentionDays` sits on the pillar itself while
	// `backbone.secrets.MaxCount` sits one deeper.
	//
	// So the path is followed to its end rather than split once. Anchoring every
	// dial to one pillar puts the other pillars' dials inside a struct that has
	// no field for them, and encoding/json drops an unknown key without an
	// error — the dial would be priced, agreed to, and silently never applied.
	// Written last so a dial merges into a pillar the shape already built.
	for path, v := range scalars {
		setPath(cfg, strings.Split(path, "."), v)
	}
	return cfg
}

// setPath writes v at a dotted path, creating the objects along the way and
// merging into any that are already there.
func setPath(root map[string]any, path []string, v int) {
	if len(path) == 0 {
		return
	}
	cur := root
	for _, key := range path[:len(path)-1] {
		next, _ := cur[key].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
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
	atomic := map[string]any{"functions": functions}
	if len(functions) > 0 {
		// The slot ceiling, stated from the list rather than left to a default.
		// It is a separate limit from the list it bounds, so a shape declaring
		// six functions and no ceiling is capped at the free preset's five and
		// refuses the sixth deploy. Declaring none states no ceiling at all — a
		// canvas-only slice means it, and an explicit zero would be a limit this
		// form invented.
		atomic["MaxNumberOfFunctions"] = len(functions)
	}
	return map[string]any{"atomic": atomic}
}

// sectionOf maps a priced line to the pillar that owns it, so the bill adds up
// the same way the form is laid out.
//
// The keys are the server's (tier.PriceConfig): atomic_scheduled,
// atomic_memory, atomic_storage, bb_storage, realtime_connections,
// canvas_storage. Anything unrecognised is attributed to nothing rather than
// guessed at — a line in the wrong section is worse than a line in no section,
// because the totals would still add up and only the attribution would lie.
func sectionOf(key string) string {
	switch {
	case strings.HasPrefix(key, "atomic"):
		return "Atomic"
	case strings.HasPrefix(key, "bb_"), strings.HasPrefix(key, "realtime"),
		strings.HasPrefix(key, "sql"):
		return "Backbone"
	case strings.HasPrefix(key, "canvas"):
		return "Canvas"
	}
	return ""
}

// priceBySection asks the same endpoint and splits the answer per pillar.
func priceBySection(cfg map[string]any) (int, map[string]int, error) {
	body, _ := json.Marshal(map[string]any{"config": cfg, "billing_period_months": 1})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/price", bytes.NewBuffer(body))
	if err != nil {
		return 0, nil, common.TransportError("price slice", err)
	}
	defer resp.Body.Close()
	data, err := common.CheckResponse(resp, "price slice")
	if err != nil {
		return 0, nil, err
	}
	var priced struct {
		MonthlyCents int `json:"monthly_cents"`
		Items        []struct {
			Key          string `json:"key"`
			SubtotalCent int    `json:"subtotal_cents"`
		} `json:"items"`
	}
	if uerr := json.Unmarshal(data, &priced); uerr != nil {
		return 0, nil, fmt.Errorf("the price endpoint answered something this version cannot read: %w", uerr)
	}
	per := map[string]int{}
	for _, it := range priced.Items {
		if sec := sectionOf(it.Key); sec != "" {
			per[sec] += it.SubtotalCent
		}
	}
	return priced.MonthlyCents, per, nil
}

// declaredShape is everything the prompts collected, kept together so the
// summary shows the same thing that was sent rather than a second assembly of
// it.
type declaredShape struct {
	StorageMiB      int
	Slots           []functionSlot
	Backbone        backboneShape
	BackboneScalars map[string]int
	CanvasMiB       int
}
