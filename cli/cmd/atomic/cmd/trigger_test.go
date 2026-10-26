package atomic_cmd

import (
	"strings"
	"testing"
)

// A trigger is declared in the Driftfile, so nothing under `drift atomic trigger`
// registers one. `list` and `unregister` stay: the first shows what the slice
// runs, the second removes a trigger the file no longer declares, which the
// Driftfile has no way to say.
func TestTriggerHasNoRegisterVerb(t *testing.T) {
	var names []string
	for _, c := range Trigger().Commands() {
		names = append(names, c.Name())
	}
	if got := strings.Join(names, ","); got != "list,unregister" {
		t.Errorf("`drift atomic trigger` has subcommands %q, want list,unregister", got)
	}
}
