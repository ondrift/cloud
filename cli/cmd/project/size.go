package project

// size.go: `drift size`, the measured peaks written back into the Driftfile.
//
// `drift file benchmark` reports what each Function and Worker has cost. The
// Driftfile holds each booking on the block it books, so the recommendation
// belongs there: this edits the `memory` line of every Function and Worker the
// slice recommends a different size for, and shows the change. The edit then
// goes through `drift plan` and apply like any other, so a recommendation is
// never bought without being seen.
//
// # The measurement is benchmark's
//
// The rows come from sizingRows and the choice of which slots have a figure
// worth writing from measuredRecommendations, both shared with benchmark. The
// platform computes the recommendation, headroom and grid included; this
// command writes that figure and derives no second opinion.
//
// # The edit keeps the file
//
// hclwrite edits the tokens of one attribute's value. Comments, blank lines,
// alignment and the order of everything else stay as they were, so the change
// is the `memory` lines and nothing else.
//
// # What it leaves alone
//
// A booking that is not a plain size string (`memory = var.rpc_memory`) is
// not rewritten: the figure lives somewhere else, and this would be guessing
// which place. A slot nothing has invoked has no measurement to size from. Each
// is reported with its reason.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cloud/cli/common"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/cobra"
	"github.com/zclconf/go-cty/cty"
)

// SizeChange is one booking rewritten.
type SizeChange struct {
	Address  string
	From, To int // bytes
	Peak     int64
	Calls    int64
}

// SizeResult is the Driftfile after the edit, and what was and was not changed.
type SizeResult struct {
	Output  []byte
	Changes []SizeChange
	// Left says, for each slot the measurement did not change, why.
	Left []string
}

// ResizeDriftfile rewrites the `memory` of each Function and Worker in src that
// rows recommend a different size for.
func ResizeDriftfile(src []byte, rows []sizingRow) (*SizeResult, error) {
	f, diags := hclwrite.ParseConfig(src, driftfileName, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}
	byKey := map[string]sizingRow{}
	for _, r := range rows {
		byKey[r.Function] = r
	}
	recs := measuredRecommendations(rows)

	res := &SizeResult{}
	for _, top := range f.Body().Blocks() {
		if top.Type() != "atomic" {
			continue
		}
		for _, el := range top.Body().Blocks() {
			if el.Type() != "backend" {
				continue
			}
			for _, blk := range el.Body().Blocks() {
				key, address, ok := slotOfBlock(blk)
				if !ok {
					continue
				}
				res.size(blk.Body(), key, address, byKey, recs)
			}
		}
	}
	res.Output = f.Bytes()
	return res, nil
}

// slotOfBlock is the booking key and the address of a function or worker block.
func slotOfBlock(blk *hclwrite.Block) (key, address string, ok bool) {
	labels := blk.Labels()
	if len(labels) != 1 {
		return "", "", false
	}
	switch blk.Type() {
	case "function":
		key, ok = functionBookingKey(labels[0])
		return key, "function " + labels[0], ok
	case "worker":
		return "worker:" + labels[0], "worker " + labels[0], true
	}
	return "", "", false
}

// functionBookingKey reads a Function's label into the key the slice books it under.
func functionBookingKey(label string) (string, bool) {
	m := functionLabelRe.FindStringSubmatch(label)
	if m == nil {
		return "", false
	}
	if m[3] != "" {
		return "queue:" + m[3], true
	}
	return strings.ToLower(m[1]) + ":" + m[2], true
}

func (r *SizeResult) size(body *hclwrite.Body, key, address string, byKey map[string]sizingRow, recs map[string]int64) {
	row, reported := byKey[key]
	rec, measured := recs[key]
	switch {
	case !reported:
		r.Left = append(r.Left, address+": the slice reports no measurement for it (is it deployed?)")
		return
	case !measured:
		r.Left = append(r.Left, address+": never invoked, so there is nothing to size it from")
		return
	}

	attr := body.GetAttribute("memory")
	if attr == nil {
		r.Left = append(r.Left, address+": writes no memory to rewrite")
		return
	}
	toks := attr.Expr().BuildTokens(nil)
	if len(toks) != 3 || toks[0].Type != hclsyntax.TokenOQuote || toks[1].Type != hclsyntax.TokenQuotedLit ||
		toks[2].Type != hclsyntax.TokenCQuote {
		r.Left = append(r.Left, address+": its memory is not a plain size string, so the figure is set elsewhere; change it there")
		return
	}
	current, err := parseSize(string(toks[1].Bytes))
	if err != nil {
		r.Left = append(r.Left, address+": its memory is not a size this can read ("+err.Error()+")")
		return
	}
	switch {
	case rec%int64(bytesPerMiB) != 0:
		r.Left = append(r.Left, fmt.Sprintf("%s: the recommendation, %d bytes, is not a whole number of MiB", address, rec))
		return
	case int64(current) == rec:
		return
	}
	body.SetAttributeValue("memory", cty.StringVal(sizeLabel(int(rec))))
	r.Changes = append(r.Changes, SizeChange{
		Address: address, From: current, To: int(rec), Peak: row.PeakBytes, Calls: row.Measurements,
	})
}

// GetSizeCmd builds `drift size`.
func GetSizeCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "size",
		Short: "Write what each function and worker has measured into the Driftfile's memory lines",
		Long: `Read what the slice measured for each function and worker, and rewrite the memory
line of every one the slice recommends a different size for. Comments and layout
stay as they are; the change is shown as a diff.

The file is changed and nothing else: run 'drift plan' to see what the new
bookings cost, and apply them as any other edit. A function nobody has called yet
is left alone, because there is nothing to size it from.`,
		Example: "  drift size\n  drift size --dry-run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := requireDriftfile()
			if err != nil {
				return err
			}
			return runSize(cmd.OutOrStdout(), path, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the change without writing it")
	return cmd
}

func runSize(out io.Writer, path string, dryRun bool) error {
	src, err := os.ReadFile(path) // #nosec G304 — the user's own manifest
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	name := filepath.Base(path)
	file, _, err := readDriftfile(path, src)
	if err != nil {
		return err
	}
	if len(file.Slice) != 1 {
		return fmt.Errorf("%s declares %d slice blocks; `drift size` reads one", name, len(file.Slice))
	}

	// The measurements are the named slice's: the request resolves its slice
	// from the session, so for this process the active slice is the file's.
	if err := os.Setenv(common.SliceEnv, file.Slice[0].Name); err != nil {
		return err
	}
	declared := map[string]string{}
	for _, el := range file.Backends {
		for _, p := range el.Functions {
			if key, ok := functionBookingKey(p.Label); ok {
				declared[key] = ""
			}
		}
		for _, f := range el.Workers {
			declared["worker:"+f.Name] = ""
		}
	}
	rows, err := sizingRows(declared)
	if err != nil {
		return err
	}
	if len(measuredRecommendations(rows)) == 0 {
		fmt.Fprintln(out, "  Nothing to size from: no function or worker has been invoked yet, so there is no")
		fmt.Fprintln(out, "  measurement. Send some traffic and run this again.")
		return nil
	}

	res, err := ResizeDriftfile(src, rows)
	if err != nil {
		return err
	}
	// What is written is read back first; a result that does not parse is
	// never put in place of the file.
	if _, _, err := readDriftfile(path, res.Output); err != nil {
		return fmt.Errorf("the edited Driftfile does not read back, so nothing was written; this is a bug in drift size:\n\n%w", err)
	}
	renderSize(out, name, src, res)

	if len(res.Changes) == 0 {
		return nil
	}
	if dryRun {
		fmt.Fprintf(out, "  %s nothing written (--dry-run)\n\n", common.Hint("·"))
		return nil
	}
	if err := os.WriteFile(path, res.Output, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s %s updated. `drift plan` shows what the new bookings cost; nothing is bought until you apply them.\n\n",
		common.Hint("✓"), name)
	return nil
}

func renderSize(out io.Writer, name string, src []byte, res *SizeResult) {
	fmt.Fprintln(out)
	for _, c := range res.Changes {
		fmt.Fprintf(out, "  ~ %-30s memory %s → %s   (peak %s over %d calls)\n", c.Address,
			sizeLabel(c.From), sizeLabel(c.To), megabytesOrDash(c.Peak), c.Calls)
	}
	for _, l := range res.Left {
		fmt.Fprintf(out, "    %s\n", l)
	}
	if len(res.Changes) == 0 {
		fmt.Fprintln(out, "  Every measured booking already matches its recommendation.")
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, UnifiedDiff("a/"+name, "b/"+name, src, res.Output))
	fmt.Fprintln(out)
}
