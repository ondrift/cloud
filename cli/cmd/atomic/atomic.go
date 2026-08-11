package atomic

import (
	"github.com/spf13/cobra"

	cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd"
	cmd_deploy "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
	cmd_run "github.com/ondrift/cloud/cli/cmd/atomic/cmd/run"
	"github.com/ondrift/cloud/cli/cmd/project"
)

// A function's declaration lives in the Driftfile, so the single-folder deploy
// and the local run both need the manifest parser. This package is the one that
// may reach for it: `cmd/project` imports the atomic DEPLOY package, never this
// one, so the arrow still points one way.

func GetCmd() *cobra.Command {
	atomicCmd := &cobra.Command{
		Use:     "atomic",
		Short:   "Manage Atomic deployments",
		Example: "  drift atomic list\n  drift atomic deploy ./my-function\n  drift atomic run ./my-function\n  drift atomic new",
		GroupID: "services",
	}

	atomicCmd.AddGroup(&cobra.Group{
		ID:    "development",
		Title: "Development",
	})

	atomicCmd.AddGroup(&cobra.Group{
		ID:    "operations",
		Title: "Operations",
	})

	atomicCmd.AddCommand(
		cmd_deploy.Deploy(project.FunctionSpecsInDir),
		cmd_run.Run(project.FunctionSpecsInDir),
		cmd.Auth(),
		cmd.Delete(),
		cmd.Element(),
		cmd.List(),
		cmd.Logs(),
		cmd.Metrics(),
		cmd.Redeploy(),
		cmd.Rollback(),
		cmd.History(),
		cmd.Trigger(),
		cmd.Alert(),
		cmd.Egress(),
		cmd.Fetch(),
		cmd_new.New(),
	)

	atomicCmd.GroupID = "services"

	return atomicCmd
}
