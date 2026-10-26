package file

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resolveFile = `variable "rpc_memory" { default = "64MiB" }

slice "demo" {
  limits {
    timeout        = "30s"
    deploy_history = 3
  }
}

atomic {
  backend "default" {
    function "POST /rpc" {
      handler = "PostRPC"
      memory  = var.rpc_memory
    }
  }
}

backbone {
  nosql "events" { size = "20MiB" }
  queue "jobs" {}
  secret "STRIPE_KEY" { value = env("STRIPE_KEY") }
}

edge {
  rate_limit = "600/min"
  allow      = ["203.0.113.0/24", "2001:db8::/32"]
  hosts      = { "admin.example.com" = "admin" }
}

environment "staging" {
  vars = { rpc_memory = "32MiB" }
}
`

// resolveOut runs `drift file resolve` on a Driftfile and returns what it printed.
func resolveOut(t *testing.T, body string, args ...string) string {
	t.Helper()
	withServedSchema(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "atomic"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	restore := redirectStdout(t, &out)
	cmd := getResolveCmd()
	cmd.SetArgs(append([]string{dir}, args...))
	err := cmd.Execute()
	restore()
	if err != nil {
		t.Fatalf("resolve %v: %v", args, err)
	}
	return out.String()
}

// resolve prints what the file provisions: each booking beside its slot, each
// storage size, the limits, and the edge as the file resolves it. A secret is
// named by where it is read from, never by a value.
func TestResolve_PrintsTheResolvedShape(t *testing.T) {
	t.Setenv("STRIPE_KEY", "sk_live_never_printed")
	out := resolveOut(t, resolveFile)
	for _, want := range []string{
		"slice demo  (priced)",
		"function POST /rpc", "64MiB",
		"events", "20MiB",
		"jobs", "the slice's queue depth",
		"limits.timeout", "30s",
		"limits.deploy_history", "3",
		"limits.requests_per_minute", "600",
		"inbound allowlist (2)", "· 203.0.113.0/24", "· 2001:db8::/32",
		"fronted hosts (1)", "· admin.example.com → admin",
		"secret STRIPE_KEY", "$STRIPE_KEY",
		"environments: staging",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resolve does not print %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk_live_never_printed") {
		t.Error("RESOLVE PRINTED A SECRET'S VALUE")
	}
}

// An environment resolves to its own slice, with its own variables.
func TestResolve_AnEnvironmentResolvesToItsOwnSlice(t *testing.T) {
	out := resolveOut(t, resolveFile, "--env", "staging")
	for _, want := range []string{"slice demo-staging", "environment staging", "32MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("resolve --env staging does not print %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "64MiB") {
		t.Errorf("staging's booking is the variable it sets, not the default:\n%s", out)
	}
}

// An empty allowlist is a declaration, and resolves to a slice every address
// reaches.
func TestResolve_SaysAnEmptyAllowlistAdmitsEveryone(t *testing.T) {
	out := resolveOut(t, "slice \"demo\" {}\nedge { allow = [] }\n")
	if !strings.Contains(out, "inbound allowlist (0)") || !strings.Contains(out, "every address reaches the slice") {
		t.Errorf("resolve does not say an empty allowlist admits everyone:\n%s", out)
	}
}

// redirectStdout captures what the command prints, which goes to os.Stdout
// rather than to the command's own writer.
func redirectStdout(t *testing.T, into *bytes.Buffer) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan struct{})
	go func() {
		_, _ = into.ReadFrom(r)
		close(done)
	}()

	return func() {
		os.Stdout = prev
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}
