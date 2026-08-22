package project

import "testing"

// `response` has to survive the CLI's own hop, which is the FIRST of three
// parsers between the Driftfile and the running function.
//
// Each hop builds its own struct and drops what it has no field for, and none
// of them fails when that happens: the deploy reports success and the endpoint
// answers in the old shape. The only symptom is a third party unable to parse a
// body — which is why the field is asserted at every hop rather than trusted at
// any of them.
func TestFunctionSpecCarriesTheDeclaredResponseShape(t *testing.T) {
	m := planManifest(t, "slice: demo\natomic:\n  functions:\n"+
		"    - route: token\n      method: post\n      handler: PostToken\n      response: json\n"+
		"    - route: ping\n      method: get\n      handler: GetPing\n")

	specs := FunctionSpecs(m)
	if len(specs) != 2 {
		t.Fatalf("resolved %d functions, want 2", len(specs))
	}

	byName := map[string]string{}
	for _, s := range specs {
		byName[s.Name] = s.Response
	}

	if got := byName["post:token"]; got != "json" {
		t.Errorf("post:token resolved response %q, want \"json\" — the declaration is "+
			"dropped at the first hop, so the slice never learns the endpoint is unwrapped", got)
	}

	// The control, and the reason the field is optional: a function that
	// declares nothing must resolve to nothing, not to a default spelled out
	// here. The slice reads absent as envelope, and two places inventing that
	// default is how they come to disagree.
	if got := byName["get:ping"]; got != "" {
		t.Errorf("get:ping resolved response %q, want empty — an undeclared shape must stay "+
			"undeclared rather than being filled in on this side", got)
	}
}
