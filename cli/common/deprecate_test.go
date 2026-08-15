package common

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// captureWarnings points the notice stream at a buffer and clears the
// once-per-process state, so each test starts from nothing.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevW, prevR := deprecationWarnings, registry
	deprecationWarnings = buf
	warnedOnce = sync.Map{}
	registryMu.Lock()
	registry = nil
	registryMu.Unlock()
	t.Cleanup(func() {
		deprecationWarnings = prevW
		warnedOnce = sync.Map{}
		registryMu.Lock()
		registry = prevR
		registryMu.Unlock()
	})
	return buf
}

// The notice has to name what to STOP typing and what to type instead, in the
// spelling a person uses. Cobra's own renders the leaf — "deploy" — which does
// not identify the command being retired.
func TestNotice_NamesBothSpellingsInFull(t *testing.T) {
	d := Deprecation{Old: "drift project deploy", New: "drift file apply", RemoveAfter: "v1.0.0"}
	got := d.Notice()

	for _, want := range []string{"drift project deploy", "drift file apply", "v1.0.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notice must contain %q, got: %s", want, got)
		}
	}
}

// A deprecation with no stated exit is how a shim becomes permanent, so the
// removal condition is rendered when it is set — and the sentence still reads
// when it is not.
func TestNotice_ReadsWithoutARemovalCondition(t *testing.T) {
	got := Deprecation{Old: "a", New: "b"}.Notice()
	if strings.Contains(got, "removed after") {
		t.Errorf("no condition was set, so none should be promised: %s", got)
	}
	if !strings.Contains(got, `Use "b" instead.`) {
		t.Errorf("the replacement must still be named: %s", got)
	}
}

// THE test for the once-per-process rule.
//
// An alias forwards into the real command, which is a second Execute on the same
// tree, so the naive version prints twice for one invocation and the notice
// starts reading as noise.
func TestWarn_PrintsOncePerName(t *testing.T) {
	buf := captureWarnings(t)
	d := Deprecation{Old: "drift project", New: "drift file", RemoveAfter: "v1.0.0"}

	d.Warn()
	d.Warn()
	d.Warn()

	if n := strings.Count(buf.String(), "drift project"); n != 1 {
		t.Fatalf("the notice must appear exactly once per name, appeared %d times:\n%s", n, buf.String())
	}
}

// The control: a DIFFERENT deprecation still gets its own line. Without this the
// test above would pass for a harness that warns once and never again.
func TestWarn_ADifferentNameStillWarns(t *testing.T) {
	buf := captureWarnings(t)
	Deprecation{Old: "drift project", New: "drift file"}.Warn()
	Deprecation{Old: "drift slice upgrade", New: "drift slice resize"}.Warn()

	for _, want := range []string{"drift project", "drift slice upgrade"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%q got no notice of its own:\n%s", want, buf.String())
		}
	}
}

// An alias must RUN the command it forwards to, and must not acquire an
// implementation of its own — that is the whole difference between a forwarding
// shim and the second code path the canon refuses.
func TestAliasCommand_RunsTheTargetAndWarns(t *testing.T) {
	buf := captureWarnings(t)

	ran := 0
	target := &cobra.Command{
		Use:  "file",
		RunE: func(c *cobra.Command, args []string) error { ran++; return nil },
	}
	root := &cobra.Command{Use: "drift"}
	root.AddCommand(target, AliasCommand(target, "project",
		Deprecation{Old: "drift project", New: "drift file", RemoveAfter: "v1.0.0"}))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the alias must run: %v", err)
	}
	if ran != 1 {
		t.Errorf("the target ran %d times, want 1 — the alias must forward, not reimplement", ran)
	}
	if !strings.Contains(buf.String(), "drift project") {
		t.Errorf("using the old name must say so:\n%s", buf.String())
	}
}

// The new spelling is the one being taught, so it must stay silent. A harness
// that warns on the replacement too is worse than none.
func TestAliasCommand_TheNewNameIsSilent(t *testing.T) {
	buf := captureWarnings(t)

	target := &cobra.Command{Use: "file", RunE: func(c *cobra.Command, a []string) error { return nil }}

	root := &cobra.Command{Use: "drift"}
	root.AddCommand(target, AliasCommand(target, "project", Deprecation{Old: "drift project", New: "drift file"}))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"file"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the new name must work: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("the replacement must not warn about itself:\n%s", buf.String())
	}
}

// THE regression this design exists for, and the case that actually gets typed.
//
// Two plausible shapes pass the bare-`drift project` test and fail this one, both
// silently: a copied command, whose children keep the original as their parent so
// cobra's pre-run walk never reaches the copy's hook; and cobra's own `Aliases`
// with `CalledAs()`, which reports only for the final resolved command and so
// returns "" for a group on the way to a subcommand. Re-entering the root under
// the new spelling is what makes the whole tree answer and announce.
func TestAliasCommand_WarnsWhenASubcommandIsReachedByTheOldName(t *testing.T) {
	buf := captureWarnings(t)

	ran := 0
	child := &cobra.Command{Use: "apply", RunE: func(c *cobra.Command, a []string) error { ran++; return nil }}
	target := &cobra.Command{Use: "file"}
	target.AddCommand(child)

	root := &cobra.Command{Use: "drift"}
	root.AddCommand(target, AliasCommand(target, "project",
		Deprecation{Old: "drift project", New: "drift file", RemoveAfter: "v0.20.0"}))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "apply"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the subcommand must run through the old name: %v", err)
	}
	if ran != 1 {
		t.Errorf("the subcommand ran %d times, want 1", ran)
	}
	if !strings.Contains(buf.String(), "drift project") {
		t.Errorf("reaching a subcommand by the old name must warn:\n%q", buf.String())
	}
}

// And the same subcommand by its NEW path stays silent, so the test above cannot
// pass for a harness that warns unconditionally.
func TestAliasCommand_ASubcommandByTheNewNameIsSilent(t *testing.T) {
	buf := captureWarnings(t)

	child := &cobra.Command{Use: "apply", RunE: func(c *cobra.Command, a []string) error { return nil }}
	target := &cobra.Command{Use: "file"}
	target.AddCommand(child)

	root := &cobra.Command{Use: "drift"}
	root.AddCommand(target, AliasCommand(target, "project", Deprecation{Old: "drift project", New: "drift file"}))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"file", "apply"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the new path must work: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("the new spelling must stay silent:\n%s", buf.String())
	}
}

// The old name keeps working but is not something to teach. Cobra lists a
// command once under Name() and never enumerates aliases as commands, so the
// retired spelling is absent from help while still resolving.
func TestAliasCommand_TheOldNameIsNotListedInHelp(t *testing.T) {
	captureWarnings(t)
	target := &cobra.Command{Use: "file", RunE: func(c *cobra.Command, a []string) error { return nil }}
	alias := AliasCommand(target, "project", Deprecation{Old: "drift project", New: "drift file"})

	if !alias.Hidden {
		t.Error("a retired spelling must not be advertised in help")
	}
	if alias.Name() != "project" {
		t.Errorf("the alias must answer to the OLD name, got %q", alias.Name())
	}
	if !alias.DisableFlagParsing {
		t.Error("the alias must not parse flags — the tail belongs to the real command")
	}
}

// The set has to be readable without running any of it, so a release can be
// checked against what it promised to remove.
func TestDeprecations_ListsWhatWasDeclared(t *testing.T) {
	captureWarnings(t)
	Deprecation{Old: "drift zeta", New: "drift z"}.Warn()
	Deprecation{Old: "drift alpha", New: "drift a"}.Warn()
	Deprecation{Old: "drift alpha", New: "drift a"}.Warn() // repeat: still one entry

	got := Deprecations()
	if len(got) != 2 {
		t.Fatalf("want 2 distinct deprecations, got %d: %+v", len(got), got)
	}
	if got[0].Old != "drift alpha" || got[1].Old != "drift zeta" {
		t.Errorf("the list must be sorted by the old spelling, got %+v", got)
	}
}

// Flags and arguments belong to the real command, so the alias must hand its
// tail over untouched. Parsing them here would fail on any flag the alias does
// not declare — before the notice is ever printed.
func TestAliasCommand_PassesFlagsAndArgsThrough(t *testing.T) {
	captureWarnings(t)

	var gotEnv string
	var gotArgs []string
	child := &cobra.Command{
		Use:  "apply",
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, a []string) error { gotArgs = a; return nil },
	}
	child.Flags().StringVar(&gotEnv, "env", "", "")
	target := &cobra.Command{Use: "file"}
	target.AddCommand(child)

	root := &cobra.Command{Use: "drift"}
	root.AddCommand(target, AliasCommand(target, "project", Deprecation{Old: "drift project", New: "drift file"}))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "apply", "--env", "staging", "extra"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the alias must forward flags: %v", err)
	}
	if gotEnv != "staging" {
		t.Errorf("--env did not reach the real command, got %q", gotEnv)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "extra" {
		t.Errorf("positional args did not survive, got %v", gotArgs)
	}
}

// A nested rename — `drift file deploy` becoming `drift file apply` — has to
// re-enter under the target's FULL path. Prepending the leaf alone would send it
// to `drift apply`, which does not exist, so the old spelling would fail rather
// than forward.
func TestAliasCommand_ForwardsANestedRename(t *testing.T) {
	buf := captureWarnings(t)

	ran := 0
	apply := &cobra.Command{Use: "apply", RunE: func(c *cobra.Command, a []string) error { ran++; return nil }}
	group := &cobra.Command{Use: "file"}
	group.AddCommand(apply)

	root := &cobra.Command{Use: "drift"}
	root.AddCommand(group)
	group.AddCommand(AliasCommand(apply, "deploy",
		Deprecation{Old: "drift file deploy", New: "drift file apply", RemoveAfter: "v0.20.0"}))

	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"file", "deploy"})

	if err := root.Execute(); err != nil {
		t.Fatalf("the nested alias must forward: %v", err)
	}
	if ran != 1 {
		t.Errorf("the renamed command ran %d times, want 1", ran)
	}
	if !strings.Contains(buf.String(), "drift file apply") {
		t.Errorf("the notice must point at the new spelling:\n%s", buf.String())
	}
}
