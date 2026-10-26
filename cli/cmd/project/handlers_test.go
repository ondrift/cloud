package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// demoProject writes a project whose Driftfile declares four functions across
// two elements, including a queue-triggered one, and returns its root.
func demoProject(t *testing.T, driftfile string) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Driftfile"), driftfile)
	mustWrite(t, filepath.Join(root, "atomic", "api.go"), `package main

func GetPing(req any) {}

func PostUsers(body any, req any) {}

func HandleOrder(body any, req any) {}

func normalise(s string) string { return s }
`)
	mustWrite(t, filepath.Join(root, "atomic", "billing", "invoices.go"),
		"package main\n\nfunc PostInvoices(body any, req any) {}\n")
	mustMkdir(t, filepath.Join(root, "canvas"))
	return root
}

const demoDriftfile = `slice "demo" {}

atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "GetPing"
      memory = "8MiB"
    }
    pulse "POST /users" {
      handler = "PostUsers"
      memory = "32MiB"
      secrets = ["STRIPE_KEY"]
      auth {
        api_key = env("USERS_KEY")
      }
    }
    pulse "QUEUE orders" {
      handler = "HandleOrder"
      memory = "64MiB"
    }
  }

  element "billing" {
    pulse "POST /invoices" {
      handler = "PostInvoices"
      memory = "48MiB"
    }
  }
}

canvas {
  site "/" { dir = "canvas" }
}
`

// The manifest is the whole declaration, and this is the seam that proves it:
// a Driftfile in, deployable functions out, with the gate and the secrets
// carried through and the source consulted only to locate each handler.
func TestFunctionSpecs_ResolveTheWholeDeclaration(t *testing.T) {
	withRealSchema(t)
	root := demoProject(t, demoDriftfile)
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}

	specs := FunctionSpecs(m)
	if len(specs) != 4 {
		t.Fatalf("got %d specs, want 4: %+v", len(specs), specs)
	}

	byName := map[string]atomic_cmd.FunctionSpec{}
	for _, s := range specs {
		byName[s.Name] = s
	}

	users := byName["post:users"]
	if users.Auth != "apikey" {
		t.Errorf("post:users auth = %q, want apikey — the gate is the manifest's to state", users.Auth)
	}
	if len(users.Secrets) != 1 || users.Secrets[0] != "STRIPE_KEY" {
		t.Errorf("post:users secrets = %v, want [STRIPE_KEY]", users.Secrets)
	}

	// An omitted gate is not a locked one.
	if ping := byName["get:ping"]; ping.Auth != "" {
		t.Errorf("get:ping auth = %q, want empty (the platform default is `none`)", ping.Auth)
	}

	// The element owns the directory; the default element is atomic/ itself.
	wantDefault, _ := filepath.Abs(filepath.Join(root, "atomic"))
	wantBilling, _ := filepath.Abs(filepath.Join(root, "atomic", "billing"))
	if got := byName["get:ping"].Dir; got != wantDefault {
		t.Errorf("get:ping dir = %q, want %q", got, wantDefault)
	}
	if got := byName["post:invoices"].Dir; got != wantBilling {
		t.Errorf("post:invoices dir = %q, want %q", got, wantBilling)
	}
}

// Elements group by what the manifest says, and every declared handler binds to
// the file that declares it — across two elements, and for a queue function
// that has no route at all.
func TestFunctionSpecs_BuildIntoElements(t *testing.T) {
	withRealSchema(t)
	root := demoProject(t, demoDriftfile)
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}

	els, err := atomic_cmd.BuildElements(FunctionSpecs(m))
	if err != nil {
		t.Fatalf("BuildElements: %v", err)
	}
	if len(els) != 2 {
		t.Fatalf("got %d elements, want 2 (billing + default): %+v", len(els), els)
	}
	billing, def := els[0], els[1] // sorted by name
	if billing.Name != "billing" || len(billing.Funcs) != 1 {
		t.Errorf("billing = %+v, want 1 function", billing)
	}
	if def.Name != atomic_cmd.DefaultElementName || len(def.Funcs) != 3 {
		t.Errorf("default = %+v, want 3 functions", def)
	}
	for _, f := range def.Funcs {
		if f.SourceFile != "api.go" {
			t.Errorf("%s bound to %q, want api.go", f.Spec.Name, f.SourceFile)
		}
	}

	// `normalise` is a callable the manifest never named: a helper, free and
	// unrouted. Nothing about sharing a file with three handlers changes that.
	if n := len(def.Funcs) + len(billing.Funcs); n != 4 {
		t.Errorf("got %d functions across the project, want the 4 declared — a helper is not a function", n)
	}
}

// --- declared configuration -------------------------------------------------

const envDriftfile = `slice "demo" {}

atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "GetPing"
      memory = "8MiB"
      env = { REGION = "eu-central", LOG_LEVEL = "debug" }
    }
    pulse "POST /users" {
      handler = "PostUsers"
      memory = "32MiB"
      secrets = ["STRIPE_KEY"]
    }
  }
}

canvas {
  site "/" { dir = "canvas" }
}
`

// THE seam this exists for, and the one that was missing when the other four
// owners were already done: a Driftfile's `env` reaching FunctionSpec.
//
// It is worth a test precisely because failing it is silent. Every other owner
// on the path drops an unknown field without complaint, so a manifest declaring
// `env` would lint clean, deploy clean, and leave the variable simply absent
// inside the function with nothing anywhere naming it.
func TestFunctionSpecs_CarryDeclaredEnv(t *testing.T) {
	withRealSchema(t)
	root := demoProject(t, envDriftfile)
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}

	byName := map[string]atomic_cmd.FunctionSpec{}
	for _, s := range FunctionSpecs(m) {
		byName[s.Name] = s
	}

	ping := byName["get:ping"]
	if len(ping.Env) != 2 {
		t.Fatalf("get:ping env = %v, want both declared keys", ping.Env)
	}
	if ping.Env["REGION"] != "eu-central" || ping.Env["LOG_LEVEL"] != "debug" {
		t.Errorf("get:ping env = %v, want REGION=eu-central LOG_LEVEL=debug", ping.Env)
	}

	// The control, and it is not decoration: without it an accessor that
	// returned the same map for every function would satisfy the assertion
	// above. `env:` and `secrets:` are separate channels, which is the whole
	// point of the field — a function declaring one must not acquire the other.
	users := byName["post:users"]
	if len(users.Env) != 0 {
		t.Errorf("post:users declares no env, got %v", users.Env)
	}
	if len(users.Secrets) != 1 || users.Secrets[0] != "STRIPE_KEY" {
		t.Errorf("post:users secrets = %v, want [STRIPE_KEY] — env must not displace it", users.Secrets)
	}
	if len(ping.Secrets) != 0 {
		t.Errorf("get:ping declares no secrets, got %v — a config key is not a credential", ping.Secrets)
	}
}

// --- concurrency --------------------------------------------------------------

// concurrencyDriftfile declares `concurrent` on a top-level Pulse and on a
// Pulse inside an element, and leaves it out of a third.
const concurrencyDriftfile = `slice "demo" {}

atomic {
  element "default" {
    pulse "POST /users" {
      handler = "PostUsers"
      memory = "16MiB"
      concurrent = true
    }
    pulse "GET /ping" {
      handler = "GetPing"
      memory = "16MiB"
    }
  }

  element "billing" {
    pulse "POST /invoices" {
      handler = "PostInvoices"
      memory = "16MiB"
      concurrent = true
    }
  }
}
`

// withFormatSchema validates against the platform's schema, as a laptop that
// has met the api holds it.
func withFormatSchema(t *testing.T) {
	t.Helper()
	withRealSchema(t)
}

// parseNoting parses a project's Driftfile and returns what it was told about
// retired keys, from a clean slate of notices.
func parseNoting(t *testing.T, root string) (*Manifest, string) {
	t.Helper()
	common.ResetDeprecationState()
	var notices strings.Builder
	restore := common.RedirectDeprecationWarnings(&notices)
	t.Cleanup(restore)
	t.Cleanup(common.ResetDeprecationState)
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}
	return m, notices.String()
}

// `concurrent` reaches FunctionSpec from a top-level Pulse and from one
// flattened out of an element, a Pulse that declares none is not concurrent,
// and nothing is said about a retired key.
func TestFunctionSpecs_CarryDeclaredConcurrency(t *testing.T) {
	withFormatSchema(t)
	m, notices := parseNoting(t, demoProject(t, concurrencyDriftfile))
	got := map[string]bool{}
	for _, s := range FunctionSpecs(m) {
		got[s.Name] = s.IsConcurrent()
	}
	for name, want := range map[string]bool{"post:users": true, "post:invoices": true, "get:ping": false} {
		if c, ok := got[name]; !ok || c != want {
			t.Errorf("%s concurrent = %v (declared: %v), want %v; all: %v", name, c, ok, want, got)
		}
	}
	if _, err := atomic_cmd.BuildElements(FunctionSpecs(m)); err != nil {
		t.Errorf("concurrent Go Pulses were refused: %v", err)
	}
	if notices != "" {
		t.Errorf("a Driftfile writing `concurrent` was told about a retired key: %q", notices)
	}
}

// A Python element's Pulse declared `concurrent = true` passes the schema,
// which cannot see the language, and is refused before anything builds,
// naming the rule; the error is one `drift apply` reports before
// deploying anything.
func TestFunctionSpecs_ConcurrencyOnAPythonPulseIsRefusedBeforeBuilding(t *testing.T) {
	withFormatSchema(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Driftfile"), `slice "demo" {}

atomic {
  element "py" {
    pulse "POST /rpc" {
      handler = "post_rpc"
      memory = "16MiB"
      concurrent = true
    }
  }
}
`)
	mustWrite(t, filepath.Join(root, "atomic", "py", "api.py"), "def post_rpc(body, req):\n    return {}\n")
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}
	_, err = atomic_cmd.BuildElements(FunctionSpecs(m))
	if !errors.Is(err, atomic_cmd.ErrConcurrency) {
		t.Fatalf("want the concurrency refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "post:rpc") || !strings.Contains(err.Error(), "a concurrent Pulse runs Go in this release") {
		t.Errorf("the refusal must name the pulse and the rule: %v", err)
	}
}

// The compiler and the schema refuse what they can see: a concurrent Pulse on
// a stream, and `concurrent` as a number.
func TestParseDriftfile_TheSchemaRefusesAConcurrencyItCanSee(t *testing.T) {
	withFormatSchema(t)
	pulse := func(entry string) string {
		return "slice \"demo\" {}\n\natomic {\n  element \"default\" {\n    pulse \"GET /ping\" {\n" +
			"      handler = \"GetPing\"\n      memory = \"16MiB\"\n      " + entry + "\n    }\n  }\n}\n"
	}
	// The controls: the same Pulse concurrent or not is accepted, so a refusal
	// below is about the value and not the fixture.
	for _, entry := range []string{"concurrent = true", "concurrent = false"} {
		if _, err := ParseDriftfile(filepath.Join(demoProject(t, pulse(entry)), "Driftfile")); err != nil {
			t.Fatalf("a Pulse declaring %s was refused: %v", entry, err)
		}
	}
	for name, entry := range map[string]string{
		"concurrent with a stream": "stream = \"sse\"\n    concurrent = true",
		"concurrent as a number":   "concurrent = 16",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDriftfile(filepath.Join(demoProject(t, pulse(entry)), "Driftfile")); err == nil {
				t.Errorf("a Pulse declaring %s was accepted", entry)
			}
		})
	}
}

// --- the compiled floor -----------------------------------------------------

// A schema stub carrying only what CompiledMemoryFloor reads. Permissive
// everywhere else: these tests are about the floor, and a full copy of the format
// here would be the second definition the CLI exists without.
const floorSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "definitions": {
    "atomicEntry": {
      "properties": {
        "memory": {
          "type": "string",
          "x-drift-compiled-minimum": "16MB",
          "x-drift-interpreted-languages": ["node", "php", "python", "ruby"]
        }
      }
    }
  }
}`

// checkFixture parses a project and runs the floor check over its elements.
func checkFixture(t *testing.T, root string) error {
	t.Helper()
	c, err := CompileDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("CompileDriftfile: %v", err)
	}
	els, err := atomic_cmd.BuildElements(FunctionSpecs(c.Manifest))
	if err != nil {
		t.Fatalf("BuildElements: %v", err)
	}
	return CheckCompiledBookings(c, els)
}

// THE case this exists for: the Driftfile printed in the getting-started
// tutorial. Its functions are Go and it booked one of them at 8MB, which the
// schema's `pattern` accepts — 8MB is a legal booking — and the api then refuses
// at deploy, after the cost-confirm and the upload.
//
// The document cannot express this, which is why it is checked here: nothing in
// a Driftfile says what language a function is written in.
func TestCheckCompiledBookings_RefusesACompiledFunctionUnderTheFloor(t *testing.T) {
	withSchema(t, floorSchema)
	root := demoProject(t, demoDriftfile) // get:ping books 8MB, in Go

	err := checkFixture(t, root)
	if err == nil {
		t.Fatal("a Go function booked at 8MB was accepted — the deploy would 400 after " +
			"the upload, which is the failure moving this check to the laptop removes")
	}
	if !strings.Contains(err.Error(), "get:ping") {
		t.Errorf("the error must name the function that has to change, got: %v", err)
	}
	if !strings.Contains(err.Error(), "16MB") {
		t.Errorf("the error must state the floor to raise it to, got: %v", err)
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("the error must name the language that carries the floor, got: %v", err)
	}
}

// The control that must keep passing: the same project at the floor. Without it
// the test above would pass for a check that refused everything.
func TestCheckCompiledBookings_AcceptsACompiledFunctionAtTheFloor(t *testing.T) {
	withSchema(t, floorSchema)
	root := demoProject(t, strings.Replace(demoDriftfile, `memory = "8MiB"`, `memory = "16MiB"`, 1))

	if err := checkFixture(t, root); err != nil {
		t.Fatalf("a Go function booked at the floor was refused: %v", err)
	}
}

// The other control, and the one that would make this check a regression if it
// broke: 8MB is a legal booking for an interpreted function and must stay legal.
// The interpreted four share one language server per language, so their working
// set genuinely fits — raising the floor for them would price the platform's own
// overhead as though the tenant had asked for it.
func TestCheckCompiledBookings_LeavesInterpretedBookingsAlone(t *testing.T) {
	withSchema(t, floorSchema)

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Driftfile"), `slice "demo" {}

atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "get_ping"
      memory = "8MiB"
    }
  }
}
`)
	mustWrite(t, filepath.Join(root, "atomic", "api.py"), "def get_ping(req):\n    return {}\n")

	if err := checkFixture(t, root); err != nil {
		t.Fatalf("a Python function booked at 8MB was refused, but the floor is compiled-only: %v", err)
	}
}

// A machine whose cached schema predates the annotation publishes no floor. The
// check then does nothing rather than inventing one — the same choice
// NamePattern makes, and for the same reason: the api is the enforcing owner
// either way, and a CLI that guesses a bound is a CLI holding a rule of its own.
func TestCheckCompiledBookings_SkipsWhenTheSchemaPublishesNoFloor(t *testing.T) {
	withSchema(t, `{
	  "$schema": "http://json-schema.org/draft-07/schema#",
	  "type": "object",
	  "definitions": {"atomicEntry": {"properties": {"memory": {"type": "string"}}}}
	}`)
	root := demoProject(t, demoDriftfile)

	if err := checkFixture(t, root); err != nil {
		t.Fatalf("a schema with no published floor must produce no verdict, got: %v", err)
	}
}

// A handler the manifest names and the source lacks fails the deploy BEFORE
// anything is built or uploaded, and the message names the file it looked in.
func TestFunctionSpecs_MissingHandlerFailsBeforeAnythingShips(t *testing.T) {
	withRealSchema(t)
	root := demoProject(t, strings.Replace(demoDriftfile, `handler = "GetPing"`, `handler = "GetPong"`, 1))
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}

	_, err = atomic_cmd.BuildElements(FunctionSpecs(m))
	if err == nil {
		t.Fatal("a declared handler with no callable behind it must stop the deploy")
	}
	if !strings.Contains(err.Error(), "get:ping") {
		t.Errorf("the error must name the function that cannot be built, got: %v", err)
	}
	if !strings.Contains(err.Error(), "GetPing") {
		t.Errorf("the error must list the callables that DO exist, got: %v", err)
	}
}

// `drift atomic deploy <dir>` ships a subset of the manifest, never something
// the manifest has never heard of.
func TestFunctionSpecsInDir_SelectsOneElement(t *testing.T) {
	withRealSchema(t)
	root := demoProject(t, demoDriftfile)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	got, err := FunctionSpecsInDir(filepath.Join(root, "atomic", "billing"))
	if err != nil {
		t.Fatalf("FunctionSpecsInDir: %v", err)
	}
	if len(got) != 1 || got[0].Name != "post:invoices" {
		t.Errorf("got %+v, want just post:invoices", got)
	}

	// A directory nobody declared has no booking, no gate and no handler, so
	// there is nothing to deploy — and saying so beats deploying nothing quietly.
	if _, err := FunctionSpecsInDir(filepath.Join(root, "canvas")); err == nil {
		t.Error("an undeclared directory must be refused, not silently skipped")
	}
}

// `drift atomic deploy <dir>` names a folder INSIDE a project, so the project
// is found by walking UP from that folder — not by hoping the shell happens to
// be standing in the root.
func TestFindDriftfileFor_WalksUpFromTheTarget(t *testing.T) {
	root := demoProject(t, demoDriftfile)
	want := filepath.Join(root, "Driftfile")

	// From the element folder, two levels down, with the working directory
	// somewhere else entirely.
	for _, from := range []string{
		root,
		filepath.Join(root, "atomic"),
		filepath.Join(root, "atomic", "billing"),
	} {
		got, err := FindDriftfileFor(from)
		if err != nil {
			t.Fatalf("FindDriftfileFor(%s): %v", from, err)
		}
		if got != want {
			t.Errorf("FindDriftfileFor(%s) = %q, want %q", from, got, want)
		}
	}
}

// Nowhere above the target has one: the error says so about the PATH, rather
// than about the working directory, which is what made the old message
// confusing.
func TestFindDriftfileFor_ReportsTheDirectoryItSearched(t *testing.T) {
	orphan := t.TempDir()
	_, err := FindDriftfileFor(orphan)
	if err == nil {
		t.Fatal("a directory in no project must be refused")
	}
	if !strings.Contains(err.Error(), orphan) {
		t.Errorf("the error must name the directory searched, got: %v", err)
	}
}
