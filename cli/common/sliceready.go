package common

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// WaitForSliceReady blocks until a slice answers, or gives up after 60s.
//
// A slice's record exists the moment create returns and the components behind it
// do not, so the documented next command — a deploy — is issued into a gap where
// something is not listening yet. Every 5xx renders as the maintenance message,
// which tells the reader to wait for Drift when what they are waiting for is
// their own slice.
//
// It lives here rather than beside the deploy because both the slice commands
// and the project commands need it, and a helper only one of them can reach is
// what makes the other import it.
func WaitForSliceReady(name string) error {
	deadline := time.Now().Add(60 * time.Second)
	u := APIBaseURL + "/ops/slice/status?name=" + url.QueryEscape(name)
	for time.Now().Before(deadline) {
		resp, err := DoJSONRequest(http.MethodGet, u, nil)
		if err == nil {
			body, cerr := CheckResponse(resp, "slice status")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional; its failure does not affect correctness here.
			if cerr == nil {
				var s struct {
					Ready bool `json:"ready"`
				}
				if jerr := json.Unmarshal(body, &s); jerr == nil && s.Ready {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("slice %q not ready after 60s", name)
}
