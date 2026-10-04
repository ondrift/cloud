// edgeauth.go — `edge.auth`, the site-auth gate in front of Canvas paths.
//
// This CLI does not apply it yet. A file that declares it is refused rather
// than applied without it: a gate the file states and the slice does not hold
// leaves the site open while the file says it is closed.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// applyEdgeAuth reconciles `edge.auth`. Absent, it does nothing.
func applyEdgeAuth(m *Manifest) error {
	if !m.Slice().Has("edge", "auth") {
		return nil
	}
	return fmt.Errorf("edge.auth is not supported by this CLI yet")
}

// planEdgeAuth is the `--plan` section for `edge.auth`.
func planEdgeAuth(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "auth") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.auth is not supported by this CLI yet\n", common.Hint("!"))
}
