package backbone

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// Every backbone verb addresses ONE slice's Backbone, so none of them may run
// without knowing which slice that is.
//
// The check lives on the group rather than in each RunE, and this is what makes
// that safe: `status` used to carry its own copy and no longer does, so if the
// group's check ever stops covering the group, nothing else is left holding it.
//
// It does not execute the verbs. Running them would need a platform, and a
// missing check would surface as a network call rather than as this failure —
// the wrong shape for a test whose subject is a guard that must fire FIRST.
func TestBackboneRefusesWithoutAnActiveSlice(t *testing.T) {
	// No override, and a HOME with no session file, so nothing can supply a
	// stored active slice.
	t.Setenv(common.SliceEnv, "")
	t.Setenv("HOME", t.TempDir())

	grp := GetCmd()
	if grp.PersistentPreRunE == nil {
		t.Fatal("the backbone group defines no PersistentPreRunE, so no verb under it " +
			"checks for an active slice and each one sends a request the api answers " +
			"against a slice literally named \"default\"")
	}

	err := grp.PersistentPreRunE(grp, nil)
	if err == nil {
		t.Fatal("with no active slice selected, the group's check admitted the command")
	}
	if !strings.Contains(err.Error(), "no slice to act on") {
		t.Errorf("the refusal does not say what is wrong.\n  got:  %v\n  want: something containing %q", err, "no slice to act on")
	}

	// The control. Without it the check could be refusing unconditionally and
	// this test would still pass, which would break every backbone command.
	t.Setenv(common.SliceEnv, "myapp")
	if err := grp.PersistentPreRunE(grp, nil); err != nil {
		t.Errorf("with an active slice the check still refused: %v", err)
	}
}

// A child's own PersistentPreRun SHADOWS the group's: cobra walks up from the
// command being run, executes the first one it finds, and stops. So a verb that
// grows its own hook silently loses the active-slice check, and the symptom is
// not a failure here but a request answered against the wrong slice.
func TestNoBackboneVerbShadowsTheGroupsPreRun(t *testing.T) {
	grp := GetCmd()

	verbs := grp.Commands()
	if len(verbs) == 0 {
		t.Fatal("the backbone group registers no subcommands, so this checked nothing")
	}

	for _, c := range verbs {
		if c.PersistentPreRunE != nil || c.PersistentPreRun != nil {
			t.Errorf("`drift backbone %s` defines its own PersistentPreRun, which shadows the "+
				"group's active-slice check — fold that work into the hook in backbone.go, "+
				"or call common.RequireActiveSlice from it too", c.Name())
		}
	}
}
