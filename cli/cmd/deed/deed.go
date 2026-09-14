package deed

import "github.com/spf13/cobra"

// GetCmd returns the "deed" command group — Drift's fourth pillar, identity,
// a peer of Backbone/Atomic/Canvas rather than a Backbone primitive. Deed runs
// on its own listener/port in the slice.
//
// KeyAuth and JWT stay SDK-only and have no commands here. They are
// challenge/response protocols an application drives, with nothing at rest to
// look at: there is no record a support engineer could be shown. Vault, Link and
// Pocket each keep state, and those are the three a ticket asks about.
//
// EVERY COMMAND HERE IS A READ. Deed's whole point is that it verifies rather
// than decides — a device joins a registry on the strength of a signature from a
// device already in it, never on an operator's say-so. A `drift deed link add`
// would be exactly the authority this pillar exists to not have, so the write
// side stays where it belongs: with the customer's own signing device. The one
// exception is restore, which is snapshot machinery rather than a command.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deed",
		Short:   "Interact with your slice's Deed (identity) pillar",
		Example: "  drift deed status\n  drift deed vault list\n  drift deed link get alice\n  drift deed pocket list alice",
		GroupID: "services",
	}

	cmd.AddCommand(statusCmd(), vaultCmd(), linkCmd(), pocketCmd())
	return cmd
}
