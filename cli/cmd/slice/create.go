package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	project "github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// getCreateCmd builds `drift slice create [name]`.
//
// Default flow: hands off to the configurator in the browser. The CLI mints a
// single-use session, opens the URL, and polls until the user submits — the
// configurator forwards the create to the api under the user's own token, so
// the slice is created by the same authenticated call the CLI would have made.
// A positional name pre-fills the form; without one the form collects it.
//
// Headless flow (--free / --headless): skips the browser entirely
// and creates a free Hacker slice directly. The name is required
// in headless mode because there is no form to collect it from.
// This path is the only one that works in CI, scripts, and SSH
// sessions.
//
// Driftfile flow (--from <path>): the slice is born at the shape the
// manifest declares, which is what `drift file apply` would have
// provisioned had the slice not existed. Without this, the two-command
// path (`slice create` then `project deploy`) starts from a shape nobody
// declared — the fixed free preset, which is larger than any honest first
// Driftfile — while the one-command path starts from the manifest. Same
// project, two different slices, depending only on which command was run
// first. See HQ CLI-STANDARDUSAGE-T17KKN.
func getCreateCmd() *cobra.Command {
	var (
		headless      bool
		free          bool
		fromPath      string
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice (opens the configurator in your browser, or --from a Driftfile)",
		Example: "  drift slice create my-slice            # opens the configurator\n" +
			"  drift slice create my-slice --free      # free Hacker slice, no browser\n" +
			"  drift slice create --from Driftfile     # born at the manifest's shape\n" +
			"  drift slice create my-slice --headless  # alias for --free (CI/scripts)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// --headless is an alias for --free (backward compat).
			if headless {
				free = true
			}

			if fromPath != "" {
				if name != "" {
					return fmt.Errorf("cannot pass <name> when --from is set; the slice name comes from the Driftfile")
				}
				if free {
					return fmt.Errorf("cannot combine --from with --free; the Driftfile's declared shape decides whether the slice is free")
				}
				return createFromDriftfile(fromPath, autoYes, billingMonths)
			}

			// Free tier: create directly, no configurator.
			if free {
				if name == "" {
					return fmt.Errorf("--free requires a slice name argument")
				}
				return createHeadless(name)
			}

			// Default: the browser configurator, the same handoff `resize`
			// uses — one session flow, one place it can break.
			result, err := runBrowserHandoff("create slice", name, modeCreate, nil)
			if err != nil {
				return err
			}
			printSliceSummary("created", result)
			// The configurator returns the api's Slice document, so the name
			// is known even when the form collected it rather than the CLI.
			if created := sliceNameFrom(result); created != "" {
				if serr := common.SaveActiveSlice(created); serr != nil {
					fmt.Println("Warning: couldn't mark the new slice as active —", serr)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&free, "free", false, "Create a free Hacker slice without opening the browser")
	cmd.Flags().BoolVar(&headless, "headless", false, "Alias for --free (CI/scripts)")
	cmd.Flags().StringVar(&fromPath, "from", "", "Create the slice at the shape a Driftfile declares (name comes from the Driftfile)")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months, for a configured (non-free) slice")
	_ = cmd.Flags().MarkHidden("headless")
	return cmd
}

// createFromDriftfile provisions a slice at the shape its Driftfile
// declares. It is the create-time twin of `slice resize --from Driftfile`
// and shares that command's whole pipeline — parse, translate, price,
// classify — so the cost the user confirms here is computed by the same
// server-side pricing call that `project deploy` would have used.
//
// There is deliberately no shape decision of its own: the tier follows the
// price — free when the manifest costs nothing, configured otherwise.
//
// This is the ONLY path that still builds a slice's shape from a manifest.
// `drift file apply` no longer does: it refuses against a slice that does not
// exist and names the configurator, because the configurator owns what a slice
// IS and this file owns what runs on it.
func createFromDriftfile(path string, autoYes bool, billingMonths int) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}

	m, err := project.ParseDriftfile(abs)
	if err != nil {
		return err
	}

	manifestCfg, err := project.ManifestToSliceConfig(m)
	if err != nil {
		return err
	}
	wantedCost, wantedItems, err := project.PriceConfig(manifestCfg)
	if err != nil {
		return fmt.Errorf("price target config: %w", err)
	}

	// Refuse rather than silently no-op or resize: this command creates.
	live, err := project.FetchLiveSlice(m.Name())
	if err != nil {
		return fmt.Errorf("fetch slice: %w", err)
	}
	if live != nil {
		return fmt.Errorf("slice %q already exists — use `drift file apply` to deploy into it, or `drift slice resize --from %s` to change its shape", m.Name(), path)
	}

	d := project.Diff(m.Name(), manifestCfg, nil, "", 0, wantedCost)
	d.WantedItems = wantedItems

	fmt.Println()
	fmt.Println(project.RenderDiff(d))
	if !confirmYesNo(autoYes, "Apply?") {
		return fmt.Errorf("aborted by user")
	}

	// Tier follows price — identical rule to cmd/project's create path.
	tier := "custom"
	if wantedCost == 0 {
		tier = project.TierHacker
	}

	fmt.Printf("\n  Creating slice %q...\n", m.Name())
	if err := project.CreateSlice(m.Name(), tier, manifestCfg, billingMonths); err != nil {
		return err
	}
	// Wait for provisioning, exactly as `project deploy`'s own create path
	// does. The whole point of this command is that a `project deploy` follows
	// it, and without the wait that deploy races the slice coming up and fails
	// with "runner unreachable" — a platform-fault message for what is really
	// just impatience. Observed on alpha before this was added.
	if err := project.WaitForSliceReady(m.Name()); err != nil {
		return fmt.Errorf("slice %q was created but did not become ready: %w", m.Name(), err)
	}

	if err := common.SaveActiveSlice(m.Name()); err != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", err)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", m.Name())
	return nil
}

// createHeadless posts directly to api/ops/slice/create with the free
// tier. Kept for non-interactive use (CI, scripts, SSH sessions). For
// configured (paid) slices, use the browser flow (the default, no
// --free/--headless).
func createHeadless(name string) error {
	body, _ := json.Marshal(map[string]string{
		"name": name,
		"tier": "hacker",
	})

	resp, err := common.DoJSONRequest(
		http.MethodPost,
		common.APIBaseURL+"/ops/slice/create",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()

	if _, err := common.CheckResponse(resp, "create slice"); err != nil {
		return err
	}

	// Wait for the slice to answer, exactly as the --from path above does.
	// The record exists the moment create returns, but the components behind
	// it do not, and this command's whole purpose is that a deploy follows it
	// — the documented opening of every demo is create, use, deploy, run back
	// to back. A deploy issued into that gap is refused by a component that is
	// not listening yet, and the CLI renders any 5xx as the maintenance
	// message, so the reader is told to wait for Drift when what they are
	// waiting for is their own slice.
	//
	// The slice is created either way, so a timeout is reported as "not ready
	// yet" rather than as a failure: a create retried against an existing name
	// gets a 409, which is a worse place to leave someone.
	fmt.Printf("  Waiting for slice %q to come up...\n", name)
	if err := project.WaitForSliceReady(name); err != nil {
		if serr := common.SaveActiveSlice(name); serr != nil {
			fmt.Println("Warning: couldn't mark the new slice as active —", serr)
		}
		return fmt.Errorf("slice %q was created and is still starting: %w", name, err)
	}

	if err := common.SaveActiveSlice(name); err != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", err)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", name)
	return nil
}
