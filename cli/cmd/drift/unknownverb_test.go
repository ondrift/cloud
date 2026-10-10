package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// isolate gives the test a HOME with no session, so nothing it runs can reach
// an account even by mistake.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// A verb a group does not have is an error, not a help screen that exits 0.
// Exiting 0 is what made it dangerous: a CI job still running
// `drift file apply` printed `drift file`'s help, passed, and deployed nothing.
//
// A group is either refused-unknown by refuseUnknownVerbs, which marks it, or
// has a run function of its own and decides for itself what its arguments
// mean. Only the marked run function is called here: a command's own would do
// whatever that command does.
func TestEveryGroupRefusesAVerbItDoesNotHave(t *testing.T) {
	isolate(t)
	marked := 0
	walk(newRootCmd("test"), func(c *cobra.Command) {
		if !c.HasSubCommands() || !c.HasParent() || c.Hidden {
			return
		}
		if c.Annotations[unknownVerbAnnotation] == "" {
			if c.RunE == nil && c.Run == nil {
				t.Errorf("%s has no run function, so cobra answers an unknown verb with help and exit 0", c.CommandPath())
			}
			return
		}
		marked++
		err := c.RunE(c, []string{"nosuchverb"})
		if err == nil || !strings.Contains(err.Error(), `"nosuchverb"`) {
			t.Errorf("%s nosuchverb: %v, want an error naming the verb", c.CommandPath(), err)
		}
	})
	if marked == 0 {
		t.Fatal("no group was marked, so this proves nothing")
	}
}

// A group run with no verb still prints its help and succeeds: `drift file` on
// its own is a question, not a mistake.
func TestABareGroupStillPrintsItsHelp(t *testing.T) {
	isolate(t)
	root := newRootCmd("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"file"})
	if err := root.Execute(); err != nil {
		t.Fatalf("drift file: %v", err)
	}
	if !strings.Contains(out.String(), "drift file [command]") {
		t.Errorf("drift file printed no help:\n%s", out.String())
	}
}

// The verbs the HCL Driftfile removed fail through the real command line, and
// say what does the job now.
func TestARemovedDriftfileVerbSaysWhatReplacedIt(t *testing.T) {
	isolate(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"file", "apply"}, "drift apply"},
		{[]string{"file", "apply", "--plan"}, "drift apply"},
		{[]string{"file", "simulate"}, "drift plan"},
		{[]string{"file", "migrate"}, "drift file new"},
		{[]string{"file", "deploy"}, "drift apply"},
		{[]string{"file", "diff"}, "drift plan"},
		{[]string{"project", "apply"}, "drift apply"},
	} {
		root := newRootCmd("test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(tc.args)
		err := root.Execute()
		if err == nil {
			t.Errorf("drift %s succeeded; it must fail so a script running it does not pass\n%s",
				strings.Join(tc.args, " "), out.String())
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("drift %s: %v, want it to name %q", strings.Join(tc.args, " "), err, tc.want)
		}
	}
}
