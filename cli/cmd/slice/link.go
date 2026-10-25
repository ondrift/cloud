// link.go — `drift slice link {add, list, remove}`, and the client calls behind
// them.
//
// Slice-to-slice linking: grant the ACTIVE slice permission to call another of
// your slices (e.g. an app slice → your own observability slice). The operator
// records the link on both ends and re-renders both: the caller reaches the
// target through its slice runtime, over an in-cluster path, and a function
// sees only a local URL for it. Nothing about the link widens the caller's
// egress to the public internet.
//
// A link carries no identity the target can trust. The X-Drift-Slice header a
// linked call arrives with is asserted by the caller, so the target
// authenticates a linked call exactly as it would any other.
//
// Same-user only (the target must be a slice you own). This widens to
// "same Drift Team" when Teams land — the CLI surface won't change.
//
// `links` in the Driftfile's edge block is the declarative way: `drift apply`
// adds the links it names and removes the ones it does not, through the same
// calls exported here.
package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// Link is one link as the platform reports it.
type Link struct {
	Target     string `json:"target"`
	TargetHost string `json:"target_host"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// sliceHeaders names the slice a link call acts on. Empty leaves it to the
// active slice, which every request carries by default.
func sliceHeaders(sliceName string, extra map[string]string) map[string]string {
	h := map[string]string{}
	for k, v := range extra {
		h[k] = v
	}
	if sliceName != "" {
		h["X-Slice"] = sliceName
	}
	return h
}

// normaliseLinkTarget is the one spelling of a target: the platform stores and
// compares names lowercased.
func normaliseLinkTarget(target string) string {
	return strings.ToLower(strings.TrimSpace(target))
}

// AddLink links sliceName to target. A target that is not a slice of the same
// owner is refused by the platform, and its message is the returned error.
func AddLink(sliceName, target string) (Link, error) {
	target = normaliseLinkTarget(target)
	op := fmt.Sprintf("link to %q", target)
	body, err := json.Marshal(map[string]string{"target": target})
	if err != nil {
		return Link{}, fmt.Errorf("Couldn't %s: %w", op, err) //nolint:staticcheck // ST1005: user-facing copy.
	}
	resp, err := common.DoRequestWithHeaders(http.MethodPost,
		common.APIBaseURL+"/ops/slice/link", strings.NewReader(string(body)),
		sliceHeaders(sliceName, map[string]string{"Content-Type": "application/json"}))
	if err != nil {
		return Link{}, common.TransportError(op, err)
	}
	defer resp.Body.Close()
	respBody, err := common.CheckResponse(resp, op)
	if err != nil {
		return Link{}, err
	}
	l := Link{Target: target}
	_ = json.Unmarshal(respBody, &l)
	return l, nil
}

// ListLinks returns the slices sliceName is linked to. A reply that cannot be
// read is an error rather than an empty list: a reconcile reading it as "no
// links" would add every declared one again and remove none.
func ListLinks(sliceName string) ([]Link, error) {
	resp, err := common.DoRequestWithHeaders(http.MethodGet,
		common.APIBaseURL+"/ops/slice/link", nil, sliceHeaders(sliceName, nil))
	if err != nil {
		return nil, common.TransportError("list links", err)
	}
	defer resp.Body.Close()
	respBody, err := common.CheckResponse(resp, "list links")
	if err != nil {
		return nil, err
	}
	var links []Link
	if err := json.Unmarshal(respBody, &links); err != nil {
		return nil, fmt.Errorf("Couldn't list links: the platform sent a reply we couldn't read") //nolint:staticcheck // ST1005: user-facing copy.
	}
	return links, nil
}

// RemoveLink removes sliceName's link to target.
func RemoveLink(sliceName, target string) error {
	target = normaliseLinkTarget(target)
	op := fmt.Sprintf("remove the link to %q", target)
	u := common.APIBaseURL + "/ops/slice/link?target=" + url.QueryEscape(target)
	resp, err := common.DoRequestWithHeaders(http.MethodDelete, u, nil, sliceHeaders(sliceName, nil))
	if err != nil {
		return common.TransportError(op, err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, op)
	return err
}

func getLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link the active slice to another of your slices so it can call it",
		Long: "Grant the active slice permission to reach another slice you own " +
			"(e.g. an app → your own observability slice). The call travels " +
			"in-cluster through the slice runtime; it never crosses the public " +
			"internet, and everything else stays subject to your egress posture.\n\n" +
			"Links are declared as `links` in its edge block: `drift apply` " +
			"adds the links it names and removes the ones it does not. `list` shows " +
			"what is live; `add` and `remove` are deprecated.",
		Example: "  drift slice link list",
	}
	cmd.AddCommand(getLinkAddCmd(), getLinkListCmd(), getLinkRemoveCmd())
	return cmd
}

func getLinkAddCmd() *cobra.Command {
	return common.DeprecateCommand(&cobra.Command{
		Use:   "add <target-slice>",
		Short: "Deprecated: declare the link in the Driftfile's edge block instead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}
			l, err := AddLink(active, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Linked → %s\n", l.Target)
			fmt.Fprintf(out, "  Call it from your functions:\n")
			fmt.Fprintf(out, "    drift.Edge.Slice(%q).Post(\"/api/...\", body)\n", l.Target)
			fmt.Fprintf(out, "  (an in-cluster path through the slice runtime; %s authenticates the call like any other)\n", l.Target)
			return nil
		},
	}, common.Deprecation{
		Old:     "drift slice link add",
		New:     "drift apply",
		Because: "List the target in `links` in the Driftfile's edge block; apply adds the links the file names.",
	})
}

func getLinkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the slices the active slice is linked to",
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}
			links, err := ListLinks(active)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(links) == 0 {
				fmt.Fprintln(out, "This slice isn't linked to any other slice.")
				return nil
			}
			fmt.Fprintf(out, "%-30s  %s\n", "TARGET", "HOST")
			for _, l := range links {
				fmt.Fprintf(out, "%-30s  %s\n", l.Target, l.TargetHost)
			}
			return nil
		},
	}
}

func getLinkRemoveCmd() *cobra.Command {
	return common.DeprecateCommand(&cobra.Command{
		Use:   "remove <target-slice>",
		Short: "Deprecated: delete the link from the Driftfile's edge block instead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}
			target := normaliseLinkTarget(args[0])
			if err := RemoveLink(active, target); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unlinked → %s\n", target)
			return nil
		},
	}, common.Deprecation{
		Old:     "drift slice link remove",
		New:     "drift apply",
		Because: "Delete the target from `links` in the Driftfile's edge block; apply removes the links the file does not name.",
	})
}
