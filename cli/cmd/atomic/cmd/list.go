package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

type atomicRecord struct {
	Id           string `json:"_id"`
	Name         string `json:"name"`
	FunctionName string `json:"function_name"`
	Method       string `json:"method"`
	// Route is what the slot was RESERVED for, written when the slice was sold
	// the slot rather than when something was deployed into it. A reserved slot
	// carries a route and no function name; a deployed one carries both.
	Route     string `json:"route,omitempty"`
	Element   string `json:"element"`
	Language  string `json:"language"`
	CreatedAt string `json:"created_at"`
}

// deployed reports whether anything has been deployed into this slot. The
// function name is written by the deploy, so its absence is what distinguishes a
// slot the tenant has bought from one they are using.
func (r atomicRecord) deployed() bool { return r.FunctionName != "" }

// route is the path the slot answers on, from whichever field holds it. A
// deployed slot's function name IS its route; a reserved one has only Route.
func (r atomicRecord) route() string {
	if r.FunctionName != "" {
		return r.FunctionName
	}
	return r.Route
}

func fetchSlots() ([]atomicRecord, error) {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/atomic/list",
		nil,
	)
	if err != nil {
		return nil, common.TransportError("list atomic functions", err)
	}
	defer resp.Body.Close()

	b, err := common.CheckResponse(resp, "list atomic functions")
	if err != nil {
		return nil, err
	}

	var records []atomicRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("Couldn't list atomic functions: the API response didn't look right (%w)", err)
	}

	// Keep every slot the tenant HAS: deployed ones, and reserved ones the slice
	// was sold and nothing has filled yet.
	//
	// Filtering on the function name alone drops the reserved set, because the
	// name is written by the deploy. That was right when the only nameless record
	// was the anonymous pre-warmed spare — which is not a slot anybody bought and
	// is still excluded here, by having no route either.
	var slots []atomicRecord
	for _, r := range records {
		if r.FunctionName != "" || r.Route != "" {
			slots = append(slots, r)
		}
	}
	return slots, nil
}

func langOrDefault(r atomicRecord) string {
	// A reserved slot has no language: nothing has been deployed into it, and
	// "native" is the api's historical label for Go, so passing it through would
	// label every slot a tenant bought as a Go function.
	if !r.deployed() {
		return "—"
	}
	switch r.Language {
	// "native" is the historical label for Go and the api still returns it for
	// anything deployed before the rename; "" is a very old record with none.
	case "", "native", "go":
		return "go"
	case "python":
		return "python"
	case "node":
		return "node"
	default:
		return r.Language
	}
}

// deployedAt is what the last column says. A reserved slot has no deploy time,
// and an empty cell there reads as missing data rather than as a slot waiting to
// be filled.
func deployedAt(r atomicRecord) string {
	if !r.deployed() {
		return "reserved"
	}
	return r.CreatedAt
}

func printFlatTable(records []atomicRecord) {
	fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", "ID", "LANG", "METHOD", "ROUTE", "Deployed At")
	fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", "------------", "--------", "--------", "------------------------", "-------------------")
	for _, r := range records {
		// Org-only routing: the route is the bare function name; the element is
		// a grouping concept (shown in the grouped view), never a path segment.
		fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", r.Id, langOrDefault(r), r.Method, r.route(), deployedAt(r))
	}
}

func List() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all deployed atomic functions",
		Example: "  drift atomic list",
		GroupID: "operations",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			deployed, err := fetchSlots()
			if err != nil {
				fmt.Println(err)
				return
			}
			if len(deployed) == 0 {
				fmt.Println("No functions deployed.")
				return
			}

			// Check if any function belongs to an element.
			hasElements := false
			for _, r := range deployed {
				if r.Element != "" {
					hasElements = true
					break
				}
			}

			if !hasElements {
				printFlatTable(deployed)
				return
			}

			// Group by element; collect ungrouped separately.
			byElement := make(map[string][]atomicRecord)
			var ungrouped []atomicRecord
			for _, r := range deployed {
				if r.Element == "" {
					ungrouped = append(ungrouped, r)
				} else {
					byElement[r.Element] = append(byElement[r.Element], r)
				}
			}

			elementNames := make([]string, 0, len(byElement))
			for name := range byElement {
				elementNames = append(elementNames, name)
			}
			sort.Strings(elementNames)

			header := fmt.Sprintf("%-12s  %-8s  %-8s  %-24s  %s", "ID", "LANG", "METHOD", "ROUTE", "Deployed At")
			rule := fmt.Sprintf("%-12s  %-8s  %-8s  %-24s  %s", "------------", "--------", "--------", "------------------------", "-------------------")

			for _, name := range elementNames {
				fmt.Printf("\n  element: %s\n", name)
				fmt.Printf("  %s\n", header)
				fmt.Printf("  %s\n", rule)
				for _, r := range byElement[name] {
					fmt.Printf("  %-12s  %-8s  %-8s  %-24s  %s\n", r.Id, langOrDefault(r), r.Method, r.route(), deployedAt(r))
				}
			}

			if len(ungrouped) > 0 {
				fmt.Printf("\n  (no element)\n")
				fmt.Printf("  %s\n", header)
				fmt.Printf("  %s\n", rule)
				for _, r := range ungrouped {
					fmt.Printf("  %-12s  %-8s  %-8s  %-24s  %s\n", r.Id, langOrDefault(r), r.Method, r.route(), deployedAt(r))
				}
			}
			fmt.Println()
		},
	}
}
