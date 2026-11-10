package atomic

import "testing"

// A function is removed by dropping its block from the Driftfile and running
// `drift apply`, so no command deletes one by name. A script still calling
// `drift atomic delete` fails as an unknown command rather than reaching an
// endpoint, and nothing here quietly brings the verb back.
func TestAtomicHasNoDeleteCommand(t *testing.T) {
	for _, c := range GetCmd().Commands() {
		if c.Name() == "delete" {
			t.Fatalf("`drift atomic delete` exists again: a function is removed by dropping it from the Driftfile and applying")
		}
	}
}
