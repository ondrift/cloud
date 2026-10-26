package atomic

import (
	"github.com/spf13/cobra"

	cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd"
	cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
	cmd_run "github.com/ondrift/cloud/cli/cmd/atomic/cmd/run"
	"github.com/ondrift/cloud/cli/cmd/project"
)

// A function's declaration lives in the Driftfile, so the local run needs the
// manifest parser. `drift apply` is what ships a function. This package is the one that
// may reach for it: `cmd/project` imports the atomic DEPLOY package, never this
// one, so the arrow still points one way.

func GetCmd() *cobra.Command {
	atomicCmd := &cobra.Command{
		Use:   "atomic",
		Short: "Manage Atomic deployments: functions and workers",
		Long: "Atomic runs your code on the slice, as two kinds of work.\n\n" +
			"Functions: one call, one result. Workers: long-running processes the slice keeps running.\n\n" +
			"Both are declared in the Driftfile, as `function` and `worker` blocks inside the `backend` " +
			"block their handler lives in (`backend \"default\"` is ./atomic itself, any other name " +
			"is ./atomic/<name>), each with the memory it books as a slot on the slice. " +
			"`drift file explain backend.function` describes the block.",
		Example: "  drift apply\n  drift atomic list\n  drift atomic run ./atomic\n" +
			"  drift atomic workers\n  drift atomic new",
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
		cmd_run.Run(project.FunctionSpecsInDir),
		cmd.Auth(),
		cmd.Delete(),
		cmd.Backend(),
		cmd.Workers(),
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
