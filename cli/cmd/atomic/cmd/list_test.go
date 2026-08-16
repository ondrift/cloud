package atomic_cmd

import (
	"net/http"
	"testing"
)

// A RESERVED slot is one the slice was sold and nothing has been deployed into
// yet. It carries a route and a method and no function name, because the name is
// written by the deploy that fills it.
//
// Hiding it makes the slots a tenant is billed for invisible until they happen
// to deploy: a slice created declaring three functions reserves three slots and
// reported "No functions deployed.", which is the one question `atomic list`
// exists to answer.
func TestFetchSlots_IncludesAReservedSlot(t *testing.T) {
	withAPI(t, http.StatusOK, `[
		{"_id":"atomic-1","function_name":"","method":"get","route":"alpha","language":"native"},
		{"_id":"atomic-2","function_name":"beta","method":"post","route":"beta","language":"node"}
	]`)

	slots, err := fetchSlots()
	if err != nil {
		t.Fatalf("listing slots: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("got %d slot(s), want 2 — the reserved one is being dropped", len(slots))
	}

	reserved := slots[0]
	if reserved.route() != "alpha" {
		t.Errorf("reserved slot route = %q, want %q", reserved.route(), "alpha")
	}
	if reserved.deployed() {
		t.Error("a slot with no function name is reserved, not deployed")
	}
	if !slots[1].deployed() {
		t.Error("a slot with a function name is deployed")
	}
}

// The control, and the behaviour the old filter was written for: the anonymous
// pre-warmed spare carries neither a name nor a route, is not something the
// tenant declared, and stays hidden.
func TestFetchSlots_HidesTheAnonymousSpare(t *testing.T) {
	withAPI(t, http.StatusOK, `[
		{"_id":"atomic-1","function_name":"","method":"","route":"","language":"native"}
	]`)

	slots, err := fetchSlots()
	if err != nil {
		t.Fatalf("listing slots: %v", err)
	}
	if len(slots) != 0 {
		t.Fatalf("got %d slot(s), want 0 — an unreserved spare is not a slot the tenant has", len(slots))
	}
}

// A reserved slot has no language and no deploy time, and must not borrow the
// defaults meant for a deployed one. `native` is the api's historical label for
// Go, so passing it through would label every reserved slot a Go function.
func TestReservedSlot_ClaimsNoLanguage(t *testing.T) {
	reserved := atomicRecord{Id: "atomic-1", Method: "get", Route: "alpha", Language: "native"}
	if got := langOrDefault(reserved); got != "—" {
		t.Errorf("reserved slot language = %q, want %q — nothing has been deployed into it", got, "—")
	}

	deployed := atomicRecord{Id: "atomic-2", FunctionName: "beta", Method: "post", Language: "native"}
	if got := langOrDefault(deployed); got != "go" {
		t.Errorf("deployed slot language = %q, want %q", got, "go")
	}
}
