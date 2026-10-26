package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
)

// TestParseDriftfile_RestaurantTemplate parses a complete restaurant Driftfile
// end-to-end and verifies every block lands where the document says it should.
// This is the contract test for what every reader downstream consumes: when
// adding a block, extend this test before touching any other code.
func TestParseDriftfile_RestaurantTemplate(t *testing.T) {
	withRealSchema(t)
	t.Setenv("RESTAURANT_NAME", "La Cucina")
	t.Setenv("RESEND_API_KEY", "test-resend-key-xyz")
	t.Setenv("SENDER_EMAIL", "noreply@la-cucina.test")

	tmp := writeRestaurantFixture(t)

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}

	if m.Name() != "la-cucina" {
		t.Errorf("slice = %q, want %q", m.Name(), "la-cucina")
	}

	// Every function, by its booking key, in the order the file declares them.
	fns := FunctionEntries(m.Slice())
	wantFns := []string{"get:menu", "post:reservations", "post:reservations/confirm"}
	if len(fns) != len(wantFns) {
		t.Fatalf("functions = %d, want %d", len(fns), len(wantFns))
	}
	for i, fn := range fns {
		if fn.Str("name") != wantFns[i] {
			t.Errorf("function %d = %q, want %q", i, fn.Str("name"), wantFns[i])
		}
	}

	if nosql := m.Slice().Entries("slot", "backbone", "nosql"); len(nosql) != 1 || nosql[0].Str("slot") != "reservations" {
		t.Errorf("backbone.nosql = %+v, want one entry naming the slot reservations", nosql)
	}
	if got := DeclaredQueues(m); len(got) != 1 || got[0] != "reservation-queue" {
		t.Errorf("queues = %v, want [reservation-queue]", got)
	}
	if e, ok := m.Slice().EntryMap("file", "backbone", "cache")["menu"]; !ok || e.Str("file") != "backbone/menu.json" {
		t.Errorf("backbone.cache.menu = %+v, want the file backbone/menu.json", e)
	}

	// Every secret is a reference in the file, resolved here from the
	// environment, because what reads a Manifest sends the values.
	secrets := m.Slice().StrMap("backbone", "secrets")
	for name, want := range map[string]string{
		"RESTAURANT_NAME": "La Cucina",
		"RESEND_API_KEY":  "test-resend-key-xyz",
		"SENDER_EMAIL":    "noreply@la-cucina.test",
	} {
		if secrets[name] != want {
			t.Errorf("secret %s = %q, want the resolved value", name, secrets[name])
		}
	}

	if sites := m.Slice().Entries("dir", "canvas", "sites"); len(sites) != 1 || sites[0].Str("dir") != "canvas" {
		t.Errorf("canvas.sites = %+v, want one site serving canvas", sites)
	}
}

// An unset secret reference is a refusal naming every variable that is not
// set, not the silent injection of an empty string or of "$NAME".
func TestParseDriftfile_MissingEnvref(t *testing.T) {
	withRealSchema(t)
	tmp := writeRestaurantFixture(t)
	t.Setenv("RESTAURANT_NAME", "La Cucina")
	// RESEND_API_KEY and SENDER_EMAIL are deliberately left unset.

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected an error for the unset references, got nil")
	}
	msg := err.Error()
	if !contains(msg, "RESEND_API_KEY") || !contains(msg, "SENDER_EMAIL") {
		t.Errorf("the error should name both unset references, got: %s", msg)
	}
}

// The platform's schema refuses a slice name out of its shape, at the label.
func TestParseDriftfile_InvalidName(t *testing.T) {
	withRealSchema(t)
	_, err := ParseDriftfile(writeDriftfile(t, "slice \"BAD_Name_With_Underscores\" {}\n"))
	if err == nil {
		t.Fatal("expected a refusal for an invalid slice name, got nil")
	}
	if !contains(err.Error(), "Driftfile:1:") || !contains(err.Error(), "slice") {
		t.Errorf("the refusal should be at the slice label, got: %s", err)
	}
}

// A block or attribute the format does not have is refused by name, at its
// line: a typo is never silently dropped.
func TestParseDriftfile_UnknownBlocksAndAttributesAreRefused(t *testing.T) {
	withRealSchema(t)
	for _, tc := range []struct{ body, want string }{
		{"slice \"hello\" {}\nqeue \"jobs\" {}\n", "qeue"},
		{"slice \"hello\" {\n  bliling = \"free\"\n}\n", "bliling"},
		// An option nothing implements is refused rather than accepted and
		// ignored: a redelivery cap the file states and nothing enforces is a lie.
		{"slice \"hello\" {}\nbackbone {\n  queue \"jobs\" { max_receives = 3 }\n}\n", "max_receives"},
	} {
		_, err := ParseDriftfile(writeDriftfile(t, tc.body))
		if err == nil || !contains(err.Error(), tc.want) || !contains(err.Error(), "Driftfile:") {
			t.Errorf("got %v, want a refusal naming %q at its position", err, tc.want)
		}
	}
}

// A database, collection or bucket states its size: it is the enforced limit
// and what it is billed at, and the platform refuses a size of zero.
func TestParseDriftfile_StorageWithoutASizeIsRefused(t *testing.T) {
	withRealSchema(t)
	for _, block := range []string{"sql", "nosql", "blobs"} {
		_, err := ParseDriftfile(writeDriftfile(t, "slice \"hello\" {}\nbackbone {\n  "+block+" \"thing\" {}\n}\n"))
		if err == nil || !contains(err.Error(), "Missing size") {
			t.Errorf("%s with no size: got %v", block, err)
		}
	}
}

// writeRestaurantFixture writes a complete restaurant project into a temp dir
// and returns the dir path. It exercises one of every block the deploy reads.
func writeRestaurantFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice "la-cucina" {}

atomic {
  backend "api" {
    function "GET /menu" {
      handler = "GetMenu"
      memory  = "32MiB"
    }
    function "POST /reservations" {
      handler = "PostReservations"
      memory  = "64MiB"
    }
    function "POST /reservations/confirm" {
      handler = "PostReservationsConfirm"
      memory  = "32MiB"
    }
  }
}

backbone {
  nosql "reservations" { size = "50MiB" }
  queue "reservation-queue" {}
  cache "menu" { file = "backbone/menu.json" }

  secret "RESTAURANT_NAME" { value = env("RESTAURANT_NAME") }
  secret "RESEND_API_KEY" { value = env("RESEND_API_KEY") }
  secret "SENDER_EMAIL" { value = env("SENDER_EMAIL") }
}

canvas {
  site "/" { dir = "canvas" }
}
`)
	mustMkdir(t, filepath.Join(tmp, "atomic", "api"))
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustWrite(t, filepath.Join(tmp, "backbone", "menu.json"), `[]`)
	return tmp
}

// A function's identity is its method and route, so GET /items and POST /items are
// two slots. Two functions on ONE identity would shadow each other on the slice,
// with the survivor answering for both, so the parse refuses them by key.
func TestCheckRouteCollisions(t *testing.T) {
	withRealSchema(t)
	two := func(a, b string) string {
		return "slice \"shop\" {}\natomic {\n  backend \"api\" {\n" +
			"    function \"" + a + "\" {\n      handler = \"A\"\n      memory = \"32MiB\"\n    }\n" +
			"    function \"" + b + "\" {\n      handler = \"B\"\n      memory = \"32MiB\"\n    }\n  }\n}\n"
	}
	m := planManifest(t, two("GET /items", "POST /items"), "atomic/api/")
	if err := checkRouteCollisions(m); err != nil {
		t.Errorf("GET /items and POST /items are distinct slots, should not collide: %v", err)
	}

	_, err := ParseDriftfile(writeDriftfile(t, two("POST /items", "POST /items"), "atomic/api/"))
	if err == nil || !strings.Contains(err.Error(), `the function "post:items" is declared 2 times`) {
		t.Errorf("the parse must refuse two POST /items, naming the key, got %v", err)
	}

	// The collision check is the same rule over the specs a deploy is handed.
	twice := append(FunctionSpecs(m), FunctionSpecs(m)[1])
	err = atomic_cmd.CheckCollisions(twice)
	if err == nil {
		t.Fatal("expected a collision error for two post:items, got nil")
	}
	if !strings.Contains(err.Error(), "items") || !strings.Contains(err.Error(), "collision") {
		t.Errorf("collision error should name the route: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The schema owns the document; it cannot own the filesystem. A file can be
// perfectly legal and still name a folder or a file that is not on this
// machine, and that is refused at the attribute that names it, rather than
// surfacing later as a build error about no input files.
func TestParseDriftfile_LocalPathsMustExist(t *testing.T) {
	withRealSchema(t)
	const head = "slice \"hello\" {}\n"
	for _, tc := range []struct{ name, body, want string }{
		{"a function's own dir", "atomic {\n  backend \"default\" {\n    function \"GET /menu\" {\n      handler = \"GetMenu\"\n      memory = \"8MiB\"\n      dir = \"src/menu\"\n    }\n  }\n}\n",
			"function \"GET /menu\": dir: src/menu does not exist"},
		{"a backend's folder", "atomic {\n  backend \"billing\" {\n    function \"GET /x\" {\n      handler = \"X\"\n      memory = \"8MiB\"\n    }\n  }\n}\n",
			"atomic/billing is not a directory"},
		{"a site's folder", "canvas {\n  site \"/\" { dir = \"site\" }\n}\n", "site \"/\": dir: site does not exist"},
		{"a seed file", "backbone {\n  nosql \"menu\" {\n    size = \"10MiB\"\n    seed = \"seeds/menu.jsonl\"\n  }\n}\n", "seeds/menu.jsonl does not exist"},
		{"a cache file", "backbone {\n  cache \"greeting\" { file = \"data/greeting.txt\" }\n}\n", "data/greeting.txt does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDriftfile(writeDriftfile(t, head+tc.body, "atomic/"))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Driftfile:") {
				t.Errorf("got %v\nwant a refusal at its position naming: %s", err, tc.want)
			}
		})
	}
}

// The seed file existing is not the same as the seed file being loadable, and a
// record with no _id is dropped on ingest rather than rejected, so the parse is
// the last place it can be reported.
func TestParseDriftfile_SeedRecordsMustBeLoadable(t *testing.T) {
	withRealSchema(t)
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"),
		"slice \"hello\" {}\nbackbone {\n  nosql \"menu\" {\n    size = \"10MiB\"\n    seed = \"seeds/menu.jsonl\"\n  }\n}\n")
	mustWrite(t, filepath.Join(tmp, "seeds", "menu.jsonl"),
		`{"_id":"pizza","price":9}`+"\n"+
			`{"price":11}`+"\n"+ // no _id
			`{"_id":"","price":12}`+"\n"+ // empty _id
			`{"_id":"soup",`+"\n") // truncated JSON

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("a seed file whose records cannot be loaded must not parse")
	}
	for _, want := range []string{":2: missing _id", ":3: empty _id", ":4: invalid JSON"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("every bad record must be reported with its line number, missing %q\ngot: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), ":1:") {
		t.Errorf("the one valid record must not be reported\ngot: %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// ParseHooks reads the hooks without compiling the rest of the file, so a
// pre_deploy build can make what the compile then checks for.
func TestParseHooks(t *testing.T) {
	path := writeDriftfile(t, `slice "hello" {}
canvas {
  site "/" { dir = "dist" }
}
hook "pre_deploy" { run = "npm run build" }
hook "pre_deploy" { run = "npm run lint" }
hook "post_deploy" { run = "./smoke.sh" }
`)
	// Note: ./dist does NOT exist; ParseHooks must not care.
	pre, post, err := ParseHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 2 || pre[0] != "npm run build" || pre[1] != "npm run lint" {
		t.Errorf("pre_deploy = %+v", pre)
	}
	if len(post) != 1 || post[0] != "./smoke.sh" {
		t.Errorf("post_deploy = %+v", post)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// An environment can set a value to ZERO: a variable's value is the
// environment's when the environment sets one, whatever it is, and a 0 is a
// value rather than an absence. The control is the default slice, which keeps
// the default.
func TestParseDriftfileEnv_AnEnvironmentCanSetZero(t *testing.T) {
	withRealSchema(t)
	path := writeDriftfile(t, `variable "history" { default = 5 }
slice "hello" {
  limits { deploy_history = var.history }
}
environment "staging" { vars = { history = 0 } }
`)
	for env, want := range map[string]int{"": 5, "staging": 0} {
		c, err := CompileDriftfileEnv(path, env)
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		if got := c.Shape.Atomic.MaxNumberOfDeploymentsInHistory; got != want {
			t.Errorf("environment %q: deploy_history = %d, want %d", env, got, want)
		}
	}
}

// An environment's secrets are resolved as the default slice's are: the
// manifest a deploy reads for staging carries the value, never "$NAME".
func TestParseDriftfileEnv_ResolvesSecretsForTheEnvironment(t *testing.T) {
	withRealSchema(t)
	t.Setenv("DRIFT_TEST_SECRET_VALUE", "s3cr3t")
	path := writeDriftfile(t, `slice "hello" {}
backbone {
  secret "API_TOKEN" { value = env("DRIFT_TEST_SECRET_VALUE") }
}
environment "staging" {}
`)
	m, err := ParseDriftfileEnv(path, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name() != "hello-staging" {
		t.Errorf("Name() = %q, want hello-staging", m.Name())
	}
	if got := m.Slice().StrMap("backbone", "secrets")["API_TOKEN"]; got != "s3cr3t" {
		t.Errorf("API_TOKEN = %q, want the resolved value", got)
	}
}
