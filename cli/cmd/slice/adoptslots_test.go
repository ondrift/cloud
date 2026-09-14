package slice

import "testing"

// A booking key is `method:route`, lowercased, everywhere a person sees one.
// These two are inverses and the rename depends on it: the halves are what the
// config stores, and the key is what the Driftfile and every refusal say.
func TestBookingKey_RoundTrips(t *testing.T) {
	for _, c := range []struct{ method, route string }{
		{"get", "hello"},
		{"post", "send-email"},
		{"delete", "groups/id"},
		{"get", "a/b/c"},
	} {
		key := bookingKey(c.method, c.route)
		method, route, ok := splitBooking(key)
		if !ok {
			t.Errorf("%q did not split back", key)
			continue
		}
		if method != c.method || route != c.route {
			t.Errorf("%q → (%q, %q), want (%q, %q)", key, method, route, c.method, c.route)
		}
	}
}

// The method is normalised, because the runtime looks a function up by an exact
// lowercase-method key. A slot renamed to `GET:hello` is a pool no invocation
// ever reaches.
func TestBookingKey_LowercasesTheMethod(t *testing.T) {
	if got := bookingKey("GET", "hello"); got != "get:hello" {
		t.Errorf("bookingKey(GET, hello) = %q, want get:hello", got)
	}
	method, _, _ := splitBooking("POST:things")
	if method != "post" {
		t.Errorf("splitBooking kept %q; the runtime matches lowercase exactly", method)
	}
}

// Not every string is a booking key, and a rename must refuse rather than
// invent halves. A bare route with no method is the shape `drift file new` used
// to write and the shape a user types by hand.
func TestSplitBooking_RefusesWhatIsNotAKey(t *testing.T) {
	for _, s := range []string{"hello", "", ":", ":route", "method:"} {
		if _, _, ok := splitBooking(s); ok {
			t.Errorf("%q was accepted as a booking key", s)
		}
	}
}

// declaredFunctions reads the config the server sends. A rename edits those
// entries in place, so this is the shape the whole feature rests on: `route`,
// `method` and `memory_bytes`, with no `name`.
func TestDeclaredFunctions_ReadsTheServersShape(t *testing.T) {
	cfg := map[string]any{
		"atomic": map[string]any{
			"functions": []any{
				map[string]any{"route": "slot-1", "method": "get", "memory_bytes": float64(16 << 20)},
				map[string]any{"route": "slot-2", "method": "get", "memory_bytes": float64(16 << 20)},
			},
		},
	}
	got := declaredFunctions(cfg)
	if len(got) != 2 {
		t.Fatalf("read %d functions, want 2", len(got))
	}
	if got[0].Route != "slot-1" || got[0].Method != "get" || got[0].MemoryBytes != 16<<20 {
		t.Errorf("first entry = %+v", got[0])
	}
}

// functionList reaches the MUTABLE entries, because a rename edits the config
// the server sent rather than rebuilding one. Rebuilding would re-encode every
// other dial on the slice through a second representation, and a rename must
// change exactly the fields it says it changes.
func TestFunctionList_ReachesTheEntriesInPlace(t *testing.T) {
	cfg := map[string]any{
		"atomic": map[string]any{
			"functions": []any{
				map[string]any{"route": "slot-1", "method": "get"},
			},
		},
	}
	list, err := functionList(cfg)
	if err != nil {
		t.Fatalf("functionList: %v", err)
	}
	list[0].(map[string]any)["route"] = "hello"

	// The edit must be visible through the original config, or the resize would
	// post the shape it started with.
	if got := declaredFunctions(cfg)[0].Route; got != "hello" {
		t.Errorf("the config still reads %q — the entries were copied, not reached", got)
	}
}

// A config with no atomic section, or a function list in a shape this version
// does not know, is refused rather than edited on a guess. The caller then
// prints the refusal it was going to print anyway.
func TestFunctionList_RefusesAShapeItCannotEdit(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"no atomic section": {},
		"atomic is not an object": {
			"atomic": "nope",
		},
		"functions is not a list": {
			"atomic": map[string]any{"functions": map[string]any{}},
		},
	} {
		if _, err := functionList(cfg); err == nil {
			t.Errorf("%s: was accepted for editing", name)
		}
	}
}

// Nothing to adopt is a QUESTION, not a failure — the caller falls back to the
// refusal naming the resize form. It must be told apart from a real error, or a
// slice with no spare slots would report a transport problem.
func TestAdoptIdleSlots_NothingWantedIsNotAnError(t *testing.T) {
	_, err := AdoptIdleSlots("any", nil, nil)
	if err != ErrNoIdleSlots {
		t.Errorf("got %v, want ErrNoIdleSlots — an empty want list is nothing to do", err)
	}
}
