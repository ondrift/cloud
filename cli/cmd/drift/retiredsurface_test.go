package main

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A slice's shape is drawn in the terminal. Nothing the CLI says may send
// someone to a web page to do it.
//
// The hosts below answer 410, so a help line, an example or an error remedy that
// names them is an instruction that cannot be followed — and it is invisible
// from inside the CLI, because a string is not a call. Both checks walk the
// whole command tree from package main, which is the only place that holds it.
var retiredShapeSurface = []string{
	"configurator",
	"slices.ondrift.eu",
	"configurator.ondrift.eu",
}

func TestHelpNamesNoRetiredShapeSurface(t *testing.T) {
	walk(newRootCmd("test"), func(c *cobra.Command) {
		for field, text := range map[string]string{
			"Short":   c.Short,
			"Long":    c.Long,
			"Example": c.Example,
		} {
			for _, dead := range retiredShapeSurface {
				if strings.Contains(strings.ToLower(text), dead) {
					t.Errorf("%s's %s names %q, which answers 410:\n%s",
						c.CommandPath(), field, dead, text)
				}
			}
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			for _, dead := range retiredShapeSurface {
				if strings.Contains(strings.ToLower(f.Usage), dead) {
					t.Errorf("%s --%s names %q, which answers 410: %q",
						c.CommandPath(), f.Name, dead, f.Usage)
				}
			}
		})
	})

	for _, c := range common.ErrorCodes() {
		for _, dead := range retiredShapeSurface {
			if strings.Contains(strings.ToLower(c.Remedy), dead) {
				t.Errorf("%s's remedy names %q, which answers 410: %q", c.Code, dead, c.Remedy)
			}
		}
	}
}

// Every flag an example shows has to exist on the command that example runs.
//
// This is the check that catches a flag being removed without its examples: the
// code compiles, the tests pass, and the help keeps advertising an option that
// is answered with "unknown flag". An example is meant to be typed verbatim, so
// it is held to that.
func TestExamplesOnlyShowFlagsThatExist(t *testing.T) {
	root := newRootCmd("test")

	walk(root, func(c *cobra.Command) {
		for _, line := range strings.Split(c.Example, "\n") {
			// Examples annotate themselves with a trailing `# what this does`,
			// which is prose and may legitimately name anything.
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != root.Name() {
				continue
			}

			// Resolve the line to the command it actually runs, so a root-level
			// example showing a subcommand's flag is checked against that
			// subcommand rather than against root.
			target, _, err := root.Find(fields[1:])
			if err != nil || target == nil {
				continue
			}
			for _, tok := range fields[1:] {
				if !strings.HasPrefix(tok, "--") {
					continue
				}
				name := strings.SplitN(strings.TrimPrefix(tok, "--"), "=", 2)[0]
				if name == "" {
					continue
				}
				if target.Flags().Lookup(name) == nil {
					t.Errorf("%s's example shows --%s, which %s does not have:\n  %s",
						c.CommandPath(), name, target.CommandPath(), strings.TrimSpace(line))
				}
			}
		}
	})
}

// walk visits cmd and every command beneath it.
func walk(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, child := range cmd.Commands() {
		walk(child, visit)
	}
}
