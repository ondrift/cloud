// edgelinks.go — `edge.links`, the slices of the same owner this slice may
// call or front.
//
// This CLI does not apply it yet. A file that declares it is refused rather
// than applied without it: `edge.links` is the truth when present, and a run
// that left the live links as they were would contradict it in silence.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// applyEdgeLinks reconciles `edge.links`. Absent, it does nothing, and the
// live links are left alone.
func applyEdgeLinks(m *Manifest) error {
	if !m.Slice().Has("edge", "links") {
		return nil
	}
	return fmt.Errorf("edge.links is not supported by this CLI yet")
}

// planEdgeLinks is the `--plan` section for `edge.links`.
func planEdgeLinks(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "links") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.links is not supported by this CLI yet\n", common.Hint("!"))
}
