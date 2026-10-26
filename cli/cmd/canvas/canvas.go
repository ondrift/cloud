package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// GetCmd is what is left of `drift canvas`: nothing, but a refusal that says what
// does the job now. A site is declared in the Driftfile (`site "/" { dir = "..." }`
// inside `canvas`) and `drift apply` uploads its files, so a script still running
// `drift canvas deploy <dir>` fails and is told what to type instead, rather than
// meeting "unknown command" with no way forward. The group is hidden: it has
// nothing to offer anyone who was not already using it.
func GetCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "canvas",
		Short:  "Removed: declare the site in the Driftfile and run drift apply",
		Hidden: true,
		// Any verb and any flag, so `drift canvas deploy ./dist --route /admin` is
		// answered with what replaced it and not with a complaint about --route.
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("there is no `drift canvas`: a site is declared in the Driftfile " +
				"(`canvas { site \"/\" { dir = \"./site\" } }`, a `path` for any mount but the root), " +
				"and `drift apply` uploads its files. `drift plan` shows what it would change")
		},
	}
}
