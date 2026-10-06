package atomic_cmd_new

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A Pulse added to a named element is declared inside that element's block in
// `atomic.elements`, not as a tagged top-level entry, and the snippet is YAML
// that reads back as exactly that.
func TestDeclarationSnippet_ANamedElementIsNested(t *testing.T) {
	got := declarationSnippet("train", "post", "post_train", "apikey", "ml")

	for _, want := range []string{"atomic.elements", `"ml"`} {
		if !strings.Contains(got.intro, want) {
			t.Errorf("the intro must name %s, got %q", want, got.intro)
		}
	}
	if strings.Contains(got.body, "element:") {
		t.Errorf("the snippet still tags the entry with element:\n%s", got.body)
	}

	var parsed []struct {
		Name   string `yaml:"name"`
		Pulses []struct {
			Route, Method, Handler, Auth string
		} `yaml:"pulses"`
	}
	if err := yaml.Unmarshal([]byte(got.body), &parsed); err != nil {
		t.Fatalf("the snippet is not YAML: %v\n%s", err, got.body)
	}
	if len(parsed) != 1 || parsed[0].Name != "ml" || len(parsed[0].Pulses) != 1 {
		t.Fatalf("the snippet must be one element holding one pulse, got %+v\n%s", parsed, got.body)
	}
	p := parsed[0].Pulses[0]
	if p.Route != "train" || p.Method != "post" || p.Handler != "post_train" || p.Auth != "apikey" {
		t.Errorf("the pulse reads back as %+v", p)
	}
}

// The default element is the top-level list, as before.
func TestDeclarationSnippet_TheDefaultElementIsTheTopLevelList(t *testing.T) {
	got := declarationSnippet("jobs", "queue", "QueueJobs", "none", defaultElement)
	if !strings.Contains(got.intro, "atomic.pulses") || strings.Contains(got.intro, "elements") {
		t.Errorf("the default element's intro must name atomic.pulses alone, got %q", got.intro)
	}
	var parsed []map[string]string
	if err := yaml.Unmarshal([]byte(got.body), &parsed); err != nil {
		t.Fatalf("the snippet is not YAML: %v\n%s", err, got.body)
	}
	if len(parsed) != 1 || parsed[0]["route"] != "jobs" || parsed[0]["method"] != "queue" ||
		parsed[0]["handler"] != "QueueJobs" || len(parsed[0]) != 3 {
		t.Errorf("the default element's snippet is one bare pulse entry, got %+v", parsed)
	}
}

// A Pulse already declared inside an element is recognised, so the user is not
// handed a duplicate of it.
func TestDeclaredRouteFindsAPulseInsideAnElement(t *testing.T) {
	inDir(t, "slice: acme\natomic:\n  elements:\n    - name: ml\n      pulses:\n"+
		"        - route: train\n          method: post\n          handler: post_train\n")
	if !declaredRoute("post", "train", "", false) {
		t.Error("a pulse declared inside an element was not recognised")
	}
	if declaredRoute("get", "train", "", false) {
		t.Error("a different method was treated as declared")
	}
}
