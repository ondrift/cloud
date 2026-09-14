package slate_cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

func Deploy() *cobra.Command {
	var route string

	cmd := &cobra.Command{
		Use:   "deploy [directory]",
		Short: "Deploy a static site from a directory",
		Example: "  drift canvas deploy ./my-site\n" +
			"  drift canvas deploy ./dist --route /admin",
		Args: cobra.ExactArgs(1),
		// RunE, not Run, AND THIS ONE IS A DEPLOY.
		//
		// A `Run` handler has no error channel, so every failure below could only
		// be printed and returned — and that exits 0. `drift canvas deploy ./dist
		// && notify-shipped` announced a site that was never uploaded, and `set
		// -e` could not help because nothing failed as far as the shell could
		// see. Of all the commands carrying this defect, it is the one whose
		// silence is most expensive.
		//
		// Returning is also what stops the message appearing twice: main() prints
		// it once, to stderr.
		RunE: func(cmd *cobra.Command, args []string) error {
			folder := args[0]
			r, err := common.CanonicalRoute(route)
			if err != nil {
				return fmt.Errorf("Couldn't deploy canvas site: %v", err)
			}
			slug, err := common.SlugifyRoute(r)
			if err != nil {
				return fmt.Errorf("Couldn't deploy canvas site: %v", err)
			}
			fmt.Printf("Deploying canvas site from %s → %s\n", folder, r)

			zipData, err := common.ZipFolder(folder)
			if err != nil {
				return fmt.Errorf("Couldn't deploy canvas site: failed to zip folder (%v)", err)
			}

			q := url.Values{}
			q.Set("site", slug)
			q.Set("route", r)
			resp, err := common.DoRequestWithHeaders(
				http.MethodPost,
				common.APIBaseURL+"/ops/canvas?"+q.Encode(),
				zipData,
				map[string]string{
					"Content-Type": "application/zip",
				},
			)
			if err != nil {
				return common.TransportError("deploy canvas site", err)
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "deploy canvas site"); err != nil {
				return err
			}

			fmt.Println("Canvas site deployed.")
			return nil
		},
	}
	cmd.Flags().StringVar(&route, "route", "/", "URL prefix to mount the site at (e.g. /admin)")
	return cmd
}
