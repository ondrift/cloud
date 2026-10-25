package project

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fxevmSizing is what the slice measured for the testnode: the rpc Pulse peaks
// far below its 64MiB booking, the sequencer Field far above its, the bench
// Pulse is booked as recommended, and the reset Pulse was never called.
func fxevmSizing() []sizingRow {
	return []sizingRow{
		{Function: "post:rpc", Measurements: 1204, PeakBytes: 9 * mib, BookedBytes: 64 * mib, Recommended: 32 * mib},
		{Function: "field:sequencer", Measurements: 40, PeakBytes: 90 * mib, BookedBytes: 64 * mib, Recommended: 128 * mib},
		{Function: "get:bench", Measurements: 10, PeakBytes: 20 * mib, BookedBytes: 32 * mib, Recommended: 32 * mib},
		{Function: "post:admin/reset", Measurements: 0, BookedBytes: 32 * mib, Recommended: 8 * mib},
	}
}

// fixtureDir is the fxevm testnode: its Driftfile and the folders it names.
func fixtureDir(t *testing.T) string {
	t.Helper()
	return fxevmFixture
}

// fxevmFixture is absolute and resolved at package start, before any test
// changes directory.
var fxevmFixture, _ = filepath.Abs(filepath.Join("testdata", "fxevm"))

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func lines(b []byte) []string { return strings.Split(string(b), "\n") }

// THE property of the command: the edit is the `memory` lines it recommends
// changing and nothing else. Every comment, blank line and other attribute of
// the file is still there, in place. Asserted on the lines themselves.
func TestSize_ChangesOnlyTheMemoryLinesItRecommends(t *testing.T) {
	src := mustRead(t, filepath.Join(fixtureDir(t), "Driftfile"))
	res, err := ResizeDriftfile(src, fxevmSizing())
	if err != nil {
		t.Fatal(err)
	}

	var changed []string
	for _, c := range res.Changes {
		changed = append(changed, c.Address)
	}
	if got := strings.Join(changed, ", "); got != "pulse POST /rpc, field sequencer" {
		t.Fatalf("changed %q, want the rpc Pulse and the sequencer Field", got)
	}

	was, now := lines(src), lines(res.Output)
	if len(was) != len(now) {
		t.Fatalf("the edit changed the number of lines: %d → %d", len(was), len(now))
	}
	var diff []string
	for i := range was {
		if was[i] != now[i] {
			diff = append(diff, now[i])
			if !strings.Contains(was[i], "memory") || !strings.Contains(now[i], "memory") {
				t.Errorf("line %d changed and is not a memory line:\n- %s\n+ %s", i+1, was[i], now[i])
			}
		}
	}
	if len(diff) != 2 || !strings.Contains(diff[0], `"32MiB"`) || !strings.Contains(diff[1], `"128MiB"`) {
		t.Errorf("the changed lines are %q, want the rpc at 32MiB and the sequencer at 128MiB", diff)
	}
	for _, kept := range []string{"# JSON-RPC multiplexes on a body field", "# The chain's single writer.", "      concurrent = true"} {
		if !strings.Contains(string(res.Output), kept) {
			t.Errorf("%q did not survive the edit", kept)
		}
	}
	// What it left alone, and why.
	if len(res.Left) != 1 || !strings.Contains(res.Left[0], "pulse POST /admin/reset: never invoked") {
		t.Errorf("left = %q, want the never-invoked Pulse named with its reason", res.Left)
	}
	// The edited file still compiles.
	withRealSchema(t)
	dir := t.TempDir()
	if err := copyTree(fixtureDir(t), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), res.Output, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := CompileDriftfile(filepath.Join(dir, "Driftfile"))
	if err != nil {
		t.Fatalf("the edited file does not compile: %v", err)
	}
	booked := map[string]int{}
	for _, f := range c.Shape.Atomic.Functions {
		booked[f.Name] = f.MemoryBytes
	}
	if booked["post:rpc"] != 32*mib || booked["field:sequencer"] != 128*mib || booked["get:bench"] != 32*mib {
		t.Errorf("the compiled bookings are %v", booked)
	}
}

// A booking that is not a plain size string is somebody's expression, and this
// does not guess where its figure lives. A trailing comment on a booking it
// does rewrite stays on its line.
func TestSize_LeavesAnExpressionAloneAndKeepsATrailingComment(t *testing.T) {
	src := []byte(`variable "m" { default = "16MiB" }
slice "demo" { billing = "monthly" }
atomic {
  element "api" {
    pulse "GET /a" {
      handler = "A"
      memory  = var.m
    }
    pulse "GET /b" {
      handler = "B"
      memory  = "64MiB" # measured on 3 July
    }
  }
}
`)
	res, err := ResizeDriftfile(src, []sizingRow{
		{Function: "get:a", Measurements: 5, Recommended: 32 * mib},
		{Function: "get:b", Measurements: 5, PeakBytes: 4 * mib, Recommended: 16 * mib},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	if !strings.Contains(out, "memory  = var.m") {
		t.Errorf("an expression was rewritten:\n%s", out)
	}
	if !strings.Contains(out, `memory  = "16MiB" # measured on 3 July`) {
		t.Errorf("the booking was not rewritten with its comment kept:\n%s", out)
	}
	if len(res.Left) != 1 || !strings.Contains(res.Left[0], "pulse GET /a: its memory is not a plain size string") {
		t.Errorf("left = %q", res.Left)
	}
}

// sizingStub answers the one read `drift size` makes, with the rows, and
// records everything it was asked.
func sizingStub(t *testing.T, rows []sizingRow) *recorder {
	t.Helper()
	type fn struct {
		Function     string `json:"function"`
		Measurements int64  `json:"measurements"`
		Peak         int64  `json:"peak_rss_bytes"`
		Booked       int64  `json:"booked_bytes"`
		Recommended  int64  `json:"recommended_booking_bytes"`
	}
	var body struct {
		Step      int64 `json:"booking_step_bytes"`
		Min       int64 `json:"min_booking_bytes"`
		Functions []fn  `json:"functions"`
	}
	body.Step, body.Min = 8*mib, 8*mib
	for _, r := range rows {
		body.Functions = append(body.Functions, fn{r.Function, r.Measurements, r.PeakBytes, r.BookedBytes, r.Recommended})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/atomic/sizing" {
			_, _ = w.Write(raw)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// sizeIn runs `drift size` in a copy of the fixture, whose Driftfile is src
// when given.
func sizeIn(t *testing.T, src []byte, args ...string) (dir, out string, err error) {
	t.Helper()
	dir = t.TempDir()
	if err := copyTree(fixtureDir(t), dir); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		if err := os.WriteFile(filepath.Join(dir, "Driftfile"), src, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("DRIFT_SLICE", "")
	var buf bytes.Buffer
	cmd := GetSizeCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return dir, buf.String(), err
}

// The command writes the recommendation into the file and shows it as a
// diff; the only thing it asks the platform is the measurement. --dry-run shows
// the same diff and leaves the file as it was.
func TestSize_WritesTheFileAndAsksOnlyForTheMeasurement(t *testing.T) {
	rec := sizingStub(t, fxevmSizing())
	original := mustRead(t, filepath.Join(fixtureDir(t), "Driftfile"))

	dir, out, err := sizeIn(t, nil, "--dry-run")
	if err != nil {
		t.Fatalf("size --dry-run: %v", err)
	}
	for _, want := range []string{
		"~ pulse POST /rpc", "memory 64MiB → 32MiB", "peak 9MB over 1204 calls",
		"~ field sequencer", "memory 64MiB → 128MiB",
		"-      memory     = \"64MiB\"", "+      memory     = \"32MiB\"", "nothing written (--dry-run)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say %q:\n%s", want, out)
		}
	}
	if got := mustRead(t, filepath.Join(dir, "Driftfile")); !bytes.Equal(got, original) {
		t.Error("--dry-run changed the Driftfile")
	}

	dir, out, err = sizeIn(t, nil)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	written := mustRead(t, filepath.Join(dir, "Driftfile"))
	if bytes.Equal(written, original) || !strings.Contains(string(written), `memory     = "32MiB"`) {
		t.Errorf("size wrote nothing, or not the recommendation:\n%s", written)
	}
	if !strings.Contains(out, "updated") || !strings.Contains(out, "drift plan") {
		t.Errorf("size does not say it wrote, or what to do next:\n%s", out)
	}
	for _, hit := range rec.hits {
		if !strings.HasPrefix(hit, "GET ") {
			t.Errorf("`drift size` made %q; it reads the measurement and writes a file", hit)
		}
	}
	if rec.count("GET /ops/atomic/sizing") == 0 {
		t.Error("`drift size` never asked for a measurement, so what it wrote came from nowhere")
	}
}

// A YAML Driftfile is not edited: it is refused as every reader refuses one,
// before the platform is asked anything, and the file is as it was.
func TestSize_RefusesAYAMLDriftfile(t *testing.T) {
	rec := sizingStub(t, fxevmSizing())
	yamlText := []byte("slice: fxevm-testnode\natomic:\n  elements: []\n")

	dir, _, err := sizeIn(t, yamlText)
	if err == nil || !strings.Contains(err.Error(), "the Driftfile is HCL now") {
		t.Fatalf("a YAML Driftfile was not refused as YAML: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "Driftfile")); !bytes.Equal(got, yamlText) {
		t.Error("the YAML Driftfile was changed")
	}
	if len(rec.hits) != 0 {
		t.Errorf("a refused file still reached the platform: %v", rec.hits)
	}
}

// With nothing measured there is nothing to write, and it says so rather than
// reporting an agreement.
func TestSize_SaysWhenNothingHasBeenMeasured(t *testing.T) {
	sizingStub(t, []sizingRow{{Function: "post:rpc", Measurements: 0, Recommended: 8 * mib}})
	original := mustRead(t, filepath.Join(fixtureDir(t), "Driftfile"))

	dir, out, err := sizeIn(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing to size from") {
		t.Errorf("an unmeasured slice was not said to be unmeasured:\n%s", out)
	}
	if got := mustRead(t, filepath.Join(dir, "Driftfile")); !bytes.Equal(got, original) {
		t.Error("the file changed with nothing measured")
	}
}
