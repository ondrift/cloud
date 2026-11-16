// Package doctor is `drift doctor` — one place to go when something is wrong.
//
// The diagnostic surface is deliberately ONE noun rather than several verbs
// scattered across the CLI. A user who is stuck should have one thing to
// remember, not a list to search; `explain` living here rather than as a
// top-level `drift explain` is that rule applied to its first verb.
//
// Everything under this noun works OFFLINE and without a session. That is not a
// nicety: the moment a person needs a diagnostic is the moment the network, the
// platform or their credentials are the thing that failed, and a doctor that
// needs the patient to be healthy is no doctor at all.
package doctor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// GetCmd returns the `drift doctor` command group.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Work out what went wrong — offline, no account needed",
		Long:    "Diagnostics for when something has failed. Everything here runs offline and without a session, because that is when you need it.",
		Example: "  drift doctor explain DRIFT-1006\n  drift doctor explain 1006\n  drift doctor codes",
		GroupID: "project",
	}
	cmd.AddCommand(getExplainCmd(), getCodesCmd())
	return cmd
}

func getExplainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain <code>",
		Short: "Expand a DRIFT-#### code into what happened and what to do",
		Long: "Every failure the CLI prints carries a code. This expands one into the full\n" +
			"meaning and the remedy.\n\n" +
			"The registry is compiled into this binary, so it answers during an outage —\n" +
			"which is exactly when you are holding a code.",
		Example: "  drift doctor explain DRIFT-1006\n  drift doctor explain 1006",
		Args:    cobra.ExactArgs(1),
		// A code that is not registered is the user holding a typo, not the
		// command being held wrong, so the usage block would bury the answer.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			entry, ok := common.LookupErrorCode(args[0])
			if !ok {
				return NotRegistered(args[0])
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\n  %s\n\n", common.Highlight(entry.Code))
			fmt.Fprintf(out, "  %s\n\n", entry.Meaning)
			for _, line := range wrap(entry.Remedy, 76) {
				fmt.Fprintf(out, "  %s\n", line)
			}
			// The fix, on its own line, in three words. Where there is none —
			// a platform fault, an unreachable network — say so rather than
			// inventing a command that would fail again.
			fmt.Fprintln(out)
			if entry.Command != "" {
				fmt.Fprintf(out, "  %s  %s\n", common.Hint("fix:"), common.Highlight(entry.Command))
			} else {
				fmt.Fprintf(out, "  %s\n", common.Hint("There is no command you can run for this one."))
			}
			if entry.Retired {
				fmt.Fprintf(out, "\n  %s\n", common.Hint("This code is retired — a current CLI no longer produces it."))
			}
			// The address of the code's own page, printed and never fetched, so
			// it is as good offline as the rest. It ends the answer.
			fmt.Fprintf(out, "\n  %s  %s\n", common.Hint("more:"), common.ErrorCodePage(entry.Code))
			fmt.Fprintln(out)
			return nil
		},
	}
}

func getCodesCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "codes",
		Short:        "List every failure code this binary can produce",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			codes := common.ErrorCodes()
			fmt.Fprintf(out, "\n  %d failure codes\n\n", len(codes))
			for _, c := range codes {
				suffix := ""
				if c.Retired {
					suffix = "  (retired)"
				}
				fmt.Fprintf(out, "  %-12s  %s%s\n", c.Code, c.Meaning, suffix)
			}
			fmt.Fprintf(out, "\n  %s\n\n", common.Hint("drift doctor explain <code> for the remedy"))
			return nil
		},
	}
}

// NotRegistered is the error for text that names no registered failure.
//
// Text that is a well-formed code numbered above every registered one is what a
// drift newer than this binary prints, so the error also says this binary may be
// the older one and which command fixes that.
func NotRegistered(arg string) error {
	msg := fmt.Sprintf("no failure is registered under %q.\n"+
		"  Codes look like DRIFT-1006. `drift doctor codes` lists every one this binary knows.", arg)
	if code, highest, newer := common.NewerErrorCode(arg); newer {
		msg += fmt.Sprintf("\n  This binary knows codes up to %s and may be older than %s. "+
			"Run `drift upgrade` to install the latest.", highest, code)
	}
	return errors.New(msg)
}

// wrap breaks a remedy onto terminal-friendly lines at word boundaries.
//
// Written here rather than pulled in: a remedy is the one place the CLI prints a
// paragraph, and a dependency for one paragraph is a dependency that outlives
// the reason for it (Article VIII).
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	lines := []string{}
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	return append(lines, cur)
}
