package backbone

import (
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// GetCmd returns the "backbone" command group, which exposes the Drift
// backbone primitives (secrets, queues, blobs) directly from the CLI.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backbone",
		Short:   "Interact with your slice's Backbone primitives",
		Example: "  drift backbone secret list\n  drift backbone queue push jobs '{\"task\":\"build\"}'\n  drift backbone blob put assets logo.png ./logo.png\n  drift backbone cache set session-token abc123 --ttl 3600",
		GroupID: "services",

		// EVERY VERB HERE ADDRESSES ONE SLICE'S BACKBONE, and none of them can
		// be answered without knowing which slice. Only `status` asked; the
		// other seven sent the request with no X-Slice header at all, and the
		// api then defaulted the slice name to the literal string "default".
		//
		// That guess fails in two ways and the quieter one is worse:
		//
		//   - the account owns no slice called "default" → the lookup 404s and
		//     the caller is told `slice not found: default`, naming a slice
		//     they never mentioned, when what happened is that they never
		//     selected one;
		//   - the account DOES own one called "default" → the command quietly
		//     succeeds against a slice the caller never named. Nothing reports
		//     anything, and a write lands somewhere unintended.
		//
		// Checked here rather than in eight RunEs so a verb added later cannot
		// forget. A child that defines its own PersistentPreRun would SHADOW
		// this one — cobra runs the closest it finds and stops — which is what
		// the test in this package guards.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			return err
		},
	}

	cmd.AddCommand(secretCmd(), blobCmd(), queueCmd(), lockCmd(), nosqlCmd(), sqlCmd(), cacheCmd(), statusCmd())
	return cmd
}
