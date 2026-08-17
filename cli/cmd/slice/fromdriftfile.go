package slice

// fromdriftfile.go — the three verbs that used to build a slice's shape from a
// Driftfile, kept working as handoffs to the configurator.
//
// `create --from`, `resize --from` and `shrink` existed to read a shape out of a
// manifest and post it. The configurator owns a slice's shape now, and the
// Driftfile no longer carries one, so their input is gone. They are not deleted:
// a user-facing name is deprecated, kept working, and only then removed.
//
// What "kept working" can honestly mean here is the handoff. Each reads the ONE
// thing the manifest still says about a slice — which slice it is — and opens
// the configurator on it. The shape the user then picks is the configurator's,
// which is the point of the change rather than a limitation of the shim.
//
// They read the identity through project.ParseProjectName, the cheap decode that
// resolves `slice:` and the retired `name:` without validating the document or
// resolving its secrets. A manifest naming a slice is enough to open a form; a
// manifest that would fail a full parse should still get you there, because the
// form is where you would go to fix it.

import (
	"fmt"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
)

// removeAfterShapeIsConfiguratorOwned is the condition that ends these shims.
//
// A condition rather than a version, matching the house style: a version named
// in a notice is a promise about a release nobody has planned. This one is
// countable — the fleet is eight slices — rather than a judgement.
const removeAfterShapeIsConfiguratorOwned = "every live slice's shape is configurator-owned"

// handoffFromDriftfile opens the configurator on the slice a manifest names.
//
// `existing` is the live config when there is one, forwarded verbatim so the
// form opens on what the slice actually is. Nil for a create, which has nothing
// to pre-fill.
func handoffFromDriftfile(op, path string, mode handoffMode) error {
	if path == "" {
		path = "Driftfile"
	}
	name, err := project.ParseProjectName(path)
	if err != nil {
		return err
	}

	var existing any
	if mode == modeResize {
		// Best-effort: the form is more useful pre-filled, and a slice that
		// cannot be read is a problem the configurator will state better than a
		// pre-flight here would.
		if cfg, ferr := fetchSliceConfig(name); ferr == nil {
			existing = cfg
		}
	}

	result, err := runBrowserHandoff(op, name, mode, existing)
	if err != nil {
		return err
	}
	printSliceSummary(pastTense(mode), result)

	if mode == modeCreate {
		if created := sliceNameFrom(result); created != "" {
			if serr := common.SaveActiveSlice(created); serr != nil {
				fmt.Println("Warning: couldn't mark the new slice as active —", serr)
			}
		}
	}
	return nil
}

func pastTense(mode handoffMode) string {
	if mode == modeCreate {
		return "created"
	}
	return "resized"
}
