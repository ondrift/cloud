package project

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// planInvocation is a `drift plan` command line inside help text, up to the
// end of the line, a quote, a backtick or a shell `&&`.
var planInvocation = regexp.MustCompile("drift plan((?: [^\n'`&]*)?)")

// Every `drift plan ...` that the plan and apply help prints is something
// someone copies, so every flag in it must be one drift plan has, spelled the
// way it parses. A one-dash `-out` reads as the shorthands -o -u -t and is
// refused.
func TestHelp_EveryPlanFlagItPrintsIsOneDriftPlanHas(t *testing.T) {
	flags := GetPlanCmd().Flags()
	for _, cmd := range []*cobra.Command{GetPlanCmd(), GetApplyCmd()} {
		found := 0
		for _, text := range []string{cmd.Long, cmd.Example} {
			for _, m := range planInvocation.FindAllStringSubmatch(text, -1) {
				for _, tok := range strings.Fields(m[1]) {
					name, isLong := strings.CutPrefix(tok, "--")
					switch {
					case isLong:
						found++
						name, _, _ = strings.Cut(name, "=")
						if flags.Lookup(name) == nil {
							t.Errorf("drift %s's help prints %q, and drift plan has no --%s", cmd.Name(), strings.TrimSpace(m[0]), name)
						}
					case strings.HasPrefix(tok, "-") && len(tok) > 2:
						found++
						t.Errorf("drift %s's help prints %q: %s is a long flag written with one dash, which drift plan reads as shorthands and refuses",
							cmd.Name(), strings.TrimSpace(m[0]), tok)
					}
				}
			}
		}
		if cmd.Name() == "apply" && found == 0 {
			t.Fatal("drift apply's help names no drift plan flag, so this proves nothing")
		}
	}
}
