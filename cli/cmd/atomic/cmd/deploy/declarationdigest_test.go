package atomic_cmd

import "testing"

// EVERY FIELD THE OPERATOR IS TOLD MUST MOVE THE DIGEST.
//
// A function whose digest matches is SKIPPED — the whole element is dropped
// before anything is staged, built or uploaded — so a field that does not move
// the digest is a field whose change never reaches the platform. The deploy
// prints `(unchanged)` beside it, which is true of the code and false of the
// function.
//
// The one that made this concrete: changing `auth: none` to `auth: apikey` and
// re-applying left the function open, silently.
func TestDeclarationDigest_EveryDeclaredFieldMovesIt(t *testing.T) {
	base := FunctionSpec{
		Name: "get:hello", Handler: "GetHello", Element: "default",
		Auth: "none", Stream: "", Response: "envelope",
		Secrets: []string{"API_KEY"},
		Env:     map[string]string{"REGION": "eu"},
	}
	start := DeclarationDigest(base)

	for name, mutate := range map[string]func(*FunctionSpec){
		"the gate":           func(s *FunctionSpec) { s.Auth = "apikey" },
		"the stream mode":    func(s *FunctionSpec) { s.Stream = "sse" },
		"the reply shape":    func(s *FunctionSpec) { s.Response = "raw" },
		"the handler":        func(s *FunctionSpec) { s.Handler = "GetGoodbye" },
		"the element":        func(s *FunctionSpec) { s.Element = "billing" },
		"a secret added":     func(s *FunctionSpec) { s.Secrets = []string{"API_KEY", "DB_URL"} },
		"a secret removed":   func(s *FunctionSpec) { s.Secrets = nil },
		"a secret renamed":   func(s *FunctionSpec) { s.Secrets = []string{"OTHER_KEY"} },
		"an env value":       func(s *FunctionSpec) { s.Env = map[string]string{"REGION": "us"} },
		"an env key added":   func(s *FunctionSpec) { s.Env = map[string]string{"REGION": "eu", "TIER": "free"} },
		"an env key removed": func(s *FunctionSpec) { s.Env = nil },
	} {
		spec := base
		spec.Secrets = append([]string(nil), base.Secrets...)
		spec.Env = map[string]string{}
		for k, v := range base.Env {
			spec.Env[k] = v
		}
		mutate(&spec)

		if DeclarationDigest(spec) == start {
			t.Errorf("changing %s did not move the digest — the function is SKIPPED on the "+
				"next apply and the change never reaches the operator", name)
		}
	}
}

// THE `cron:` ONE, which is how this was found.
//
// It does not live on the spec — the manifest publishes schedules to a
// package-level registry (see schedules.go) — so it is the field most easily
// left out of a digest built by reading the struct.
func TestDeclarationDigest_TheDeclaredCronMovesIt(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	spec := FunctionSpec{Name: "get:tick", Handler: "GetTick"}

	SetDeclaredSchedules(nil)
	without := DeclarationDigest(spec)

	SetDeclaredSchedules(map[string]string{"get:tick": "*/1 * * * *"})
	with := DeclarationDigest(spec)

	if with == without {
		t.Fatal("adding a cron did not move the digest — the function is skipped on the next " +
			"apply and the schedule is never registered")
	}

	SetDeclaredSchedules(map[string]string{"get:tick": "0 2 * * *"})
	if DeclarationDigest(spec) == with {
		t.Error("CHANGING a cron did not move the digest — the schedule keeps the old expression")
	}
}

// Stable across reorderings that mean nothing. `secrets` is a set, and a digest
// that flipped on a reordered list would report a change nobody made — which
// costs a rebuild every time somebody tidies their Driftfile.
func TestDeclarationDigest_IsStableAcrossMeaninglessReordering(t *testing.T) {
	a := FunctionSpec{
		Name: "get:hello", Secrets: []string{"A", "B", "C"},
		Env: map[string]string{"X": "1", "Y": "2"},
	}
	b := FunctionSpec{
		Name: "get:hello", Secrets: []string{"C", "A", "B"},
		Env: map[string]string{"Y": "2", "X": "1"},
	}
	if DeclarationDigest(a) != DeclarationDigest(b) {
		t.Error("the same declaration written in a different order hashed differently")
	}
}

// `dir` is where the source sits on THIS machine and is never sent. Folding it
// in would make the same project redeploy for every developer who checked it out
// somewhere else.
func TestDeclarationDigest_IgnoresTheLocalDirectory(t *testing.T) {
	a := FunctionSpec{Name: "get:hello", Dir: "/home/alice/proj/atomic"}
	b := FunctionSpec{Name: "get:hello", Dir: "/Users/bob/work/proj/atomic"}
	if DeclarationDigest(a) != DeclarationDigest(b) {
		t.Error("the local source path moved the digest — the same project would redeploy " +
			"on every machine that checked it out to a different path")
	}
}

// DeployDigest is what both sides use, and an unknown build must stay unknown.
// An empty build digest already means "not skippable" to every caller; hashing
// the emptiness into a real value would turn it into something that can match.
func TestDeployDigest_AnUnknownBuildStaysUnmatchable(t *testing.T) {
	spec := FunctionSpec{Name: "get:hello"}
	if got := DeployDigest("", spec); got != "" {
		t.Errorf("an empty build digest produced %q — that value can match, so a function "+
			"whose source could not be hashed would start skipping", got)
	}
}

// And it moves with EITHER half, which is the whole point of combining them.
func TestDeployDigest_MovesWithTheBuildAndWithTheDeclaration(t *testing.T) {
	spec := FunctionSpec{Name: "get:hello", Auth: "none"}
	start := DeployDigest("build-1", spec)

	if DeployDigest("build-2", spec) == start {
		t.Error("a source change did not move the deploy digest")
	}
	changed := spec
	changed.Auth = "apikey"
	if DeployDigest("build-1", changed) == start {
		t.Error("a declaration change did not move the deploy digest")
	}
}
