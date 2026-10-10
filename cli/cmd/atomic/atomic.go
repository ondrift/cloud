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
		Use:   "atomic",
		Short: "Manage Atomic deployments: pulses and fields",
		Long: "Atomic runs your code on the slice, as two kinds of work.\n\n" +
			"Pulses: Atomic's functions, one call, one result. Fields: long-running workers.\n\n" +
			"Both are declared in the Driftfile, as `pulse` and `field` blocks inside the `element` " +
			"block their handler lives in (`element \"default\"` is ./atomic itself, any other name " +
			"is ./atomic/<name>), each with the memory it books as a slot on the slice. " +
			"`drift file explain element.pulse` describes the block.",
		Example: "  drift atomic list\n  drift atomic deploy ./atomic\n  drift atomic run ./atomic\n" +
			"  drift atomic fields\n  drift atomic new",
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
		cmd.Fields(),
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
