package file

// explain.go — what a Driftfile BLOCK or ATTRIBUTE is, and what it may contain.
//
// `drift file explain atomic.element.pulse` answers the question kubectl answers for
// a manifest field: what it is, what it takes, and what goes inside it.
//
// It renders project.DriftfileFormat, whose structure is read from the very
// structs the Driftfile is decoded into, so what this prints is what the reader
// accepts. It is offline and needs no account and no cached schema.
//
// # Not `drift file resolve`
//
// For what one FILE resolves to, `drift file resolve`. The guard below refuses
// a path on disk or `--env`, which belong to that command, and names it rather
// than guessing which of the two was meant.

import (
	"fmt"
	"os"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getExplainCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:   "explain [path]",
		Short: "Describe a Driftfile block or attribute: what it means, and what it takes",
		Long: "Walks a dotted path into the Driftfile format (`atomic.element.pulse`, " +
			"`atomic.element.pulse.memory`) and prints what it is, the labels and attributes it takes, " +
			"and the blocks inside it. Bare `drift file explain` lists the blocks a Driftfile can " +
			"hold.\n\nFor what one FILE resolves to, see `drift file resolve`.",
		Example: "  drift file explain\n  drift file explain atomic.element.pulse\n" +
			"  drift file explain atomic.element.pulse.memory\n  drift file explain slice.limits\n" +
			"  drift file explain --recursive",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) > 0 {
				path = strings.TrimSpace(args[0])
			}
			if err := guardOldUsage(cmd, path); err != nil {
				return err
			}
			root := project.DriftfileFormat()
			block, attr, parent := root.Find(path)
			switch {
			case attr != nil:
				printAttr(cmd, parent, *attr)
			case block != nil:
				printBlock(cmd, *block, recursive)
			default:
				return unknownPath(*parent, path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&recursive, "recursive", false, "print every block and attribute under the path")
	// DECLARED SO IT CAN BE REFUSED WELL. Without it cobra answers `--env` with
	// "unknown flag" before RunE is reached, and the person typing it learns
	// that this command has no such flag rather than which command does. Hidden,
	// because it is not an option here — it is a redirect.
	var env string
	cmd.Flags().StringVar(&env, "env", "", "")
	_ = cmd.Flags().MarkHidden("env")
	return cmd
}

// looksLikeFile reports whether an argument was meant for `drift file resolve`.
//
// Asked of the filesystem rather than inferred from the spelling, because
// `Driftfile` and `staging` are both legal paths and neither looks like one.
// The two prefixes are checked first for the case that matters most: a path
// the user typed that does NOT exist — `./Driftfil` should be answered as a
// mistyped file, not as a mysterious block.
func looksLikeFile(p string) bool {
	if strings.ContainsAny(p, `/\`) || strings.HasPrefix(p, ".") {
		return true
	}
	st, err := os.Stat(p)
	return err == nil && (st.Mode().IsRegular() || st.IsDir())
}

// guardOldUsage refuses what belongs to `drift file resolve`, by naming it.
func guardOldUsage(cmd *cobra.Command, path string) error {
	if cmd.Flags().Changed("env") {
		return fmt.Errorf("`drift file explain` describes the Driftfile FORMAT and has no environment\n" +
			"  for what a file resolves to, run: drift file resolve --env <name>")
	}
	if path != "" && looksLikeFile(path) {
		return fmt.Errorf("%q is a path on disk, and `drift file explain` takes a block or attribute\n"+
			"  to describe a block:         drift file explain atomic.element.pulse\n"+
			"  to resolve that file:        drift file resolve %s", path, path)
	}
	return nil
}

// header is how a block is written, labels included: `pulse "trigger"`.
func header(b project.FormatBlock) string {
	if b.Name == "" {
		return "Driftfile"
	}
	h := b.Name
	for _, l := range b.Labels {
		h += fmt.Sprintf(" %q", l)
	}
	return h
}

func printBlock(cmd *cobra.Command, b project.FormatBlock, recursive bool) {
	out := cmd.OutOrStdout()
	if b.Name == "" {
		fmt.Fprintln(out, "Driftfile")
	} else {
		fmt.Fprintf(out, "BLOCK:  %s {}\n", header(b))
		if strings.Contains(b.Path, ".") {
			fmt.Fprintf(out, "        %s\n", common.Hint("inside "+b.Path[:strings.LastIndex(b.Path, ".")]))
		}
	}
	if b.Doc != "" {
		fmt.Fprintf(out, "\n%s\n", indentWrap(b.Doc, "     ", 76))
	}
	if recursive {
		fmt.Fprintln(out)
		printTree(cmd, b, "   ")
		return
	}
	if len(b.Attrs) > 0 {
		fmt.Fprintf(out, "\nATTRIBUTES:\n")
		for _, a := range b.Attrs {
			fmt.Fprintf(out, "   %s = %s%s\n", a.Name, a.Type, required(a))
			fmt.Fprintf(out, "%s\n", indentWrap(firstSentence(a.Doc), "     ", 76))
		}
	}
	if len(b.Blocks) > 0 {
		fmt.Fprintf(out, "\nBLOCKS:\n")
		for _, c := range b.Blocks {
			fmt.Fprintf(out, "   %s {}\n", header(c))
			fmt.Fprintf(out, "%s\n", indentWrap(firstSentence(c.Doc), "     ", 76))
		}
	}
	next := "drift file explain <block>   ·   --recursive for the whole tree"
	if b.Path != "" {
		next = "drift file explain " + b.Path + ".<attribute>"
	}
	fmt.Fprintf(out, "\n  %s\n", common.Hint(next))
}

func printAttr(cmd *cobra.Command, in *project.FormatBlock, a project.FormatAttr) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ATTRIBUTE:  %s = %s%s\n", a.Name, a.Type, required(a))
	fmt.Fprintf(out, "            %s\n", common.Hint("inside "+header(*in)))
	fmt.Fprintf(out, "\n%s\n", indentWrap(a.Doc, "     ", 76))
}

// printTree prints every block and attribute under b, depth-first.
func printTree(cmd *cobra.Command, b project.FormatBlock, indent string) {
	out := cmd.OutOrStdout()
	for _, a := range b.Attrs {
		fmt.Fprintf(out, "%s%s = %s%s\n", indent, a.Name, a.Type, required(a))
	}
	for _, c := range b.Blocks {
		fmt.Fprintf(out, "%s%s {\n", indent, header(c))
		printTree(cmd, c, indent+"   ")
		fmt.Fprintf(out, "%s}\n", indent)
	}
}

func required(a project.FormatAttr) string {
	if a.Required {
		return "   -required-"
	}
	return ""
}

// unknownPath is the discovery half of this command. A bare "no such block"
// sends someone to the docs for a name that is one letter away.
func unknownPath(at project.FormatBlock, path string) error {
	missing := path[strings.LastIndex(path, ".")+1:]
	var names []string
	for _, c := range at.Blocks {
		names = append(names, c.Name)
	}
	for _, a := range at.Attrs {
		names = append(names, a.Name)
	}
	var b strings.Builder
	where := "a Driftfile"
	if at.Name != "" {
		where = "a " + at.Name + " block"
	}
	fmt.Fprintf(&b, "%s holds no block or attribute %q", where, missing)
	if n := nearest(names, missing); n != "" {
		fmt.Fprintf(&b, "\n  did you mean: %s", n)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "\n  available here: %s", strings.Join(names, ", "))
	}
	return fmt.Errorf("%s", b.String())
}

// nearest is the name closest to a mistyped one, within two edits.
func nearest(names []string, want string) string {
	best, bestD := "", 3
	for _, n := range names {
		if d := editDistance(n, want); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// firstSentence trims a description to its opening statement for a list,
// where the full prose belongs to `explain <that path>` rather than here.
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// indentWrap wraps text to width and prefixes every line, so a long
// description reads as a paragraph rather than one line off the screen.
func indentWrap(s, prefix string, width int) string {
	var out []string
	line := prefix
	for _, w := range strings.Fields(s) {
		if len(line)+1+len(w) > width && strings.TrimSpace(line) != "" {
			out = append(out, line)
			line = prefix
		}
		if strings.TrimSpace(line) == "" {
			line += w
		} else {
			line += " " + w
		}
	}
	return strings.Join(append(out, line), "\n")
}
