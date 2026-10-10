package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withEnvironments turns the fxevm fixture into a file with a staging
// environment: the rpc Pulse's memory is a variable, 64MiB by default and
// 32MiB in staging.
func withEnvironments(extra string) func(string) string {
	return func(s string) string {
		s = strings.Replace(s, "concurrent = true\n    memory     = \"64MiB\"", "concurrent = true\n    memory     = var.rpc_memory", 1)
		return s + `
variable "rpc_memory" {
  default = "64MiB"
}

environment "staging" {
  billing = "monthly"
  vars    = { rpc_memory = "32MiB" }
}
` + extra
	}
}

// fixtureCopy writes the fxevm fixture, edited, into a fresh directory and
// returns its Driftfile's path.
func fixtureCopy(t *testing.T, edit func(string) string) string {
	t.Helper()
	dir := t.TempDir()
	if err := copyTree(filepath.Join(mustAbs(t, "testdata"), "fxevm"), dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Driftfile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(raw))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESET_KEY", "reset-key-4321")
	t.Setenv("EXPLORER_PASS", "explorer-pass")
	return path
}

func rpcMemory(c *Compiled) int {
	for _, f := range c.Shape.Atomic.Functions {
		if f.Name == "post:rpc" {
			return f.MemoryBytes
		}
	}
	return -1
}

func TestEnvironment_AnEnvironmentIsItsOwnSliceWithItsOwnValues(t *testing.T) {
	withRealSchema(t)
	path := fixtureCopy(t, withEnvironments(""))

	base, err := CompileDriftfile(path)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := CompileDriftfileEnv(path, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Manifest.Name(); got != "fxevm-testnode" {
		t.Errorf("the default slice is %q", got)
	}
	if got := staging.Manifest.Name(); got != "fxevm-testnode-staging" {
		t.Errorf("staging's slice is %q, want fxevm-testnode-staging", got)
	}
	if got := rpcMemory(base); got != 64*bytesPerMiB {
		t.Errorf("the default rpc booking is %d bytes, want 64MiB", got)
	}
	if got := rpcMemory(staging); got != 32*bytesPerMiB {
		t.Errorf("staging's rpc booking is %d bytes, want the 32MiB its vars set", got)
	}
	if staging.Environment != "staging" || base.Environment != "" {
		t.Errorf("Environment = %q / %q", staging.Environment, base.Environment)
	}
}

func TestEnvironment_AnEnvironmentMayNameItsSliceAndBilling(t *testing.T) {
	withRealSchema(t)
	path := fixtureCopy(t, withEnvironments(`
environment "demo" {
  slice   = "fxevm-demo"
  billing = "monthly"
}
`))
	c, err := CompileDriftfileEnv(path, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if c.Manifest.Name() != "fxevm-demo" || c.Billing != "monthly" {
		t.Errorf("demo compiled to slice %q, billing %q", c.Manifest.Name(), c.Billing)
	}
}

func TestEnvironment_AnUnknownEnvironmentIsRefusedNamingTheDeclaredOnes(t *testing.T) {
	withRealSchema(t)
	path := fixtureCopy(t, withEnvironments(""))
	_, err := CompileDriftfileEnv(path, "prod")
	if err == nil || !strings.Contains(err.Error(), `there is no environment "prod"`) || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("an unknown environment was not refused with the declared ones: %v", err)
	}
}

func TestEnvironment_AVariableWithNoValueIsAnErrorAtItsLine(t *testing.T) {
	withRealSchema(t)
	// No default: staging sets it, the default slice does not.
	path := fixtureCopy(t, func(s string) string {
		s = strings.Replace(s, "concurrent = true\n    memory     = \"64MiB\"", "concurrent = true\n    memory     = var.rpc_memory", 1)
		return s + `
variable "rpc_memory" {}

environment "staging" {
  billing = "monthly"
  vars    = { rpc_memory = "32MiB" }
}
`
	})
	if _, err := CompileDriftfileEnv(path, "staging"); err != nil {
		t.Fatalf("staging sets the variable, and still: %v", err)
	}
	_, err := CompileDriftfile(path)
	if err == nil || !strings.Contains(err.Error(), `variable "rpc_memory" has no value here`) {
		t.Fatalf("a variable nothing sets was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "Driftfile:") {
		t.Errorf("the refusal carries no line: %v", err)
	}
}

func TestEnvironment_PlanAndApplyAnEnvironmentAgainstItsOwnSlice(t *testing.T) {
	p, deployed := applyFixture(t, "", withEnvironments(""))

	out, err := planOutput(t, "--no-env-file", "staging")
	if err != nil {
		t.Fatalf("plan staging: %v", err)
	}
	for _, want := range []string{"environment staging → slice", "fxevm-testnode-staging", "+ create", "books 32MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan staging does not say %q:\n%s", want, out)
		}
	}

	a := noAnswers()
	a.price = 480 // (32 + 64 + 32 + 32) MiB at 3 cents a MiB, as the fake quotes it
	var aerr error
	captureStdout(t, func() {
		aerr = runApply(applyOptions{noEnvFile: true, answers: a, env: "staging", out: os.Stdout, in: strings.NewReader("")})
	})
	if aerr != nil {
		t.Fatalf("apply staging: %v", aerr)
	}
	bodies := p.bodies["POST /ops/slice/create"]
	if len(bodies) != 1 || bodies[0]["name"] != "fxevm-testnode-staging" {
		t.Fatalf("apply staging did not create staging's slice: %v", bodies)
	}
	if *deployed != 1 {
		t.Errorf("deployed %d times", *deployed)
	}
}

func TestEnvironment_APlanFileAppliesTheEnvironmentItWasPlannedFor(t *testing.T) {
	p, _ := applyFixture(t, "", withEnvironments(""))
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if _, err := planOutput(t, "--no-env-file", "--out", planFile, "staging"); err != nil {
		t.Fatalf("plan --out staging: %v", err)
	}
	if _, err := runApplyTest(t, noAnswers(), planFile); err != nil {
		t.Fatalf("apply plan.json: %v", err)
	}
	bodies := p.bodies["POST /ops/slice/create"]
	if len(bodies) != 1 || bodies[0]["name"] != "fxevm-testnode-staging" {
		t.Fatalf("the plan file's environment was not the one applied: %v", bodies)
	}
}
