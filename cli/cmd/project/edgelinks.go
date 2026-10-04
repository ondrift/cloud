// edgelinks.go — `edge.links`, the slices of the same owner this slice may
// call or front.
//
// This CLI does not apply it yet. A file that declares it is refused before
// anything is deployed: `edge.links` is the truth when present, and a run that
// left the live links as they were would contradict it in silence.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// preflightEdgeLinks runs before the first deploy step, so a file this CLI
// cannot apply in full is refused before any of it is applied.
func preflightEdgeLinks(m *Manifest) error {
	if !m.Slice().Has("edge", "links") {
		return nil
	}
	return fmt.Errorf("edge.links is not supported by this CLI yet")
}

// applyEdgeLinks reconciles `edge.links`. Absent, it does nothing, and the
// live links are left alone. The preflight has already refused a file that
// declares it.
func applyEdgeLinks(m *Manifest) error {
	return preflightEdgeLinks(m)
}

// planEdgeLinks is the `--plan` section for `edge.links`.
func planEdgeLinks(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "links") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.links is not supported by this CLI yet\n", common.Hint("!"))
}
