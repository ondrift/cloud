package file

// resolve.go — what THIS Driftfile will actually provision.
//
// The value is the RESOLVED shape, not a re-print of the file. Variables take
// their values, an environment names its own slice, and every
// booking, storage size and limit is printed as the platform will be asked for
// it, so the answer to "what does staging actually get" is a command rather
// than an inference.
//
// It does NOT print a price. The server is the single authority on cost
// (POST /ops/slice/price), and `drift plan` asks it, with a session.
//
// # Not `drift file explain`
//
// `explain` describes the FORMAT — what is this block, what may it contain.
// That is a different question from this one: one is about the grammar, the
// other about one file's meaning. `explain` refuses an argument meant for this
// command and names it instead; see the guard in explain.go.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getResolveCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "resolve [path]",
		Short: "Show what a Driftfile resolves to: every booking, size and limit",
		Long: "Compiles the Driftfile, for an environment if one is named, and prints the shape it " +
			"will provision. Offline; no account needed.\n\n" +
			"For what a block means rather than what this file says, see `drift file explain`.",
		Example:      "  drift file resolve\n  drift file resolve --env staging\n  drift file resolve ./Driftfile",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			if !project.SchemaAvailable() {
				return project.ErrNoSchema
			}
			c, perr := project.CompileDriftfileEnv(path, env)
			if perr != nil {
				return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), perr)
			}
			printResolved(c)
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "environment to resolve (default: the file's own slice)")
	return cmd
}

func printResolved(c *project.Compiled) {
	m := c.Manifest
	s := m.Slice()

	paid := "priced"
	if c.FitsFree {
		paid = "fits a free slice"
	}
	title := fmt.Sprintf("slice %s  (%s)", m.Name(), paid)
	if c.Environment != "" {
		title += fmt.Sprintf("  environment %s", c.Environment)
	}
	fmt.Println(title)
	fmt.Println(strings.Repeat("─", len(title)))

	booked := map[string]int{}
	for _, f := range c.Shape.Atomic.Functions {
		booked[f.Name] = f.MemoryBytes
	}

	if specs := project.FunctionSpecs(m); len(specs) > 0 {
		fmt.Printf("\n  pulses (%d)\n", len(specs))
		for _, p := range specs {
			fmt.Printf("    · %-34s %s%s\n", c.Addresses[p.Name], sizeOf(booked[p.Name]), inElement(p.Element))
		}
	}
	if specs := project.FieldSpecs(m); len(specs) > 0 {
		fmt.Printf("\n  fields (%d)\n", len(specs))
		for _, f := range specs {
			fmt.Printf("    · %-34s %s%s\n", c.Addresses[f.Name], sizeOf(booked[f.Name]), inElement(f.Element))
		}
	}

	bb := c.Shape.Backbone
	for _, set := range []struct {
		noun  string
		sizes map[string]int
	}{
		{"sql", bb.SQL.Databases},
		{"nosql", bb.NoSQL.Collections},
		{"blobs", bb.Blobs.Buckets},
	} {
		if len(set.sizes) == 0 {
			continue
		}
		fmt.Printf("\n  %s (%d)\n", set.noun, len(set.sizes))
		for _, name := range sortedKeys(set.sizes) {
			fmt.Printf("    · %-34s %s\n", name, sizeOf(set.sizes[name]))
		}
	}
	if len(bb.Queues.Queues) > 0 {
		fmt.Printf("\n  queues (%d)\n", len(bb.Queues.Queues))
		for _, name := range sortedKeys(bb.Queues.Queues) {
			depth := "the slice's queue depth"
			if d := bb.Queues.Queues[name]; d > 0 {
				depth = fmt.Sprintf("%d messages", d)
			}
			fmt.Printf("    · %-34s %s\n", name, depth)
		}
	}

	if limits := project.ShapeLimits(c); len(limits) > 0 {
		fmt.Printf("\n  limits\n")
		for _, l := range limits {
			fmt.Printf("    · %-34s %s\n", l.Name, l.Value)
		}
	}

	if sites := s.Entries("dir", "canvas", "sites"); len(sites) > 0 {
		fmt.Printf("\n  sites (%d)\n", len(sites))
		for _, site := range sites {
			path := site.Str("path")
			if path == "" {
				path = "/"
			}
			fmt.Printf("    · %-34s %s\n", path, site.Str("dir"))
		}
	}
	if domains := s.Entries("host", "edge", "domains"); len(domains) > 0 {
		fmt.Printf("\n  domains (%d)\n", len(domains))
		for _, d := range domains {
			suffix := ""
			if d.Bool("wildcard") {
				suffix = "  (wildcard)"
			}
			fmt.Printf("    · %s%s\n", d.Str("host"), suffix)
		}
	}
	// An empty allowlist is still a declaration: it clears the slice's, so it is
	// printed rather than left out like an absent key.
	if s.Has("edge", "allow") {
		cidrs := s.Strings("edge", "allow")
		fmt.Printf("\n  inbound allowlist (%d)\n", len(cidrs))
		if len(cidrs) == 0 {
			fmt.Printf("    %s\n", common.Hint("empty: every address reaches the slice"))
		}
		for _, cidr := range cidrs {
			fmt.Printf("    · %s\n", cidr)
		}
	}
	if hosts := s.Entries("host", "edge", "hosts"); len(hosts) > 0 {
		fmt.Printf("\n  fronted hosts (%d)\n", len(hosts))
		for _, h := range hosts {
			fmt.Printf("    · %s → %s\n", h.Str("host"), h.Str("slice"))
		}
	}

	// Write-only values: where each is read from, never a value. The compile
	// holds references only, so there is no value here to print.
	if len(c.WriteOnly) > 0 {
		fmt.Printf("\n  write-only values (%d, read from the environment)\n", len(c.WriteOnly))
		for _, w := range c.WriteOnly {
			fmt.Printf("    · %-34s $%s\n", w.Address, w.Env)
		}
	}

	if len(c.Environments) > 0 {
		fmt.Printf("\n  %s %s\n", common.Hint("environments:"), strings.Join(c.Environments, ", "))
	}
	fmt.Printf("\n  %s\n", common.Hint("the price comes from the platform: drift plan"))
}

// inElement names the element a Pulse or Field belongs to, and nothing for
// the default element.
func inElement(element string) string {
	if element != "" {
		return "  " + common.Hint("(element "+element+")")
	}
	return ""
}

// sizeOf renders a byte count the way a Driftfile writes it.
func sizeOf(b int) string {
	switch {
	case b <= 0:
		return "—"
	case b%(1<<30) == 0:
		return fmt.Sprintf("%dGiB", b>>30)
	case b%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", b>>20)
	case b%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", b>>10)
	}
	return fmt.Sprintf("%dB", b)
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
