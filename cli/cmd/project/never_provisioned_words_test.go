package project

import (
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/cmd/slice"
)

// The list row, the plan's stop and the free-slot refusal are one sentence built
// once: each carries the same state and the same delete.
func TestNeverProvisioned_ListPlanAndRefusalSayTheSameWords(t *testing.T) {
	const name = "stuck"
	free := FreeSlots{Limit: 1, Held: []FreeSliceRef{{Name: name, Slot: 1, Failed: true}}}
	for site, text := range map[string]string{
		"the list row":  slice.NeverProvisioned(name, slice.ListRow),
		"the plan stop": slice.NeverProvisioned(name, slice.PlanStop),
		"the refusal":   free.fullRefusal(),
	} {
		for _, want := range []string{"never provisioned", "drift slice delete " + name} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q: %s", site, want, text)
			}
		}
	}
}
