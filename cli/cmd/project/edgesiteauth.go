// edgesiteauth.go — `edge.site_auth`, the site-auth gate in front of Canvas
// paths.
//
// This CLI does not apply it yet. A file that declares it is refused before
// anything is deployed: a gate the file states and the slice does not hold
// leaves the site open while the file says it is closed.
package project

import (
	"fmt"
	"io"

	"github.com/ondrift/cloud/cli/common"
)

// preflightEdgeSiteAuth runs before the first deploy step, so a file this CLI
// cannot apply in full is refused before any of it is applied.
func preflightEdgeSiteAuth(m *Manifest) error {
	if !m.Slice().Has("edge", "site_auth") {
		return nil
	}
	return fmt.Errorf("edge.site_auth is not supported by this CLI yet")
}

// applyEdgeSiteAuth reconciles `edge.site_auth`. Absent, it does nothing.
// The preflight has already refused a file that declares it.
func applyEdgeSiteAuth(m *Manifest) error {
	return preflightEdgeSiteAuth(m)
}

// planEdgeSiteAuth is the `--plan` section for `edge.site_auth`.
func planEdgeSiteAuth(m *Manifest, out io.Writer) {
	if !m.Slice().Has("edge", "site_auth") {
		return
	}
	fmt.Fprintf(out, "\n  %s edge.site_auth is not supported by this CLI yet\n", common.Hint("!"))
}
