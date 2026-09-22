package file

// explain.go — what a Driftfile KEY is, and what it may contain.
//
// `drift file explain backbone.nosql` answers the question kubectl answers for
// a manifest field: the type, the prose, and the keys underneath with theirs.
//
// # It renders data this machine already holds
//
// The CLI carries no model of the Driftfile format. It validates against the
// JSON Schema the platform serves and caches at ~/.drift/driftfile.schema.json,
// and that document is not a bare type definition — its own $comment says the
// documentation lives in it, and it carries `description`, `default`,
// `examples`, `enum`, `required` and `pattern` throughout.
//
// So this is a RENDERER, and two things follow that are worth stating:
//
//   - THE PROSE CANNOT DRIFT FROM THE FORMAT. It is the same document the
//     validator uses. A field the platform adds is explainable the moment the
//     cached schema refreshes, with no CLI release — which is the whole reason
//     `ondrift/cloud-driftfile` was archived rather than kept in step.
//   - It is OFFLINE, from cache, like the rest of `drift file`, and refuses the
//     same way when the machine has never fetched a schema. A command that
//     invented an answer here would be a second authority on the format.
//
// # This verb used to mean something else
//
// It resolved the environment overlay and printed what one Driftfile
// provisions. That is `drift file resolve` now (cloud/public#69). The guard
// below refuses the old usage and names the new verb rather than aliasing it:
// the two commands answer different questions, so a spelling that silently
// picked one would be the ambiguity the split exists to remove.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getExplainCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:   "explain [path]",
		Short: "Describe a Driftfile key: its type, what it means, and what it accepts",
		Long: "Walks a dotted path into the Driftfile format and prints the key's type, its " +
			"description, and the keys it accepts. Bare `drift file explain` lists the top-level " +
			"keys.\n\nOffline, from the schema this machine has cached — the same document " +
			"`drift file lint` validates against, so the two can never disagree.\n\n" +
			"For what one FILE resolves to, see `drift file resolve`.",
		Example: "  drift file explain\n  drift file explain backbone.nosql\n" +
			"  drift file explain atomic.functions.memory\n  drift file explain --recursive",
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

			// Same refusal as `lint`, and for the same reason: the platform owns
			// the format, and a machine that has never fetched it can only invent
			// an answer. ErrNoSchema already says how to get one.
			if !project.SchemaAvailable() {
				return project.ErrNoSchema
			}
			raw, rerr := common.LocalDriftfileSchema()
			if rerr != nil {
				return rerr
			}
			root, perr := parseSchema(raw)
			if perr != nil {
				return perr
			}

			node, at, missing, werr := walk(root, path)
			if werr != nil {
				return werr
			}
			if node == nil {
				return unknownPath(root, at, path, missing)
			}
			printExplain(root, path, node, recursive)
			return nil
		},
	}
	cmd.Flags().BoolVar(&recursive, "recursive", false,
		"print the whole tree of field names under the path")
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
// mistyped file, not as a mysterious key.
func looksLikeFile(p string) bool {
	if strings.ContainsAny(p, `/\`) || strings.HasPrefix(p, ".") {
		return true
	}
	st, err := os.Stat(p)
	return err == nil && (st.Mode().IsRegular() || st.IsDir())
}

// guardOldUsage refuses what `drift file explain` used to mean.
//
// TWO SHAPES REACH HERE BY HABIT, and both are answered by naming the verb
// rather than by doing something. Guessing would be the ambiguity this split
// removed: `drift file explain backbone` is a dotted path OR a file called
// `backbone`, and only the person typing it knows which.
func guardOldUsage(cmd *cobra.Command, path string) error {
	if cmd.Flags().Changed("env") {
		return fmt.Errorf("`drift file explain` describes the Driftfile FORMAT and has no environment\n" +
			"  for what a file resolves to, run: drift file resolve --env <name>")
	}
	if path == "" {
		return nil
	}
	// A path that names something on disk was almost certainly meant for the
	// old command. Checked against the filesystem rather than guessed from the
	// spelling, because `./Driftfile`, `Driftfile` and `staging/` are all
	// plausible and none of them looks like a dotted key.
	if looksLikeFile(path) {
		return fmt.Errorf("%q is a path on disk, and `drift file explain` takes a Driftfile KEY\n"+
			"  to describe a key:          drift file explain backbone.nosql\n"+
			"  to resolve that file:       drift file resolve %s", path, path)
	}
	return nil
}

// printExplain renders one node: FIELD, DESCRIPTION, then its fields.
func printExplain(root schemaNode, path string, node schemaNode, recursive bool) {
	name := path
	if name == "" {
		name = "Driftfile"
	} else if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}

	forms := formsFor(root, node)
	fmt.Printf("FIELD:  %s %s\n", name, typeName(root, node))
	if v := common.Hint(fmt.Sprintf("format %s", common.DriftfileSchemaVersionOrNone())); v != "" {
		fmt.Printf("        %s\n", v)
	}

	if d := str(flatten(root, node, 0), "description"); d != "" {
		fmt.Printf("\nDESCRIPTION:\n%s\n", indentWrap(d, "     ", 72))
	}
	if def, ok := flatten(root, node, 0)["default"]; ok {
		fmt.Printf("\n     %s %v\n", common.Hint("default:"), def)
	}
	if en := strList(flatten(root, node, 0), "enum"); len(en) > 0 {
		fmt.Printf("\n     %s %s\n", common.Hint("one of:"), strings.Join(en, ", "))
	}
	if p := str(flatten(root, node, 0), "pattern"); p != "" {
		fmt.Printf("     %s %s\n", common.Hint("pattern:"), p)
	}
	if ex := strList(flatten(root, node, 0), "examples"); len(ex) > 0 {
		fmt.Printf("     %s %s\n", common.Hint("e.g."), strings.Join(ex, ", "))
	}

	if recursive {
		fmt.Printf("\nFIELDS:\n")
		printTree(root, node, "   ", 0)
		return
	}

	// EVERY ACCEPTED FORM, not the first. A oneOf is where the shorthands live
	// — `queues: [validate]` beside the object form — and showing one would
	// contradict what `drift file lint` accepts.
	multi := len(forms) > 1
	for i, form := range forms {
		fields := fieldsOf(root, form)
		if len(fields) == 0 {
			if multi {
				fmt.Printf("\nACCEPTS (%d of %d):  %s\n", i+1, len(forms), typeName(root, form))
			}
			continue
		}
		header := "FIELDS:"
		if multi {
			header = fmt.Sprintf("FIELDS (form %d of %d, %s):", i+1, len(forms), typeName(root, form))
		}
		// THE ALTERNATIVES, when there is a choice to make. A node whose
		// requirements live in anyOf branches has no single answer to "what must
		// I write", and printing only the keys common to every branch would tell
		// someone that `handler` is mandatory while saying nothing about having
		// to pick a trigger spelling at all.
		if sets := requiredSets(root, form); len(sets) > 1 {
			fmt.Printf("\nREQUIRED — any one of these sets:\n")
			for _, s := range sets {
				sorted := append([]string(nil), s...)
				sort.Strings(sorted)
				fmt.Printf("   %s\n", strings.Join(sorted, " + "))
			}
		}

		fmt.Printf("\n%s\n", header)
		for _, f := range fields {
			c, ok := child(root, form, f)
			if !ok {
				continue
			}
			req := ""
			if isRequired(root, form, f) {
				req = " -required-"
			}
			fmt.Printf("   %s %s%s\n", f, typeName(root, c), req)
			if d := str(c, "description"); d != "" {
				fmt.Printf("%s\n", indentWrap(firstSentence(d), "     ", 70))
			}
		}
	}

	if path == "" {
		fmt.Printf("\n  %s\n", common.Hint("drift file explain <key>   ·   --recursive for the whole tree"))
	}
}

// printTree prints field names only, depth-first — kubectl's --recursive.
// Bounded, because a recursive $ref would otherwise print until the terminal
// gives up.
func printTree(root schemaNode, node schemaNode, indent string, depth int) {
	if depth > 6 {
		return
	}
	for _, f := range fieldsOf(root, node) {
		c, ok := child(root, node, f)
		if !ok {
			continue
		}
		fmt.Printf("%s%s %s\n", indent, f, typeName(root, c))
		printTree(root, c, indent+"   ", depth+1)
	}
}

// unknownPath is the discovery half of this command. A bare "no such field"
// sends someone to the docs for a key that is one letter away.
func unknownPath(root schemaNode, at schemaNode, path, missing string) error {
	fields := fieldsOf(root, at)
	var b strings.Builder
	fmt.Fprintf(&b, "no field %q in the Driftfile format", missing)
	if missing != path {
		fmt.Fprintf(&b, " (walking %q)", path)
	}
	if n := nearest(fields, missing); n != "" {
		fmt.Fprintf(&b, "\n  did you mean: %s", n)
	}
	if len(fields) > 0 {
		sorted := append([]string(nil), fields...)
		sort.Strings(sorted)
		fmt.Fprintf(&b, "\n  available here: %s", strings.Join(sorted, ", "))
	}
	return fmt.Errorf("%s", b.String())
}

// firstSentence trims a description to its opening statement for a field list,
// where the full prose belongs to `explain <that field>` rather than here.
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// indentWrap wraps text to width and prefixes every line, so a long
// description reads as a paragraph rather than one line off the screen.
//
// IT WRAPS EACH LINE SEPARATELY, and that is not a detail. Several descriptions
// in this schema carry YAML examples — the root's is most of a tutorial — and
// collapsing newlines into spaces first turned
//
//	name: my-project
//	canvas:
//	  sites: [./site]
//
// into "name: my-project canvas: sites: [./site]", which is not YAML and not
// prose. The schema's own line structure is part of what it is saying.
func indentWrap(s, prefix string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		// Indentation inside an example is meaningful, so it is carried rather
		// than normalised away by Fields.
		lead := para[:len(para)-len(strings.TrimLeft(para, " \t"))]
		line := prefix + lead
		for _, w := range words {
			if len(line)+1+len(w) > width && strings.TrimSpace(line) != "" {
				out = append(out, line)
				line = prefix + lead
			}
			if strings.TrimSpace(line) == "" {
				line += w
			} else {
				line += " " + w
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
