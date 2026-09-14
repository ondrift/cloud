package atomic_cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func Element() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "element",
		Short:   "Manage Atomic elements (virtual API groups)",
		Example: "  drift atomic element list",
		GroupID: "operations",
	}
	cmd.AddCommand(elementList())
	return cmd
}

func elementList() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all elements and the functions they contain",
		Example: "  drift atomic element list",
		Args:    cobra.NoArgs,
		// RunE, not Run. A `Run` handler has no error channel, so the only way to
		// report a failure was to print it and return — which exits 0. Every
		// script doing `drift atomic element list && …` carried on as though the
		// listing had succeeded, and `set -e` could not help because nothing
		// failed as far as the shell could see.
		//
		// Returning is also what removes the doubled message: main() prints the
		// error once, to stderr, where an error belongs.
		RunE: func(cmd *cobra.Command, args []string) error {
			deployed, err := fetchSlots()
			if err != nil {
				return err
			}

			byElement := make(map[string][]atomicRecord)
			for _, r := range deployed {
				if r.Element != "" {
					byElement[r.Element] = append(byElement[r.Element], r)
				}
			}

			if len(byElement) == 0 {
				fmt.Println("No elements defined. Deploy with --element <name> to create one.")
				return nil
			}

			names := make([]string, 0, len(byElement))
			for name := range byElement {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				fns := byElement[name]
				fmt.Printf("%s  (%d function", name, len(fns))
				if len(fns) != 1 {
					fmt.Print("s")
				}
				fmt.Println(")")

				for _, r := range fns {
					// Org-only routing: the element groups these functions (it's the
					// section header above) but never appears in the route path.
					route := "/api/" + r.FunctionName
					methods := r.Method
					if methods == "" {
						methods = "?"
					}
					fmt.Printf("  %-8s  %-32s  %s\n", strings.ToUpper(methods), route, r.Id)
				}
				fmt.Println()
			}
			return nil
		},
	}
}
