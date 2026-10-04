// edgeallow.go — `edge.allow`, the inbound CIDR allowlist.
//
// This CLI does not apply it yet. A file that declares it is refused before
// anything is deployed: an allowlist the file states and the slice does not
// hold leaves the slice open to every client while the file says otherwise.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// preflightEdgeAllow runs before the first deploy step, so a file this CLI
// cannot apply in full is refused before any of it is applied.
func preflightEdgeAllow(m *Manifest) error {
	if !m.Slice().Has("edge", "allow") {
		return nil
	}
	return fmt.Errorf("edge.allow is not supported by this CLI yet")
}

// applyEdgeAllow reconciles `edge.allow`. Absent, it does nothing. The
// preflight has already refused a file that declares it.
func applyEdgeAllow(m *Manifest) error {
	return preflightEdgeAllow(m)
}

// planEdgeAllow is the `--plan` section for `edge.allow`.
func planEdgeAllow(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "allow") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.allow is not supported by this CLI yet\n", common.Hint("!"))
}
