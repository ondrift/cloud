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

func getUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <name>",
		Short:   "Set the active slice for subsequent commands",
		Example: "  drift slice use my-slice",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate the slice exists before saving, and REFUSE if it does not.
			//
			// This used to warn and set it anyway, which quietly broke every
			// command that followed: `atomic list` answered "No functions
			// deployed." — because the api really does return 200 with an empty
			// array for a slice that is not there — and `backbone` reported a
			// slice not found. One typo, and the tool reads as an empty slice, a
			// missing slice and a working slice depending on which subcommand you
			// happen to run next.
			//
			// A name that cannot work is worth one refusal now rather than a
			// confusing answer every time. The names are already in hand from the
			// lookup, so listing them costs nothing and turns "does not exist"
			// into "here is what you meant".
			//
			// If the lookup itself fails the name is saved unvalidated, exactly as
			// before: being offline is not a reason to refuse to configure a CLI,
			// and this check is a convenience rather than a security boundary.
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/list", nil)
			if err == nil {
				defer resp.Body.Close()
				var slices []struct {
					Name string `json:"name"`
				}
				if json.NewDecoder(resp.Body).Decode(&slices) == nil {
					found := false
					names := make([]string, 0, len(slices))
					for _, s := range slices {
						names = append(names, s.Name)
						if s.Name == name {
							found = true
						}
					}
					if !found {
						fmt.Print(unknownSliceMessage(name, names))
						return
					}
				}
			}

			if err := common.SaveActiveSlice(name); err != nil {
				fmt.Println("Failed to set active slice:", err)
				return
			}
			fmt.Printf("Active slice set to '%s'.\n", name)
		},
	}
}

// unknownSliceMessage names the slice that was asked for and every one that
// exists, so the next command is a correction rather than another guess.
//
// The no-slices case is called out separately because it is a different problem
// with a different fix: nothing is misspelled, there is simply nothing to select
// yet, and saying "your slices are:" followed by nothing reads as a bug.
func unknownSliceMessage(name string, existing []string) string {
	if len(existing) == 0 {
		return fmt.Sprintf("No slice named '%s' — and this account has no slices yet.\n"+
			"Create one with 'drift slice create <name>' or 'drift file apply'.\n", name)
	}
	sort.Strings(existing)
	var b strings.Builder
	fmt.Fprintf(&b, "No slice named '%s'. This account has:\n", name)
	for _, s := range existing {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	fmt.Fprintf(&b, "The active slice is unchanged.\n")
	return b.String()
}
