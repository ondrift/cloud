package project

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

const mib = 1024 * 1024

// liveConfig decodes a slice config the way FetchSliceConfigRaw hands one over:
// generic JSON, not a struct. The overlay has to work on that, because the CLI
// forwards the document whole and never learns its shape.
func liveConfig(t *testing.T, src string) any {
	t.Helper()
	var cfg any
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return cfg
}

// --apply sets each measured function's booking on the live config.
//
// Matching is on the booking key — `method:route` — which is what the sizing
// endpoint reports and what the runtime looks a pool up by.
func TestOverlayBookings_SetsTheMeasuredRecommendation(t *testing.T) {
	cfg := liveConfig(t, `{"atomic":{"functions":[
		{"name":"get:ping","memory_bytes":33554432},
		{"name":"post:report","memory_bytes":33554432}]}}`)

	n := overlayBookings(cfg, map[string]int64{
		"get:ping":    8 * mib,
		"post:report": 128 * mib,
	})
	if n != 2 {
		t.Fatalf("changed %d bookings, want 2", n)
	}

	fns := cfg.(map[string]any)["atomic"].(map[string]any)["functions"].([]any)
	for i, want := range []int64{8 * mib, 128 * mib} {
		got := fns[i].(map[string]any)["memory_bytes"]
		if toBytes(got) != want {
			t.Errorf("function %d books %v, want %d", i, got, want)
		}
	}
}

// The config is forwarded whole, so everything the CLI has no name for has to
// survive the substitution untouched. This is the analogue of the comments a
// Driftfile rewrite had to preserve: the parts this code does not understand are
// exactly the parts it must not damage.
func TestOverlayBookings_LeavesEveryOtherFieldAlone(t *testing.T) {
	const src = `{"atomic":{"max_number_of_functions":9,"functions":[
		{"name":"get:ping","memory_bytes":33554432,"some_future_key":"keep me"}]},
		"backbone":{"nosql":{"collections":{"orders":52428800}}},
		"a_field_this_cli_has_never_heard_of":{"nested":true}}`
	cfg := liveConfig(t, src)

	if n := overlayBookings(cfg, map[string]int64{"get:ping": 16 * mib}); n != 1 {
		t.Fatalf("changed %d bookings, want 1", n)
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{
		`"max_number_of_functions":9`,
		`"some_future_key":"keep me"`,
		`"orders":52428800`,
		`"a_field_this_cli_has_never_heard_of"`,
	} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("%s did not survive the overlay:\n%s", keep, out)
		}
	}
}

// A booking already at its recommendation is not a change, so a second --apply
// on an unchanged slice opens nothing rather than buying a no-op resize.
//
// JSON numbers decode as float64, so this also pins the comparison: an int64
// compared against the decoded value directly is never equal, and every run
// would report a change and open the browser.
func TestOverlayBookings_AnAlreadyCorrectBookingIsNotAChange(t *testing.T) {
	cfg := liveConfig(t, `{"atomic":{"functions":[{"name":"get:ping","memory_bytes":16777216}]}}`)

	if n := overlayBookings(cfg, map[string]int64{"get:ping": 16 * mib}); n != 0 {
		t.Errorf("changed %d bookings, want 0 — the value already matched", n)
	}
}

// A function the slice has never run is left alone. Its "recommendation" is the
// floor — the absence of a measurement, not advice — so pre-filling it would look
// like the platform had sized the function when nothing had.
func TestOverlayBookings_SkipsFunctionsWithNoMeasurement(t *testing.T) {
	rows := []sizingRow{
		{Function: "get:ping", Measurements: 40, PeakBytes: 7 * mib, Recommended: 16 * mib},
		{Function: "get:cold", Measurements: 0, Recommended: 8 * mib},
	}
	rec := map[string]int64{}
	for _, r := range rows {
		if r.Measurements > 0 && r.Recommended > 0 {
			rec[r.Function] = r.Recommended
		}
	}
	if _, ok := rec["get:cold"]; ok {
		t.Error("an unmeasured function was queued for applying")
	}

	cfg := liveConfig(t, `{"atomic":{"functions":[
		{"name":"get:ping","memory_bytes":33554432},
		{"name":"get:cold","memory_bytes":33554432}]}}`)
	if n := overlayBookings(cfg, rec); n != 1 {
		t.Fatalf("changed %d bookings, want 1", n)
	}

	fns := cfg.(map[string]any)["atomic"].(map[string]any)["functions"].([]any)
	if got := toBytes(fns[1].(map[string]any)["memory_bytes"]); got != 32*mib {
		t.Errorf("the unmeasured function's booking was changed to %d", got)
	}
}

// A config with no function list must not panic or invent one.
func TestOverlayBookings_ToleratesAConfigWithNoFunctions(t *testing.T) {
	for _, src := range []string{
		`{"canvas":{"total_max_size_in_bytes":1}}`,
		`{"atomic":{"max_number_of_functions":5}}`,
		`{"atomic":{"functions":{}}}`,
		`{"atomic":{"functions":[]}}`,
		`"not an object at all"`,
	} {
		cfg := liveConfig(t, src)
		if n := overlayBookings(cfg, map[string]int64{"get:ping": 8 * mib}); n != 0 {
			t.Errorf("changed %d bookings in a config with no function list: %s", n, src)
		}
	}
}

func toBytes(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return -1
	}
}

// The table has to distinguish "measured and cheap" from "never called". Both
// render small, and only one is advice.
func TestRenderSizing_SaysWhenNothingWasMeasured(t *testing.T) {
	var buf bytes.Buffer
	renderSizing(&buf, "lab", []sizingRow{
		{Function: "get:ping", Measurements: 40, PeakBytes: 7 * mib, BookedBytes: 32 * mib, Recommended: 16 * mib},
		{Function: "get:cold", Measurements: 0, BookedBytes: 32 * mib, Recommended: 8 * mib,
			Note: "never invoked — nothing measured"},
	})
	out := buf.String()

	if !strings.Contains(out, "never invoked") {
		t.Errorf("an unmeasured function renders as though it were measured:\n%s", out)
	}
	// The measured row shows its call count; the unmeasured one shows a dash
	// rather than a zero, which would read as a measurement of zero.
	if !strings.Contains(out, "40") {
		t.Errorf("the measurement count is missing:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("an absent figure should render as a dash, not a number:\n%s", out)
	}
	if !strings.Contains(out, "7MB") || !strings.Contains(out, "16MB") {
		t.Errorf("peak and recommendation are missing:\n%s", out)
	}
}

// The report points at where a booking is actually set.
//
// It used to close with "Apply these with --write", which is the wrong
// instruction: `atomic.functions[].memory` is deprecated-and-ignored, so writing
// it changes the file and not the slice. A report that tells someone to run a
// command which does not do what they want is worse than one that says nothing.
func TestRenderSizing_PointsAtTheConfiguratorRatherThanAtWrite(t *testing.T) {
	var out strings.Builder
	renderSizing(&out, "lab", []sizingRow{{
		Function: "get:ping", Measurements: 10, PeakBytes: 1 << 20,
		BookedBytes: 32 << 20, Recommended: 16 << 20,
	}})

	// The command with THIS slice already in it. A measurement is only useful
	// where it can be applied, and the reader should be one command from
	// applying it rather than one search.
	if !strings.Contains(out.String(), "drift slice resize lab") {
		t.Errorf("the report should name the command that applies these bookings:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Apply these with --write") {
		t.Errorf("the report still sends people to --write, which no longer resizes "+
			"anything:\n%s", out.String())
	}
}

// --write is a thin alias onto --apply and says so once.
//
// The thing it used to write to ceased to exist, so there is no behaviour left
// to keep working — only a name people have in their fingers. It forwards rather
// than refusing because the handoff discloses the price and the restart before
// anything is bought, which is what made the original "an alias must not spend
// money on muscle memory" objection moot.
func TestBenchmarkWrite_IsAnAliasOntoApply(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices strings.Builder
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	cmd := getBenchmarkCmd()
	if err := cmd.Flags().Set("write", "true"); err != nil {
		t.Fatal(err)
	}
	common.DeprecateFlag(cmd, "write", common.Deprecation{
		Old:     "drift file benchmark --write",
		New:     "drift file benchmark --apply",
		Because: "test probe",
	})()

	said := notices.String()
	if !strings.Contains(said, "--write") {
		t.Errorf("passing --write said nothing about the rename:\n%s", said)
	}
	if !strings.Contains(said, "--apply") {
		t.Errorf("the notice must name what to type instead:\n%s", said)
	}
}

// ...and says nothing when the flag is left alone, so an ordinary report is
// not decorated with a notice about a flag nobody passed.
func TestBenchmarkWrite_IsSilentWithoutTheFlag(t *testing.T) {
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)
	var notices strings.Builder
	restore := common.RedirectDeprecationWarnings(&notices)
	defer restore()

	cmd := getBenchmarkCmd()
	common.DeprecateFlag(cmd, "write", common.Deprecation{
		Old:     "drift file benchmark --write",
		Because: "test probe",
	})()

	if notices.Len() != 0 {
		t.Errorf("a run that never passed --write was warned anyway:\n%s", notices.String())
	}
}

// Both flags exist, and --apply is the one the help describes as the action.
func TestBenchmark_HasApplyAndKeepsWriteWorking(t *testing.T) {
	cmd := getBenchmarkCmd()
	if cmd.Flags().Lookup("apply") == nil {
		t.Fatal("--apply is missing")
	}
	w := cmd.Flags().Lookup("write")
	if w == nil {
		t.Fatal("--write was removed rather than deprecated — a name people have in their fingers")
	}
	if !strings.Contains(strings.ToLower(w.Usage), "deprecated") {
		t.Errorf("--write's help does not say it is deprecated, got %q", w.Usage)
	}
}
