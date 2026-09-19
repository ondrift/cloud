package main

import (
	"regexp"
	"strings"
	"testing"

	account "github.com/ondrift/cloud/cli/cmd/account"
	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// Anything the CLI tells someone to type has to be typeable.
//
// Help text is already held to that: an Example is walked for flags that exist,
// and an error remedy is resolved against the command tree. Neither check can
// see the text below, because it is printed at runtime from inside a RunE
// closure and a printed string is not a call. So the strings are declared where
// the tree can be asked about them, and this is the asking.
//
// The registry is the post-signup banner: the literal first commands a new
// account is told to type, where a wrong line is the first thing the product
// ever does to someone. It grows as other runtime-printed suggestions are found.
var typeableHints = map[string][]string{
	"the post-signup banner (drift account create)":  account.FirstRunHints,
	"the not-logged-in hint (common.NotLoggedInHint)": {common.NotLoggedInHint},
}

func TestPrintedHintsNameRealCommands(t *testing.T) {
	root := newRootCmd("test")

	var checked int
	for site, lines := range typeableHints {
		for _, line := range lines {
			for _, invocation := range quotedInvocations(line) {
				checked++
				if problem := unrunnable(root, invocation); problem != "" {
					t.Errorf("%s tells the user to run %q, which %s:\n  %s",
						site, invocation, problem, strings.TrimSpace(line))
				}
			}
		}
	}

	// Without this, an extractor that finds nothing passes every line.
	if checked == 0 {
		t.Error("no invocation was extracted from any hint, so this test checked nothing")
	}
}

// The control for the resolver. Each of these either names nothing or stops at
// a group that runs nothing, and the first is the exact shape a banner line
// takes when it names a verb that was never a top-level command. If the
// resolver calls any of them runnable it cannot detect the case it exists for.
func TestTheHintResolverRejectsACommandThatDoesNotExist(t *testing.T) {
	root := newRootCmd("test")

	for _, bogus := range []string{
		"drift deploy drift.yaml",
		"drift nosuchgroup",
		"drift slice nosuchverb",
		"drift slice",
	} {
		if unrunnable(root, bogus) == "" {
			t.Errorf("%q was judged runnable, so the check above cannot detect a hint "+
				"that names nothing", bogus)
		}
	}
}

// A survey that offers a value its own validation rejects is a dead end reached
// by using the command exactly as designed: the menu is the CLI's own
// suggestion, and the step after it, inside the same invocation, refuses the
// answer with no way back but re-running the wizard.
//
// Survey options are a plain slice rather than something the command tree
// exposes, so the call sites are named here by hand. The registry grows as
// surveys are added.
func TestSurveyOptionsAreAcceptedByTheirOwnValidation(t *testing.T) {
	surveys := []struct {
		site    string
		options []string
		accept  func(string) error
	}{
		{"drift atomic new, the Auth: prompt", atomic_cmd_new.AuthOptions, atomic_cmd_new.ValidateAuth},
	}

	for _, s := range surveys {
		if len(s.options) == 0 {
			t.Errorf("%s offers nothing, so it cannot be checked", s.site)
			continue
		}
		for _, opt := range s.options {
			if err := s.accept(opt); err != nil {
				t.Errorf("%s offers %q, and its own validation then refuses it: %v",
					s.site, opt, err)
			}
		}
		// The control, per site: the validation has to refuse something, or
		// every option passes for a reason that has nothing to do with the menu.
		if s.accept("definitely-not-an-option") == nil {
			t.Errorf("%s's validation accepts anything, so the check above proves nothing", s.site)
		}
	}
}

// quotedInvocationRe matches a single-quoted `drift …` command directly,
// rather than splitting the line on every `'` and alternating. Splitting broke
// on ordinary prose: an apostrophe in a contraction ("you're", "don't") ahead
// of the first real quote shifts every span's parity, so the odd/even split
// silently paired the wrong halves and stopped extracting anything at all from
// that point on — the exact class of silently-checks-nothing test this
// mechanism exists to prevent. Anchoring on "starts with drift" instead makes
// a stray apostrophe elsewhere in the line harmless.
var quotedInvocationRe = regexp.MustCompile(`'(drift(?:\s[^']*)?)'`)

// quotedInvocations returns the single-quoted `drift …` commands in a printed
// line. The hints quote what to type precisely so it can be lifted back out.
func quotedInvocations(line string) []string {
	var out []string
	for _, m := range quotedInvocationRe.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// unrunnable reports why an invocation cannot be typed, or "" when it names a
// real, executable command.
//
// It descends the tree word by word rather than calling cobra's Find, which
// returns the deepest command it matched and so answers "drift" for
// `drift deploy …` without complaint. The first word after the binary name has
// to name a subcommand; once a word no longer does, the rest is the arguments
// the user supplies, and what matters then is whether the command reached can
// actually run. `drift slice` is a group with no verb, so `drift slice
// nosuchverb` is a hint that prints a usage block, not a hint that works.
func unrunnable(root *cobra.Command, invocation string) string {
	words := strings.Fields(invocation)
	if len(words) == 0 || words[0] != root.Name() {
		return "does not start with `" + root.Name() + "`"
	}

	cur := root
	for i, w := range words[1:] {
		// A placeholder or a flag ends the command path: `drift slice create
		// <name>` is `slice create` plus what the user fills in.
		if strings.HasPrefix(w, "<") || strings.HasPrefix(w, "[") || strings.HasPrefix(w, "-") {
			break
		}
		child := subcommand(cur, w)
		if child == nil {
			if i == 0 {
				return "names no `" + root.Name() + "` command"
			}
			break // an argument to cur, not a command
		}
		cur = child
	}

	if !cur.Runnable() {
		return "stops at `" + cur.CommandPath() + "`, which is a group and runs nothing"
	}
	return ""
}

// subcommand returns the child of cmd that name addresses, including by alias,
// or nil.
func subcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}
