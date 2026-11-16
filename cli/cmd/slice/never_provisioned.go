package slice

import "fmt"

// Where a never-provisioned slice is being named. The platform decides that a
// slice never provisioned and sends it as `never_provisioned`; the CLI reads the
// flag and says what clears it, and says it in one place.
type NeverProvisionedPlace int

const (
	// ListRow is the note on a slice's row in `drift slice list`.
	ListRow NeverProvisionedPlace = iota
	// PlanStop is the refusal of `drift plan` and `drift apply`, which stop before
	// reading or sending anything.
	PlanStop
	// SlotRefusal is the clause beside a free slice in the refusal that the
	// account holds every free slice it may.
	SlotRefusal
)

// NeverProvisioned says that the slice never provisioned and names the delete
// that clears it. The delete is the person's: nothing here runs it.
func NeverProvisioned(name string, where NeverProvisionedPlace) string {
	const state = "never provisioned"
	fix := "drift slice delete " + name
	switch where {
	case PlanStop:
		return fmt.Sprintf("slice %s was %s; nothing was applied. Run %s, then apply again", name, state, fix)
	case SlotRefusal:
		return fmt.Sprintf("%s: `%s` gives the slot back", state, fix)
	default:
		return state + ": " + fix
	}
}
