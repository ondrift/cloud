// edgeratelimit.go — `edge.rate_limit`, the slice's requests per minute.
//
// This CLI does not apply it yet. A file that declares it is refused rather
// than applied without it: a limit the file states and the slice does not hold
// is a control that reads as present and is not.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// applyEdgeRateLimit reconciles `edge.rate_limit`. Absent, it does nothing.
func applyEdgeRateLimit(m *Manifest) error {
	if !m.Slice().Has("edge", "rate_limit") {
		return nil
	}
	return fmt.Errorf("edge.rate_limit is not supported by this CLI yet")
}

// planEdgeRateLimit is the `--plan` section for `edge.rate_limit`.
func planEdgeRateLimit(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "rate_limit") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.rate_limit is not supported by this CLI yet\n", common.Hint("!"))
}
