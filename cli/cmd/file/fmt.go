package file

// fmt.go — canonical key order and indentation, so Driftfile diffs stay readable.
//
// # Two things it deliberately does not do
//
// **It does not expand shorthands.** `canvas: ./site` is one of the format's
// short forms and a user chose it; rewriting it into the canonical long form on
// a formatting pass would turn "tidy my file" into "restructure my file", and
// produce exactly the enormous diff this command exists to prevent.
//
// **It does not touch anything below the section level.** Ordering the top level
// and the known sections is enough to keep diffs stable; sorting a user's list of
// functions or domains would reorder things whose order they may be reading as
// meaningful.
//
// # Comments
//
// It formats the yaml.Node tree rather than a re-marshalled struct, because a
// struct round-trip silently drops every comment in the file. A formatter that
// eats your comments is worse than no formatter, and it is the kind of loss that
// is only noticed later, in a diff, by someone else.

import (
	"bytes"
	"fmt"
	"os"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// canonicalOrder is the key order a formatted Driftfile is written in: identity
// first, then the retention knobs, then the three primitives in ABC order
// (Atomic, Backbone, Canvas — the platform's own vocabulary), then the siblings
// that are about deployment rather than shape.
//
// A key not listed keeps its relative position after the listed ones, so a field
// this CLI has never heard of is preserved and moved to the end rather than
// dropped or rejected.
var canonicalOrder = []string{
	"name",
	"log_retention",
	"backup_retention",
	"atomic",
	"backbone",
	"canvas",
	"domains",
	"environments",
	"hooks",
	"tests",
}

// sectionOrder is the same idea one level down, for the sections whose shape is
// known. Sections not listed here are left in the order the user wrote them.
var sectionOrder = map[string][]string{
	"atomic":   {"function_timeout", "rate_limit", "deploy_history", "egress", "functions"},
	"backbone": {"secrets", "nosql", "sql", "queues", "blobs", "cache", "realtime"},
	"canvas":   {"canvas_size", "sites"},
}

func getFmtCmd() *cobra.Command {
	var write bool
	cmd := &cobra.Command{
		Use:   "fmt [path]",
		Short: "Rewrite a Driftfile in canonical key order",
		Long: "Prints the Driftfile with a canonical key order and 2-space indentation. " +
			"Comments and short forms are preserved. Use --write to update the file in place.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — the user named this file
			if rerr != nil {
				return rerr
			}
			out, ferr := formatDriftfile(src)
			if ferr != nil {
				return fmt.Errorf("%s: %w", shortPath(path), ferr)
			}
			if !write {
				fmt.Print(string(out))
				return nil
			}
			if bytes.Equal(src, out) {
				fmt.Printf("%s %s already formatted\n", common.Hint("·"), shortPath(path))
				return nil
			}
			// 0o600 rather than preserving the original mode: a Driftfile can carry
			// `$ENVREF` secret names and lives beside code, and widening a file's
			// permissions as a side effect of formatting it would be a surprising
			// way to lose a secret.
			if werr := os.WriteFile(path, out, 0o600); werr != nil {
				return werr
			}
			fmt.Printf("%s %s formatted\n", common.Hint("✓"), shortPath(path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "rewrite the file in place instead of printing it")
	return cmd
}

// formatDriftfile reorders keys and re-indents, preserving comments and values.
func formatDriftfile(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	// An empty file parses to a document with no content; there is nothing to
	// order and nothing to complain about.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return src, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return src, nil
	}

	orderMapping(root, canonicalOrder)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		if order, ok := sectionOrder[key]; ok && val.Kind == yaml.MappingNode {
			orderMapping(val, order)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// orderMapping rearranges a mapping node's key/value pairs into `order`,
// preserving each pair's own nodes — and therefore its comments — and leaving
// unknown keys in their existing relative order at the end.
func orderMapping(m *yaml.Node, order []string) {
	if m.Kind != yaml.MappingNode {
		return
	}
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}

	type pair struct {
		k, v *yaml.Node
		rank int
		seq  int
	}
	pairs := make([]pair, 0, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		r, known := rank[m.Content[i].Value]
		if !known {
			r = len(order) // unknown keys sort after every known one
		}
		pairs = append(pairs, pair{k: m.Content[i], v: m.Content[i+1], rank: r, seq: i})
	}

	// A stable sort on (rank, original position) is what keeps unknown keys —
	// and any two keys sharing a rank — in the order the user wrote them.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0; j-- {
			a, b := pairs[j-1], pairs[j]
			if a.rank < b.rank || (a.rank == b.rank && a.seq <= b.seq) {
				break
			}
			pairs[j-1], pairs[j] = b, a
		}
	}

	out := make([]*yaml.Node, 0, len(m.Content))
	for _, p := range pairs {
		out = append(out, p.k, p.v)
	}
	m.Content = out
}
