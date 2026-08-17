// quota.go — the limits a local container runs under.
//
// `drift file run` boots the project in Docker with no control plane in the
// path, and a container given no DRIFT_QUOTA_CONFIG runs every limit unlimited:
// a collection or bucket springs into existence on first write, while the same
// write against the real slice is refused with 400. The local run passed, the
// deploy did not, and nothing on either side said why.
//
// So the container is handed the referenced slice's OWN envelope, fetched once
// and forwarded UNPARSED. The platform produces that document in exactly one
// place and keeps it in lockstep with the slice's parser; decoding it here to
// rebuild it would make the CLI a second encoder of a wire contract, and the
// parts a client-side mirror cannot express — the per-queue depths, Deed's caps,
// and the declaration flag that decides whether an empty set means "allow
// everything" or "refuse everything" — would go missing without a sound.
package project

import (
	"fmt"
	"io"
	"net/http"

	"github.com/ondrift/cloud/cli/common"
)

// localQuotaConfig resolves the limits to hand a local run, degrading to an
// unlimited container rather than failing.
//
// `drift file run` is the verb that needs no account and no network, and that
// stays true: a slice that cannot be read costs one stated line, not the run.
// An unenforced local run that says it is unenforced is honest; the silent one
// is the defect this exists to remove.
func localQuotaConfig(slice string, out io.Writer) string {
	body, err := fetchSliceQuotaConfig(slice)
	if err != nil {
		fmt.Fprintf(out, "  %s running without %s's limits: %v\n",
			common.Hint("·"), slice, err)
		fmt.Fprintf(out, "     writes this slice would refuse are accepted here — collections, buckets and databases it does not declare\n")
		return ""
	}
	return body
}

// fetchSliceQuotaConfig returns the tier-limits envelope the named slice runs
// under, as the platform's own bytes.
//
// X-Slice is set explicitly rather than left to the session's active slice. The
// manifest names the slice being run, and "the slice this machine last used" is
// a different question — one the orphan check answered wrongly for its whole
// life before an apply from a fresh session surfaced it.
func fetchSliceQuotaConfig(slice string) (string, error) {
	resp, err := common.DoRequestWithHeaders(http.MethodGet,
		common.APIBaseURL+"/ops/slice/quota", nil,
		map[string]string{"X-Slice": slice})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "read the slice's limits")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
