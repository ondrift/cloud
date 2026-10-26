package project

import "testing"

// A clone is deep: writing through it never reaches the document it copied.
func TestNodeCloneIsDeep(t *testing.T) {
	base := Node{"atomic": Node{"functions": []any{Node{"name": "get:a"}}}}
	c := base.Clone()
	c.Sub("atomic").Nodes("functions")[0]["name"] = "get:b"
	if got := base.Sub("atomic").Nodes("functions")[0].Str("name"); got != "get:a" {
		t.Errorf("a write through the clone reached the original: %q", got)
	}
}

func TestNodeAccessorsAreTotal(t *testing.T) {
	n := Node{
		"name": "demo",
		"atomic": Node{
			"deploy_history": 3,
			"functions":      []any{Node{"name": "hello"}, "not-an-object"},
		},
		"canvas": Node{"sites": []any{"./a", "./b"}},
	}

	if n.Str("name") != "demo" {
		t.Error("Str")
	}
	if n.Int("atomic", "deploy_history") != 3 {
		t.Error("Int")
	}
	if got := n.Nodes("atomic", "functions"); len(got) != 1 || got[0].Str("name") != "hello" {
		t.Errorf("Nodes should skip non-objects, got %v", got)
	}
	if got := n.Strings("canvas", "sites"); len(got) != 2 {
		t.Errorf("Strings, got %v", got)
	}

	// Missing and wrongly-typed paths yield zero values, never panics — the
	// schema is what rejects a bad document, not these readers.
	if n.Str("nope", "deeper") != "" || n.Int("name") != 0 || n.Sub("nope") == nil {
		t.Error("accessors must be total")
	}
	if len(n.Nodes("nope")) != 0 {
		t.Error("a missing list is empty, not a panic")
	}
}

// Has is what makes present-and-zero distinguishable from absent.
func TestNodeHas(t *testing.T) {
	n := Node{"atomic": Node{"deploy_history": 0, "rate_limit": ""}}

	if !n.Has("atomic", "deploy_history") {
		t.Error("a key set to 0 is still present")
	}
	if !n.Has("atomic", "rate_limit") {
		t.Error(`a key set to "" is still present`)
	}
	if n.Has("atomic", "never_set") {
		t.Error("an absent key must not report present")
	}
}

func TestNodeSet(t *testing.T) {
	n := Node{}
	n.Set("prod-app", "name")
	n.Set("256MB", "atomic", "function_timeout")

	if n.Str("name") != "prod-app" {
		t.Error("Set at the root")
	}
	if n.Str("atomic", "function_timeout") != "256MB" {
		t.Error("Set should create intermediate objects")
	}
}
