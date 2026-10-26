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
	withRealSchema(t)
	m := planManifest(t, "slice \"demo\" {}\n\natomic {\n  backend \"default\" {\n"+
		"    function \"POST /token\" {\n      handler = \"PostToken\"\n      memory = \"16MiB\"\n      response = \"json\"\n    }\n"+
		"    function \"GET /ping\" {\n      handler = \"GetPing\"\n      memory = \"16MiB\"\n    }\n  }\n}\n", "atomic/")

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

	// The control, and the reason the worker is optional: a function that
	// declares nothing must resolve to nothing, not to a default spelled out
	// here. The slice reads absent as envelope, and two places inventing that
	// default is how they come to disagree.
	if got := byName["get:ping"]; got != "" {
		t.Errorf("get:ping resolved response %q, want empty — an undeclared shape must stay "+
			"undeclared rather than being filled in on this side", got)
	}
}
