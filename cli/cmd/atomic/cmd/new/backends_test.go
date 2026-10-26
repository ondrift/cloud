package atomic_cmd_new

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// parsedSnippet is a snippet read back as HCL: the backend it declares, the
// function inside it, and each of the function's attributes as written.
type parsedSnippet struct {
	backend, function string
	attrs             map[string]string
	apiKeyEnv         string
}

func parseSnippet(t *testing.T, body string) parsedSnippet {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(body), "snippet", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("the snippet is not HCL: %v\n%s", diags, body)
	}
	top := f.Body.(*hclsyntax.Body).Blocks
	if len(top) != 1 || top[0].Type != "atomic" || len(top[0].Labels) != 0 {
		t.Fatalf("the snippet must be one atomic block, got:\n%s", body)
	}
	blocks := top[0].Body.Blocks
	if len(blocks) != 1 || blocks[0].Type != "backend" || len(blocks[0].Labels) != 1 {
		t.Fatalf("the atomic block must hold one backend block, got:\n%s", body)
	}
	el := blocks[0]
	if len(el.Body.Blocks) != 1 || el.Body.Blocks[0].Type != "function" || len(el.Body.Blocks[0].Labels) != 1 {
		t.Fatalf("the backend must hold one function block, got:\n%s", body)
	}
	p := el.Body.Blocks[0]
	out := parsedSnippet{backend: el.Labels[0], function: p.Labels[0], attrs: map[string]string{}}
	for name, attr := range p.Body.Attributes {
		v, d := attr.Expr.Value(nil)
		if d.HasErrors() || !v.Type().Equals(cty.String) {
			t.Fatalf("function attribute %s is not a string literal:\n%s", name, body)
		}
		out.attrs[name] = v.AsString()
	}
	for _, b := range p.Body.Blocks {
		if b.Type != "auth" {
			t.Fatalf("unexpected block %s in the function:\n%s", b.Type, body)
		}
		call, ok := b.Body.Attributes["api_key"].Expr.(*hclsyntax.FunctionCallExpr)
		if !ok || call.Name != "env" || len(call.Args) != 1 {
			t.Fatalf("the api_key is not env(\"NAME\"):\n%s", body)
		}
		v, _ := call.Args[0].Value(nil)
		out.apiKeyEnv = v.AsString()
	}
	return out
}

// A Function added to a named backend is declared inside that backend's block,
// with everything the compiler requires of it: its handler and its memory. An
// API-key gate reads its key from a variable named for the route, never from a
// literal.
func TestDeclarationSnippet_ANamedBackendHoldsTheFunction(t *testing.T) {
	got := declarationSnippet("train", "post", "post_train", "apikey", "ml")
	if !strings.Contains(got.intro, `backend "ml"`) {
		t.Errorf("the intro must name the backend, got %q", got.intro)
	}
	s := parseSnippet(t, got.body)
	if s.backend != "ml" || s.function != "POST /train" {
		t.Errorf("the snippet declares function %q in backend %q", s.function, s.backend)
	}
	if s.attrs["handler"] != "post_train" || s.attrs["memory"] != snippetMemory || len(s.attrs) != 2 {
		t.Errorf("the function reads back as %v", s.attrs)
	}
	if s.apiKeyEnv != "TRAIN_API_KEY" {
		t.Errorf("the key is read from %q, want TRAIN_API_KEY", s.apiKeyEnv)
	}
}

// The default backend is `backend "default"`, and a queue function is labelled
// with QUEUE and its queue. A function with no gate has no auth block.
func TestDeclarationSnippet_TheDefaultBackendAndAQueueFunction(t *testing.T) {
	for _, backend := range []string{"", defaultBackend} {
		got := declarationSnippet("jobs", "queue", "QueueJobs", "none", backend)
		s := parseSnippet(t, got.body)
		if s.backend != defaultBackend || s.function != "QUEUE jobs" || s.attrs["handler"] != "QueueJobs" || s.apiKeyEnv != "" {
			t.Errorf("backend %q: the snippet reads back as %+v", backend, s)
		}
	}
}

// A route's variable name keeps only what a variable name can hold.
func TestAPIKeyVariable(t *testing.T) {
	for route, want := range map[string]string{
		"train":         "TRAIN_API_KEY",
		"admin/reset":   "ADMIN_RESET_API_KEY",
		"groups/:id":    "GROUPS_ID_API_KEY",
		"send-email-v2": "SEND_EMAIL_V2_API_KEY",
	} {
		if got := apiKeyVariable(route); got != want {
			t.Errorf("apiKeyVariable(%q) = %q, want %q", route, got, want)
		}
	}
}
