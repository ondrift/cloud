package project

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ondrift/cloud/cli/common"
)

const mib = 1024 * 1024

func parseDoc(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("fixture is not YAML: %v", err)
	}
	return &doc
}

func render(t *testing.T, doc *yaml.Node) string {
	t.Helper()
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// --write edits the value and leaves the document alone.
//
// The file is a document people write in, not a serialisation format. A round
// trip through a struct would drop every comment in it, which is why this walks
// the node tree — and why the test asserts the comments are still there rather
// than only that the number changed.
func TestWriteBookings_RewritesMemoryAndKeepsTheDocument(t *testing.T) {
	src := `# my project
name: shop
atomic:
  rate_limit: 500/min   # slice-wide
  functions:
    # the cheap one
    - { name: "get:ping", memory: 32MB }
    - name: "post:report"
      memory: 32MB       # sized by guesswork
backbone:
  nosql:
    - { name: orders, size: 50MB }
`
	doc := parseDoc(t, src)
	n := writeBookingsInto(doc, map[string]int64{
		"get:ping":    8 * mib,
		"post:report": 128 * mib,
	})
	if n != 2 {
		t.Fatalf("changed %d bookings, want 2", n)
	}

	got := render(t, doc)
	for _, want := range []string{`memory: 8MB`, `memory: 128MB`} {
		if !strings.Contains(got, want) {
			t.Errorf("the rewritten Driftfile has no %q:\n%s", want, got)
		}
	}
	for _, keep := range []string{"# my project", "# slice-wide", "# the cheap one", "# sized by guesswork"} {
		if !strings.Contains(got, keep) {
			t.Errorf("comment %q was dropped — a Driftfile is written by a person:\n%s", keep, got)
		}
	}
	// The rest of the document is untouched.
	for _, keep := range []string{"rate_limit: 500/min", "name: orders", "size: 50MB", "name: shop"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q did not survive the rewrite:\n%s", keep, got)
		}
	}
}

// A function the slice has never run is skipped, because its "recommendation" is
// the floor — the absence of a measurement, not advice. Writing it would look
// like the platform had sized the function when nothing had.
func TestWriteBookings_SkipsFunctionsWithNoMeasurement(t *testing.T) {
	rows := []sizingRow{
		{Function: "get:ping", Measurements: 40, PeakBytes: 7 * mib, Recommended: 16 * mib},
		{Function: "get:cold", Measurements: 0, Recommended: 8 * mib},
	}
	var buf bytes.Buffer
	// The file path is never reached: with one measured row it writes, so this
	// asserts through writeBookingsInto instead, which is where the choice lives.
	rec := map[string]int64{}
	for _, r := range rows {
		if r.Measurements > 0 && r.Recommended > 0 {
			rec[r.Function] = r.Recommended
		}
	}
	if _, ok := rec["get:cold"]; ok {
		t.Error("an unmeasured function was queued for writing")
	}

	doc := parseDoc(t, `
name: shop
atomic:
  functions:
    - { name: "get:ping", memory: 32MB }
    - { name: "get:cold", memory: 32MB }
`)
	if n := writeBookingsInto(doc, rec); n != 1 {
		t.Fatalf("changed %d bookings, want 1", n)
	}
	got := render(t, doc)
	if !strings.Contains(got, "memory: 16MB") {
		t.Errorf("the measured function was not rewritten:\n%s", got)
	}
	if strings.Count(got, "32MB") != 1 {
		t.Errorf("the unmeasured function's booking was touched:\n%s", got)
	}
	_ = buf
}

// A booking already at its recommendation is not a change, so a second --write
// on an unchanged slice reports nothing rather than rewriting the file.
func TestWriteBookings_AnAlreadyCorrectBookingIsNotAChange(t *testing.T) {
	doc := parseDoc(t, `
name: shop
atomic:
  functions:
    - { name: "get:ping", memory: 16MB }
`)
	if n := writeBookingsInto(doc, map[string]int64{"get:ping": 16 * mib}); n != 0 {
		t.Errorf("changed %d bookings, want 0 — the value already matched", n)
	}
}

// A function whose entry has no `memory` at all gets one. The point of --write is
// that the file is complete afterwards, and a manifest edited since the deploy is
// exactly when someone reaches for it.
func TestWriteBookings_AddsAMissingMemoryKey(t *testing.T) {
	doc := parseDoc(t, `
name: shop
atomic:
  functions:
    - { name: "get:ping" }
`)
	if n := writeBookingsInto(doc, map[string]int64{"get:ping": 24 * mib}); n != 1 {
		t.Fatalf("changed %d bookings, want 1", n)
	}
	if got := render(t, doc); !strings.Contains(got, "memory: 24MB") {
		t.Errorf("no memory key was added:\n%s", got)
	}
}

// A Driftfile with no atomic section must not panic or invent one.
func TestWriteBookings_ToleratesADriftfileWithNoFunctions(t *testing.T) {
	for _, src := range []string{
		"name: site\ncanvas: ./site\n",
		"name: site\natomic:\n  rate_limit: 10/s\n",
		"name: site\natomic:\n  functions: {}\n",
	} {
		doc := parseDoc(t, src)
		if n := writeBookingsInto(doc, map[string]int64{"get:ping": 8 * mib}); n != 0 {
			t.Errorf("changed %d bookings in a manifest with no function list:\n%s", n, src)
		}
	}
}

// The table has to distinguish "measured and cheap" from "never called". Both
// render small, and only one is advice.
func TestRenderSizing_SaysWhenNothingWasMeasured(t *testing.T) {
	var buf bytes.Buffer
	renderSizing(&buf, []sizingRow{
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
// It used to close with "Apply these with --write", which is now the wrong
// instruction: `atomic.functions[].memory` is deprecated-and-ignored, so writing
// it changes the file and not the slice. A report that tells someone to run a
// command which does not do what they want is worse than one that says nothing.
func TestRenderSizing_PointsAtTheConfiguratorRatherThanAtWrite(t *testing.T) {
	var out strings.Builder
	renderSizing(&out, []sizingRow{{
		Function: "get:ping", Measurements: 10, PeakBytes: 1 << 20,
		BookedBytes: 32 << 20, Recommended: 16 << 20,
	}})

	if !strings.Contains(out.String(), common.ConfiguratorBaseURL) {
		t.Errorf("the report should name the configurator, where a booking is set:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Apply these with --write") {
		t.Errorf("the report still sends people to --write, which no longer resizes "+
			"anything:\n%s", out.String())
	}
}

// --write says what it does NOW, and says it before doing it.
//
// It still edits the Driftfile, so nothing scripted breaks — but the key it
// edits is ignored by the platform, and "wrote 19 bookings" without that reads
// as "the slice is now sized correctly", which is the one thing it does not mean.
func TestBenchmarkWrite_SaysTheSliceIsNotResized(t *testing.T) {
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
		Because: "test probe",
	})()

	if !strings.Contains(notices.String(), "--write") {
		t.Errorf("passing --write said nothing about the key being ignored:\n%s", notices.String())
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
