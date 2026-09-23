package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// SliceEntry is one slice in the caller's account, as returned by
// /ops/slice/list. Exported so the `drift portal` TUI can reuse the fetch.
//
// MonthlyCostCents is the price stored on the slice, fixed when it was created
// or last resized. It is read, never recomputed from the config: the stored
// figure is the one being billed, and a live re-price would disagree with it
// the moment unit prices move.
type SliceEntry struct {
	Name             string `json:"name"`
	Tier             string `json:"tier"`
	MonthlyCostCents int    `json:"monthly_cost_cents"`

	// Config is the slice's declared shape, decoded only as far as the tenant's
	// own labels.
	//
	// A NESTED STRUCT rather than a field of its own, because `labels` is not a
	// field of its own: it lives INSIDE the config, where everything the tenant
	// declares lives. Flattening it here would make this the one place the two
	// shapes disagree, and the flattened spelling would decode to nothing with
	// the endpoint answering perfectly.
	//
	// Everything else in the config is deliberately absent. This file renders a
	// list, and a mirror that decoded the whole shape would be a second
	// definition of it — see cmd/project/config_wire.go, which is the read-only
	// one that exists on purpose and says why.
	Config struct {
		Labels map[string]string `json:"labels"`
	} `json:"config"`
}

// FetchSlices returns the caller's slices. Data-only (no printing) so both the
// `slice list` command and the portal dashboard can share it.
func FetchSlices() ([]SliceEntry, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/list", nil)
	if err != nil {
		return nil, common.TransportError("list slices", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "list slices")
	if err != nil {
		return nil, err
	}

	var slices []SliceEntry
	if err := json.Unmarshal(body, &slices); err != nil {
		return nil, fmt.Errorf("Couldn't list slices: the API response didn't look right (%w)", err)
	}
	return slices, nil
}

// TierLabel renders a tier for display: hacker reads as "free", everything else
// as "configured". Shared by `slice list` and the portal.
func TierLabel(tier string) string {
	if tier == "hacker" {
		return "free"
	}
	return "configured"
}

// PriceLabel renders what a slice costs per month. A free slice reads as
// "free"; a configured one reads as its stored price.
//
// Without a stored price it falls back to TierLabel rather than printing
// "EUR 0.00/mo" — zero here means the figure was never recorded, and rendering
// that as a price would state the slice is free, which is a billing claim the
// CLI has no basis to make.
func PriceLabel(tier string, cents int) string {
	if tier == "hacker" || cents <= 0 {
		return TierLabel(tier)
	}
	return fmt.Sprintf("EUR %d.%02d/mo", cents/100, cents%100)
}

// LabelsLine renders a slice's labels as one `key=value` line, or "" when it has
// none.
//
// SORTED BY KEY, because Go randomises map iteration and a list that prints the
// same slice's labels in a different order on every invocation cannot be diffed,
// scanned or piped. The platform sorts for the same reason wherever it names one
// of these sets in a refusal.
//
// Values are printed RAW. These are not Kubernetes labels and never reach
// cluster metadata, so `client=bakkerij & zn.` is a legal value and quoting or
// escaping it here would show the tenant something they did not type.
func LabelsLine(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, "  ")
}

func getListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all your slices",
		Example: "  drift slice list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			slices, err := FetchSlices()
			if err != nil {
				return err
			}
			active := common.GetActiveSlice()
			for _, s := range slices {
				marker := "  "
				if s.Name == active {
					marker = "* "
				}
				fmt.Printf("%s%-20s %s\n", marker, s.Name, PriceLabel(s.Tier, s.MonthlyCostCents))
				// On its own line, and only when there are any. Labels are free
				// text up to 256 bytes each and a slice may carry 32 of them, so
				// appending them to the row above would wrap the one column a
				// reader scans — the slice name — out of alignment.
				if line := LabelsLine(s.Config.Labels); line != "" {
					fmt.Printf("  %-20s %s\n", "", line)
				}
			}
			return nil
		},
	}
}
