// edgehosts.go — `edge.hosts`, fronting: a host this slice owns, served by a
// slice it links.
//
// This CLI does not apply it yet. A file that declares it is refused before
// anything is deployed: a host the file says another slice serves would go on
// being served by this one.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// preflightEdgeHosts runs before the first deploy step, so a file this CLI
// cannot apply in full is refused before any of it is applied.
func preflightEdgeHosts(m *Manifest) error {
	if !m.Slice().Has("edge", "hosts") {
		return nil
	}
	return fmt.Errorf("edge.hosts is not supported by this CLI yet")
}

// applyEdgeHosts reconciles `edge.hosts`. Absent, it does nothing. The
// preflight has already refused a file that declares it.
func applyEdgeHosts(m *Manifest) error {
	return preflightEdgeHosts(m)
}

// planEdgeHosts is the `--plan` section for `edge.hosts`.
func planEdgeHosts(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "hosts") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.hosts is not supported by this CLI yet\n", common.Hint("!"))
}
