package project

// api.go is the thin client for the slice endpoints an apply reads. Every
// helper here calls one HTTP endpoint and returns the parsed response. The CLI
// is intentionally self-contained — no drift-common dependency — so wire shapes
// are mirrored locally, in config_wire.go.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"
)

// LiveSlice is a CLI-local mirror of the fields we actually use from
// the platform's models.Slice. The wire endpoint returns more fields
// (createdAt, billing, provisioning) but we only need name + config.
type LiveSlice struct {
	Name             string      `json:"name"`
	Tier             string      `json:"tier"`
	Config           SliceConfig `json:"config"`
	MonthlyCostCents int         `json:"monthly_cost_cents"`
}

// FetchLiveSlice GETs /ops/slice/get?name=<name>. Returns nil if the
// slice doesn't exist (404), error for any other failure.
func FetchLiveSlice(name string) (*LiveSlice, error) {
	u := common.APIBaseURL + "/ops/slice/get?name=" + url.QueryEscape(name)
	resp, err := common.DoJSONRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, common.TransportError("fetch slice", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := common.CheckResponse(resp, "fetch slice")
	if err != nil {
		return nil, err
	}
	var s LiveSlice
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("decode slice: %w", err)
	}
	return &s, nil
}

// LineItem mirrors the platform's core/common/plan.LineItem wire shape —
// one priced (or informational, UnitCents==0) row in a price breakdown.
// Everything in this file READS. Nothing here prices a slice, creates one or
// changes its shape, and nothing should be added that does: the configurator is
// the only writer of a slice's shape, and a second one on this side is the thing
// the split removed. What the CLI does to a slice that already exists — deploy
// into it, read its status, apply its resources — is all this client is for.

// readResponseBody is a small helper used by callers that want raw bytes.
func readResponseBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
