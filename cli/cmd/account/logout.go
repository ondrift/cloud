package account

// logout.go — `drift account logout`: end the session on this machine.
//
// # What it removes
//
// The current account's profile in ~/.drift/session.json. That profile holds the
// account's access token and its refresh token, and nothing else on disk holds
// either, so removing it leaves no credential for the account on this machine.
// The file keeps the other accounts it holds: logging out of one must not log
// out the rest, and no flag does. The last account out removes the file itself.
//
// # What it leaves
//
// It does not contact the platform, so an unreachable platform cannot make it
// fail. It leaves ~/.drift/device_id, which names this machine and not an
// account: the next login binds its refresh token to the same id.
//
// A DRIFT_TOKEN in the environment is not a session. It cannot be unset from
// here, so when one is set the output says commands keep acting as that token.
//
// # When there is nothing to log out of
//
// That is not an error, so a script can run it unconditionally. A file that
// cannot be read is removed, and the output says so: every other command treats
// it as "not logged in", and the command meant to clear the session must not be
// the one it strands. Credentials the CLI cannot read are credentials it cannot
// use, so nothing is lost.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func GetLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out of the current account on this machine",
		Long: "Log out of the current account on this machine.\n\n" +
			"Removes that account's stored session: its access token and its refresh\n" +
			"token. It does not contact the platform. Other accounts you are logged in\n" +
			"to stay logged in, and the device id that names this machine is left alone.\n\n" +
			"Logging out when nobody is logged in is not an error.\n\n" +
			"A token in " + common.TokenEnv + " is not a session: unset the variable to stop\n" +
			"using it.",
		Example:      "  drift account logout",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return logout(cmd.OutOrStdout())
		},
	}
}

// logout ends the current account's session and says what it did.
func logout(out io.Writer) error {
	names, current, err := common.Accounts()

	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(out, "Nobody is logged in on this machine.")

	case err != nil:
		if err := endSession(); err != nil {
			return err
		}
		fmt.Fprintln(out, "The session file on this machine could not be read, so it was removed. Nobody is logged in.")

	case len(names) == 0:
		// A file with the right shape and no account in it is a session in name
		// only; removing it keeps "no session" a missing file.
		if err := endSession(); err != nil {
			return err
		}
		fmt.Fprintln(out, "Nobody is logged in on this machine.")

	case !slices.Contains(names, current):
		// DRIFT_ACCOUNT names a profile that is not here. Ending a session here
		// would end one nobody asked about, so nothing is touched.
		fmt.Fprintf(out, "The account this command acts as is not logged in on this machine. Logged in: %s.\n",
			strings.Join(names, ", "))

	default:
		if err := endSession(); err != nil {
			return err
		}
		fmt.Fprintf(out, "Logged out of %s on this machine.\n", current)
		if left, _, _ := common.Accounts(); len(left) > 0 {
			fmt.Fprintf(out, "Still logged in: %s.\n", strings.Join(left, ", "))
		}
	}

	if common.PersonalAccessToken() != "" {
		fmt.Fprintf(out, "%s is set, so commands in this shell still act as that token. Unset it to stop.\n",
			common.TokenEnv)
	}
	return nil
}

// endSession removes the current account from the stored session.
func endSession() error {
	if err := common.ClearSession(); err != nil {
		return fmt.Errorf("couldn't log out: %w", err)
	}
	return nil
}
