package main

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

// The three-word rule, enforced rather than trusted.
//
//	"If what you want to do is more than three words away from within your CLI,
//	 Drift has failed you."
//
// A rule kept by discipline alone is how every other drift in this codebase
// happened — and this one was already broken in shipped output before anything
// checked it. Every remedy the CLI suggests is declared in the error registry
// precisely so a test can walk them.
//
// This lives in package main because it is the only place that holds the whole
// command tree, which is what "resolves to a real command" needs.
func TestRemedies_AreAtMostThreeWords(t *testing.T) {
	for _, c := range common.ErrorCodes() {
		if c.Command == "" {
			continue // a failure with no user-runnable fix; see below
		}
		if n := len(strings.Fields(c.Command)); n > 3 {
			t.Errorf("%s suggests %q — %d words. A remedy more than three words away "+
				"is the failure this rule exists to catch", c.Code, c.Command, n)
		}
	}
}

// A remedy naming a command nobody implemented is worse than no remedy: it sends
// a stuck user somewhere that fails again, and it is invisible until they try.
func TestRemedies_ResolveToRealCommands(t *testing.T) {
	root := newRootCmd("test")

	for _, c := range common.ErrorCodes() {
		if c.Command == "" {
			continue
		}
		words := strings.Fields(c.Command)
		if words[0] != root.Name() {
			t.Errorf("%s suggests %q, which does not start with %q", c.Code, c.Command, root.Name())
			continue
		}
		cmd, _, err := root.Find(words[1:])
		if err != nil {
			t.Errorf("%s suggests %q, which does not resolve: %v", c.Code, c.Command, err)
			continue
		}
		// Find returns the deepest command it matched, so a typo in the LAST
		// word resolves to the parent and looks like a pass. Compare the name.
		if cmd.Name() != words[len(words)-1] {
			t.Errorf("%s suggests %q, but that resolves to %q — the last word names "+
				"no command", c.Code, c.Command, cmd.CommandPath())
		}
	}
}

// An empty command is a real answer and must stay available: a platform fault or
// an unreachable network has no user-runnable fix, and inventing one would send
// the user somewhere that fails again. This pins that the registry actually uses
// it, so the two tests above cannot be satisfied by giving everything a command.
func TestRemedies_AFailureWithNoUserFixSaysSo(t *testing.T) {
	var blank int
	for _, c := range common.ErrorCodes() {
		if c.Command == "" {
			blank++
		}
	}
	if blank == 0 {
		t.Error("every code carries a command, which means a platform fault is " +
			"suggesting the user run something — say nothing instead")
	}
}

// The control for the resolver: a command that does not exist must FAIL to
// resolve. Without it, TestRemedies_ResolveToRealCommands would pass for a
// resolver that accepts anything.
func TestRemedies_TheResolverRejectsAMissingCommand(t *testing.T) {
	root := newRootCmd("test")

	for _, bogus := range [][]string{{"nosuchgroup"}, {"slice", "nosuchverb"}} {
		cmd, _, err := root.Find(bogus)
		resolved := err == nil && cmd.Name() == bogus[len(bogus)-1]
		if resolved {
			t.Errorf("%v resolved to a real command, so the check cannot detect a typo",
				append([]string{root.Name()}, bogus...))
		}
	}
}

// Guard against the tree failing to assemble at all — every test above would
// then pass over an empty registry of commands.
func TestRootCommand_Assembles(t *testing.T) {
	root := newRootCmd("test")
	if len(root.Commands()) == 0 {
		t.Fatal("the root command has no subcommands, so nothing above tested anything")
	}
	var doctor *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "doctor" {
			doctor = c
		}
	}
	if doctor == nil {
		t.Fatal("`drift doctor` is not registered, so no remedy can be explained")
	}
}
