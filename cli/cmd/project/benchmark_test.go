package project

import (
	"bytes"
	"strings"
	"testing"
)

const mib = 1024 * 1024

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

// The report points at the command that writes a booking where it lives, the
// Driftfile, and at what shows its price.
func TestRenderSizing_PointsAtDriftSize(t *testing.T) {
	var out strings.Builder
	renderSizing(&out, []sizingRow{{
		Function: "get:ping", Measurements: 10, PeakBytes: 1 << 20,
		BookedBytes: 32 << 20, Recommended: 16 << 20,
	}})
	if !strings.Contains(out.String(), "`drift size` writes these into the Driftfile") ||
		!strings.Contains(out.String(), "`drift plan`") {
		t.Errorf("the report should name `drift size` and `drift plan`:\n%s", out.String())
	}
	if strings.Contains(out.String(), "drift slice resize") {
		t.Errorf("the report sends people to the resize form, which the next `drift apply` would undo:\n%s", out.String())
	}
}

// benchmark reports and buys nothing: it has no flag that would.
func TestBenchmark_ReportsAndHasNoFlagThatBuys(t *testing.T) {
	cmd := getBenchmarkCmd()
	for _, gone := range []string{"apply", "write"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s is still a flag of `drift file benchmark`; a booking is written by `drift size`", gone)
		}
	}
}
