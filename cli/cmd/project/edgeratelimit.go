// edgeratelimit.go — `edge.rate_limit`, the slice's requests per minute.
//
// This CLI does not apply it yet, and says so rather than refusing. The key is
// where the retired `atomic.rate_limit` lands, which the slice has always
// ignored in favour of the rate limit set on the slice form, so an old file
// keeps applying exactly as it did.
package project

import (
	"fmt"
	"io"
	"os"

	"github.com/ondrift/cloud/cli/common"
)

// edgeRateLimitNotApplied is the notice for a file that declares a rate limit.
const edgeRateLimitNotApplied = "edge.rate_limit is not applied by this CLI yet; " +
	"the slice keeps its current rate limit (drift slice resize sets it)."

// preflightEdgeRateLimit runs before the first deploy step. Nothing about a
// declared rate limit stops this CLI applying the rest of the file.
func preflightEdgeRateLimit(*Manifest) error { return nil }

// applyEdgeRateLimit reconciles `edge.rate_limit`. Absent, it does nothing;
// present, it says the value is not applied and lets the apply continue.
func applyEdgeRateLimit(m *Manifest) error {
	reportEdgeRateLimit(m, os.Stdout)
	return nil
}

// reportEdgeRateLimit writes the notice when the file declares a rate limit.
func reportEdgeRateLimit(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "rate_limit") {
		return
	}
	fmt.Fprintf(out, "  %s %s\n", common.Hint("·"), edgeRateLimitNotApplied)
}

// planEdgeRateLimit is the `--plan` section for `edge.rate_limit`.
func planEdgeRateLimit(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "rate_limit") {
		return
	}
	fmt.Fprintf(out, "\n  %s %s\n", common.Hint("!"), edgeRateLimitNotApplied)
}
