package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestParseDriftfile_RestaurantTemplate parses a complete restaurant Driftfile
// end-to-end and verifies every field lands where the format says it should.
// This is the contract test for the schema the platform serves — when adding a
// format feature, extend this test before touching any other code.
func TestParseDriftfile_RestaurantTemplate(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "test-resend-key-xyz")
	t.Setenv("SENDER_EMAIL", "noreply@la-cucina.test")

	tmp := writeRestaurantFixture(t)

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}

	if m.Name() != "la-cucina" {
		t.Errorf("slice.name = %q, want %q", m.Name(), "la-cucina")
	}

	// atomic — every function carries its route and the memory it books. The
	// memory is asserted alongside the name because it is what the slice admits
	// work against and what the function is billed at; a list that parsed its
	// names and dropped its bookings would deploy a slice nobody priced.
	fns := m.Slice().Entries("name", "atomic", "functions")
	if len(fns) != 3 {
		t.Fatalf("atomic.functions count = %d, want 3", len(fns))
	}
	wantFns := []struct{ name, memory string }{
		{"get:menu", "32MB"},
		{"post:reservations", "64MB"},
		{"post:reservations/confirm", "32MB"},
	}
	for i, fn := range fns {
		if fn.Str("name") != wantFns[i].name {
			t.Errorf("atomic.functions[%d].name = %q, want %q", i, fn.Str("name"), wantFns[i].name)
		}
		if fn.Str("memory") != wantFns[i].memory {
			t.Errorf("atomic.functions[%d].memory = %q, want %q", i, fn.Str("memory"), wantFns[i].memory)
		}
	}

	// backbone.nosql — flat-list of strings, expanded to an entry and then
	// normalised onto `slot`, which is what the deploy path reads.
	if len(m.Slice().Entries("slot", "backbone", "nosql")) != 1 || m.Slice().Entries("slot", "backbone", "nosql")[0].Str("slot") != "reservations" {
		t.Errorf("backbone.nosql = %+v, want one entry naming the slot reservations", m.Slice().Entries("slot", "backbone", "nosql"))
	}

	// backbone.queues — flat-list of strings
	if len(m.Slice().Entries("name", "backbone", "queues")) != 1 || m.Slice().Entries("name", "backbone", "queues")[0].Str("name") != "reservation-queue" {
		t.Errorf("backbone.queues = %+v, want [reservation-queue]", m.Slice().Entries("name", "backbone", "queues"))
	}

	// backbone.cache — short-form `<key>: <path>` becomes File
	cache := m.Slice().EntryMap("file", "backbone", "cache")
	if e, ok := cache["menu"]; !ok {
		t.Errorf("backbone.cache.menu missing")
	} else if e.Str("file") != "./backbone/menu.json" {
		t.Errorf("backbone.cache.menu.file = %q, want ./backbone/menu.json", e.Str("file"))
	}

	// backbone.secrets — $ENVREFs are resolved before validation.
	if got := m.Slice().StrMap("backbone", "secrets")["RESTAURANT_NAME"]; got != "La Cucina" {
		t.Errorf("secret RESTAURANT_NAME = %q, want La Cucina", got)
	}
	if got := m.Slice().StrMap("backbone", "secrets")["RESEND_API_KEY"]; got != "test-resend-key-xyz" {
		t.Errorf("secret RESEND_API_KEY = %q, want resolved env value", got)
	}
	if got := m.Slice().StrMap("backbone", "secrets")["SENDER_EMAIL"]; got != "noreply@la-cucina.test" {
		t.Errorf("secret SENDER_EMAIL = %q, want resolved env value", got)
	}

	// canvas — bare-string shorthand becomes sites:[{dir: ./canvas}]
	if len(m.Slice().Entries("dir", "canvas", "sites")) != 1 || m.Slice().Entries("dir", "canvas", "sites")[0].Str("dir") != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice().Entries("dir", "canvas", "sites"))
	}
}

// TestParseDriftfile_MissingEnvref ensures that an unset $ENVREF
// surfaces as a clear validation error, not silent injection of
// an empty string.
func TestParseDriftfile_MissingEnvref(t *testing.T) {
	tmp := writeRestaurantFixture(t)
	// Deliberately leave RESEND_API_KEY/SENDER_EMAIL unset.

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for missing envref, got nil")
	}
	msg := err.Error()
	if !contains(msg, "RESEND_API_KEY") || !contains(msg, "SENDER_EMAIL") {
		t.Errorf("error should mention both unset envrefs, got: %s", msg)
	}
}

// TestParseDriftfile_InvalidName rejects an out-of-shape slice name.
func TestParseDriftfile_InvalidName(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: BAD_Name_With_Underscores
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected validation error for invalid name, got nil")
	}
	if !contains(err.Error(), "name") {
		t.Errorf("error should mention the name field, got: %s", err)
	}
}

// TestParseDriftfile_CanvasShorthandString verifies the bare-string
// canvas shorthand expands correctly.
func TestParseDriftfile_CanvasShorthandString(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice().Entries("dir", "canvas", "sites")) != 1 || m.Slice().Entries("dir", "canvas", "sites")[0].Str("dir") != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice().Entries("dir", "canvas", "sites"))
	}
}

// A bare list of function names under `atomic:` is REFUSED.
//
// Every function must book its memory, and a list of names has nowhere to put
// the booking — so the short form cannot be sugar for the long one. It would
// have to invent a reservation the tenant never chose, for a figure the slice
// admits work against and the invoice is computed from.
//
// Canvas keeps its shorthand (the test above) because `canvas: ./site` states
// the whole of what a site entry carries. That is the line: a shorthand is sugar
// only while it can express everything the long form must.
func TestParseDriftfile_AtomicBareListIsRefused(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
atomic:
  - foo
  - bar
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("a bare list of function names parsed; every function must declare " +
			"the memory it books, and this form cannot carry one")
	}
	// The SCHEMA owns the rule, so the assertion is that the refusal points at
	// the offending section rather than at a wording the platform defines.
	if !contains(err.Error(), "atomic") {
		t.Errorf("the error should name the section it refused, got: %v", err)
	}
	// And it must name only what the user wrote. Expanding the list into
	// `{functions: [...]}` before validating reported `/atomic/functions/0`,
	// sending them looking for a key their file does not contain.
	if contains(err.Error(), "functions") {
		t.Errorf("the error names a key the Driftfile does not contain, got: %v", err)
	}
}

// The bare-string `sql: [ledger]` / `nosql: [widgets]` / `blobs: [assets]` forms
// are NOT legal, and the CLI no longer pretends otherwise.
//
// This test used to assert the opposite — that SQLEntry.UnmarshalYAML accepted a
// bare string — and its own comment conceded the contradiction: "a bare-string
// entry can never carry a `size`, and size is now mandatory, so a full Driftfile
// using the bare form would fail validation regardless". It decoded the type
// directly to dodge the validation that would have caught it.
//
// That is what two definitions of a format look like from the inside: the schema
// requires `name` and `size` because size both prices and enforces the quota, the
// CLI accepted a form that can carry neither, and a test was written around the
// gap rather than closing it (#CLI-STANDARDUSAGE-ERF1CV).
func TestParseDriftfile_BareStringResourceEntriesAreRejected(t *testing.T) {
	for _, section := range []string{"sql", "nosql", "blobs"} {
		t.Run(section, func(t *testing.T) {
			tmp := t.TempDir()
			mustWrite(t, filepath.Join(tmp, "Driftfile"),
				"name: hello\ncanvas: ./canvas\nbackbone:\n  "+section+": [thing]\n")
			mustMkdir(t, filepath.Join(tmp, "canvas"))

			_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
			if err == nil {
				t.Fatalf("backbone.%s accepted a bare string, which the platform rejects "+
					"because size is required and a bare string cannot carry one", section)
			}
		})
	}
}

// TestParseDriftfile_UnknownTopLevelField catches a typo'd top-level key
// (e.g. "nmae" instead of "name"). Nothing in the CLI knows what a key is, so
// the refusal comes from the schema's `additionalProperties: false` — and the
// consequence of losing it is silent: the document decodes, the typo is dropped,
// and the slice deploys unnamed with no error anywhere.
func TestParseDriftfile_UnknownTopLevelField(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
nmae: hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unknown field \"nmae\", got nil")
	}
	if !contains(err.Error(), "nmae") {
		t.Errorf("error should name the unrecognized field, got: %s", err)
	}
}

// TestParseDriftfile_UnknownNestedField catches a typo'd field nested
// under atomic/backbone/canvas (e.g. "schedule" misspelled), which is the
// more common real-world case than a top-level typo.
func TestParseDriftfile_UnknownNestedField(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
backbone:
  nosql:
    - name: widgets
      size: 50MB
  qeues: [jobs]
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unknown field \"qeues\", got nil")
	}
	if !contains(err.Error(), "qeues") {
		t.Errorf("error should name the unrecognized field, got: %s", err)
	}
}

// writeRestaurantFixture writes a minimal-but-complete restaurant project shape
// into a temp dir and returns the dir path. It exercises one of every section
// and every shorthand the format still accepts.
//
// The functions declare no `dir`, so there are no per-function source
// directories here: a function's name is its ROUTE, and nothing derives a path
// from it.
func writeRestaurantFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: la-cucina
atomic:
  functions:
    - name: "get:menu"
      handler: GetMenu
      memory: 32MB
    - name: "post:reservations"
      handler: PostReservations
      memory: 64MB
    - name: "post:reservations/confirm"
      handler: PostReservationsConfirm
      memory: 32MB
backbone:
  nosql:
    - name: reservations
      size: 50MB
  queues: [reservation-queue]
  cache:
    menu: ./backbone/menu.json
  secrets:
    RESTAURANT_NAME: "La Cucina"
    RESEND_API_KEY:  $RESEND_API_KEY
    SENDER_EMAIL:    $SENDER_EMAIL
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustMkdir(t, filepath.Join(tmp, "backbone"))
	mustWrite(t, filepath.Join(tmp, "backbone", "menu.json"), `[]`)
	return tmp
}

// A function's identity is its declared name, method included, so get:items
// and post:items are two functions. Two entries on ONE identity would shadow
// each other on the slice, with the survivor answering for both.
//
// The check reads the manifest and nothing else. It used to read the ANNOTATION
// in each function's source instead, which meant the manifest could declare
// `get:items` while the source shipped `post:items` — and the collision the
// manifest plainly showed was invisible.
func TestCheckRouteCollisions(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: shop
atomic:
  functions:
    - name: "get:items"
      handler: GetItems
      memory: 32MB
    - name: "post:items"
      handler: PostItems
      memory: 32MB
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if err := checkRouteCollisions(m); err != nil {
		t.Errorf("get:items + post:items are distinct functions, should not collide: %v", err)
	}

	// The same identity twice, on two different handlers.
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: shop
atomic:
  functions:
    - name: "post:items"
      handler: CreateItem
      memory: 32MB
    - name: "post:items"
      handler: PostItems
      memory: 32MB
canvas: ./canvas
`)
	m2, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	err = checkRouteCollisions(m2)
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

// The schema owns the format; it cannot own the filesystem. A document can be
// perfectly legal and still name a directory that is not on this machine, and the
// checks that catch that are the one local pass that survives
// (#CLI-STANDARDUSAGE-ERF1CV).
//
// Written because the suite was green with them DELETED: nothing else parses a
// valid Driftfile whose paths are missing, so their removal cost only the error
// message — a declared-but-absent function surfaces much later as a compiler
// complaint about no input files, from inside a build the user did not ask about.
func TestParseDriftfile_LocalPathsMustExist(t *testing.T) {
	cases := []struct {
		name      string
		driftfile string
		want      string
	}{
		{
			name:      "function explicit dir",
			driftfile: "name: hello\natomic:\n  functions:\n    - name: \"get:menu\"\n      handler: GetMenu\n      memory: 8MB\n      dir: ./src/menu\n",
			want:      `atomic.functions[0]: function "get:menu" declares dir`,
		},
		{
			name:      "canvas site directory",
			driftfile: "name: hello\ncanvas: ./site\n",
			want:      `canvas.sites[0]: directory not found at`,
		},
		{
			name:      "nosql seed file",
			driftfile: "name: hello\nbackbone:\n  nosql:\n    - name: menu\n      size: 10MB\n      seed: ./seeds/menu.jsonl\n",
			want:      `backbone.nosql[0]: "menu" seed file not found at`,
		},
		{
			name:      "cache file",
			driftfile: "name: hello\nbackbone:\n  cache:\n    greeting:\n      file: ./data/greeting.txt\n",
			want:      `cache "greeting" file not found at`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			mustWrite(t, filepath.Join(tmp, "Driftfile"), tc.driftfile)

			_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
			if err == nil {
				t.Fatalf("a Driftfile naming a path that is not on this machine must not parse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name the field and the resolved path.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}
}

// The seed file existing is not the same as the seed file being loadable, and a
// record with no _id is dropped on ingest rather than rejected — so the parse is
// the last place it can be reported.
func TestParseDriftfile_SeedRecordsMustBeLoadable(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"),
		"name: hello\nbackbone:\n  nosql:\n    - name: menu\n      size: 10MB\n      seed: ./seeds/menu.jsonl\n")
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

// TestParseDriftfile_BraceVarSubstitution covers the env-aware
// Driftfile feature: ${VAR} placeholders resolve from os.Environ
// before YAML parsing. (With project-level environments the slice name is
// usually derived, not written; ${VAR} still works anywhere in the file.)
func TestParseDriftfile_BraceVarSubstitution(t *testing.T) {
	t.Setenv("ENV", "staging")
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: ${ENV}-hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if m.Name() != "staging-hello" {
		t.Errorf("slice.name = %q, want staging-hello", m.Name())
	}
}

// TestParseDriftfile_BraceVarMissing surfaces every unset placeholder
// at once instead of one-at-a-time.
func TestParseDriftfile_BraceVarMissing(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: ${ENV}-${REGION}-app
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unset ${VAR}, got nil")
	}
	msg := err.Error()
	if !contains(msg, "ENV") || !contains(msg, "REGION") {
		t.Errorf("error should mention both unset vars, got: %s", msg)
	}
}

// TestEnvironments covers the project-level environments feature: per-env
// config overrides deep-merge onto the base, the resource set is shared,
// un-overridden knobs inherit, and the slice name is derived.
func TestEnvironments(t *testing.T) {
	tmp := t.TempDir()
	// Each `atomic` override restates `functions`, because the section the
	// override merges into requires them. `deploy_history` is the control: no
	// override mentions it, so it has to survive the merge into `atomic`.
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: snip
log_retention: 30d
atomic:
  rate_limit: 5000/min
  deploy_history: 5
  functions:
    - { name: "get:redirect", handler: GetRedirect, memory: 128MB }
backbone:
  nosql:
    - name: links
      size: 500MB
canvas: ./web
environments:
  prod: {}
  staging:
    log_retention: 3d
    atomic:
      rate_limit: 200/min
      functions:
        - { name: "get:redirect", handler: GetRedirect, memory: 64MB }
    backbone: { nosql: [{name: links, size: 50MB}] }
  dev:
    atomic:
      rate_limit: 20/min
      functions:
        - { name: "get:redirect", handler: GetRedirect, memory: 128MB }
`)
	mustMkdir(t, filepath.Join(tmp, "web"))

	parse := func() *Manifest {
		m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
		if err != nil {
			t.Fatalf("ParseDriftfile: %v", err)
		}
		return m
	}

	// prod → bare name; base values untouched.
	m := parse()
	if env, err := m.SelectEnvironment("prod", true); err != nil || env != "prod" {
		t.Fatalf("select prod: env=%q err=%v", env, err)
	}
	if m.Name() != "snip" {
		t.Errorf("prod name = %q, want snip", m.Name())
	}
	if m.Slice().Str("atomic", "rate_limit") != "5000/min" || m.Slice().Str("log_retention") != "30d" {
		t.Errorf("prod values changed: rate=%q retention=%q", m.Slice().Str("atomic", "rate_limit"), m.Slice().Str("log_retention"))
	}
	// The base booking, against which staging's override below is the contrast.
	if fns := m.Slice().Entries("name", "atomic", "functions"); len(fns) != 1 || fns[0].Str("memory") != "128MB" {
		t.Errorf("prod functions = %+v, want [get:redirect booked at 128MB]", fns)
	}

	// staging → suffixed name; scalar overrides applied; resource set shared.
	m = parse()
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "snip-staging" {
		t.Errorf("staging name = %q, want snip-staging", m.Name())
	}
	if m.Slice().Str("atomic", "rate_limit") != "200/min" {
		t.Errorf("staging atomic overrides not applied: %+v", m.Slice().Sub("atomic"))
	}
	if m.Slice().Str("log_retention") != "3d" {
		t.Errorf("staging overrides not applied: retention=%q", m.Slice().Str("log_retention"))
	}
	// An environment books its own memory per function: staging runs the same
	// route on a smaller pool, and pays for the smaller one.
	if fns := m.Slice().Entries("name", "atomic", "functions"); len(fns) != 1 ||
		fns[0].Str("name") != "get:redirect" || fns[0].Str("memory") != "64MB" {
		t.Errorf("staging functions = %+v, want [get:redirect booked at 64MB]", fns)
	}
	if len(m.Slice().Entries("name", "backbone", "nosql")) != 1 || m.Slice().Entries("name", "backbone", "nosql")[0].Str("name") != "links" || m.Slice().Entries("name", "backbone", "nosql")[0].Str("size") != "50MB" {
		t.Errorf("staging nosql = %+v, want shared [links] with overridden size 50MB", m.Slice().Entries("name", "backbone", "nosql"))
	}

	// dev → only rate overridden; everything else inherits the base.
	m = parse()
	if _, err := m.SelectEnvironment("dev", true); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "snip-dev" || m.Slice().Str("atomic", "rate_limit") != "20/min" {
		t.Errorf("dev name/rate = %q/%q", m.Name(), m.Slice().Str("atomic", "rate_limit"))
	}
	// The overlay names two keys inside `atomic`; the third has to survive it.
	// An overlay that replaced the section wholesale would zero this.
	if m.Slice().Int("atomic", "deploy_history") != 5 || m.Slice().Str("log_retention") != "30d" {
		t.Errorf("dev should inherit base history/retention: history=%d retention=%q", m.Slice().Int("atomic", "deploy_history"), m.Slice().Str("log_retention"))
	}

	// Default (no arg) resolves to prod when present.
	m = parse()
	if env, err := m.SelectEnvironment("", false); err != nil || env != "prod" {
		t.Errorf("default select: env=%q err=%v, want prod", env, err)
	}

	// Unknown environment errors.
	m = parse()
	if _, err := m.SelectEnvironment("nope", true); err == nil {
		t.Error("expected error for unknown environment")
	}
}

// TestEnvironmentsBareList covers the `environments: [a, b]` sugar: each named
// environment inherits the base shape unchanged; the name still derives.
func TestEnvironmentsBareList(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
environments: [prod, staging]
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}
	if len(m.EnvironmentNames()) != 2 {
		t.Fatalf("environments count = %d, want 2", len(m.EnvironmentNames()))
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	if m.Name() != "hello-staging" {
		t.Errorf("name = %q, want hello-staging", m.Name())
	}
}

// TestSelectEnvironmentNoEnvironments: a single-slice project ignores an empty
// selection but rejects an explicit one.
func TestSelectEnvironmentNoEnvironments(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), "name: solo\ncanvas: ./canvas\n")
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	if env, err := m.SelectEnvironment("", false); err != nil || env != "" {
		t.Errorf("no-env default: env=%q err=%v", env, err)
	}
	if m.Name() != "solo" {
		t.Errorf("name = %q, want solo", m.Name())
	}
	if _, err := m.SelectEnvironment("staging", true); err == nil {
		t.Error("expected error: explicit env on a project that declares none")
	}
}

// TestEnvironmentRejectsName: an override block may not set name.
func TestEnvironmentRejectsName(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
environments:
  staging:
    name: other
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("an environment override set `name` and was accepted")
	}
	// The SCHEMA owns this rule now, so the assertion is on the rule being
	// enforced and on the message pointing at the right place — not on the local
	// pass's old wording. Pinning "must not set name" here would mean the CLI
	// still had to phrase a rule the platform defines
	// (#CLI-STANDARDUSAGE-ERF1CV).
	msg := err.Error()
	if !contains(msg, "name") || !contains(msg, "environments/staging") {
		t.Errorf("the error should name the offending field and where it is, got: %v", err)
	}
}

// TestParseHooks reads the hooks block without validating the rest of the
// project (so a pre_deploy build can run before the full parse).
func TestParseHooks(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./dist
hooks:
  pre_deploy:
    - npm run build
    - npm run lint
  post_deploy: [./smoke.sh]
`)
	// Note: ./dist does NOT exist — ParseHooks must not care.
	pre, post := ParseHooks(filepath.Join(tmp, "Driftfile"))
	if len(pre) != 2 || pre[0] != "npm run build" {
		t.Errorf("pre_deploy = %+v", pre)
	}
	if len(post) != 1 || post[0] != "./smoke.sh" {
		t.Errorf("post_deploy = %+v", post)
	}
}

// #DRIFTFILE-R0TNSP — `queues:` was the one resource list that took bare
// strings while every sibling (nosql, sql, blobs, canvas sites) took a
// string-or-map. The spec's own forward-compatibility example shows the map
// form, so the DOCUMENTED shape was the one that died — and it died with a raw
// decoder message ("cannot unmarshal !!map into string") that names neither the
// file nor the field.
//
// The map form must parse. `name` is the only key it carries: per-queue options
// like max_receives are still unimplemented, and accepting a knob that does
// nothing would be a worse lie than rejecting it.
func TestParseDriftfile_QueueShorthandStringOrMap(t *testing.T) {
	var doc Node
	if err := yaml.Unmarshal([]byte(`
queues:
  - reservation-queue
  - { name: email-nudges }
`), &doc); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	queues := doc.Entries("name", "queues")
	if len(queues) != 2 {
		t.Fatalf("queues count = %d, want 2", len(queues))
	}
	if queues[0].Str("name") != "reservation-queue" {
		t.Errorf("queues[0] = %+v, want bare {Name: reservation-queue}", queues[0])
	}
	if queues[1].Str("name") != "email-nudges" {
		t.Errorf("queues[1] = %+v, want {Name: email-nudges}", queues[1])
	}
}

// And the map form must survive a full ParseDriftfile — the shorthand expander
// and the schema both run there, and either could reject a shape the standalone
// unmarshaler accepts.
func TestParseDriftfile_QueueMapFormEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: queue-shapes
backbone:
  queues:
    - reservation-queue
    - { name: email-nudges }
`)
	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("the spec's own queue example does not parse: %v", err)
	}
	if len(m.Slice().Entries("name", "backbone", "queues")) != 2 {
		t.Fatalf("queues = %+v, want 2 entries", m.Slice().Entries("name", "backbone", "queues"))
	}
	if m.Slice().Entries("name", "backbone", "queues")[1].Str("name") != "email-nudges" {
		t.Errorf("queues[1].name = %q, want email-nudges", m.Slice().Entries("name", "backbone", "queues")[1].Str("name"))
	}
}

// A queue map carrying an unimplemented option must be REFUSED, not silently
// dropped. Accepting `max_receives: 3` and ignoring it would tell the user
// their redelivery cap is set when nothing enforces it — the exact failure
// mode this card is about, one layer deeper.
func TestParseDriftfile_QueueRejectsUnimplementedOption(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: queue-shapes
backbone:
  queues:
    - { name: email-nudges, max_receives: 3 }
`)
	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("max_receives was accepted; nothing implements it, so the manifest lied")
	}
	if !contains(err.Error(), "max_receives") {
		t.Errorf("error %q does not name the offending key", err.Error())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ─── Environment overrides: presence, not truthiness ────────────────────────

// An environment override must be able to set a value to ZERO or FALSE.
//
// The struct-based mergers gated every field on truthiness — `if overlay.X != ""`
// / `!= 0` — so `deploy_history: 0` in an environments block was read as "absent"
// and silently discarded. A user asking staging to keep no rollback history got
// the base value and no error, which is the worst shape: the Driftfile says one
// thing and the slice is another (#CLI-STANDARDUSAGE-T9914R).
//
// YAML distinguishes "absent" from "present and zero"; a struct does not. The
// overlay is merged as a DOCUMENT for exactly this reason.
func TestSelectEnvironment_OverrideToZeroIsHonoured(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
atomic:
  rate_limit: 5000/min
  deploy_history: 5
  functions:
    - { name: "get:ping", handler: GetPing, memory: 32MB }
environments:
  prod: {}
  staging:
    atomic:
      deploy_history: 0
      functions:
        - { name: "get:ping", handler: GetPing, memory: 32MB }
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}

	if got := m.Slice().Int("atomic", "deploy_history"); got != 0 {
		t.Errorf("deploy_history = %d, want 0 — the override was discarded because "+
			"the merge read 0 as 'absent', so the Driftfile and the slice disagree "+
			"with no error anywhere", got)
	}
	// The control: a sibling the overlay did NOT mention must survive. Without
	// this, a merge that simply dropped the base would pass the assertion above.
	if got := m.Slice().Str("atomic", "rate_limit"); got != "5000/min" {
		t.Errorf("rate_limit = %q, want 5000/min — the overlay replaced the whole "+
			"atomic block instead of merging into it", got)
	}
}

// The same defect in its other form: a field the mergers never learned about is
// silently not overridable. Overrides were applied field by field, so adding a
// Driftfile field without adding a merge clause left it inheriting the base
// forever — no compiler error and no failing test, which is why this asserts a
// field rather than trusting the four merge functions to be complete.
//
// mergeCanvas handled exactly two fields, so anything else in `canvas:` was not
// overridable at all.
func TestSelectEnvironment_OverridesEveryDeclaredField(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
log_retention: 24h
environments:
  prod: {}
  staging:
    log_retention: 1h
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}

	if got := m.Slice().Str("log_retention"); got != "1h" {
		t.Errorf("log_retention = %q, want 1h", got)
	}
}

// A `$ENVREF` secret must still be resolved after an environment merge.
//
// ParseDriftfile resolves `$VAR` secrets onto the typed slice; the environment
// merge re-decodes that slice from the raw document, where the value is still
// `$VAR`. Without re-resolving, selecting an environment would silently revert
// every envref and deploy the literal string "$VAR" as the secret's value — a
// credential that is wrong in a way nothing downstream can detect.
func TestSelectEnvironment_SecretEnvRefsSurviveTheMerge(t *testing.T) {
	t.Setenv("DRIFT_TEST_SECRET_VALUE", "s3cr3t")

	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
backbone:
  secrets:
    API_TOKEN: $DRIFT_TEST_SECRET_VALUE
environments:
  prod: {}
  staging:
    log_retention: 1h
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Slice().StrMap("backbone", "secrets")["API_TOKEN"]; got != "s3cr3t" {
		t.Fatalf("parse did not resolve the envref: %q", got)
	}

	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	if got := m.Slice().StrMap("backbone", "secrets")["API_TOKEN"]; got != "s3cr3t" {
		t.Errorf("after the environment merge API_TOKEN = %q, want s3cr3t — the "+
			"merge re-decoded from the raw document and reverted the envref, so the "+
			"deploy would ship the literal placeholder as the secret", got)
	}
}

// A function that declares no `dir` is DISCOVERED from source, so there is no
// path to check.
//
// Its `name` is a TRIGGER — `get:menu` — not a directory. Deriving `atomic/<name>`
// from it asked the filesystem for something nobody declared, using a string
// holding `/` and `:`. It also assumed one folder per function, which is not the
// layout most projects use: a flat `atomic/` tree holding several functions per
// file has no per-function directory at all, and demanding one made every such
// project unable to declare its own functions. The element owns the directory.
//
// Asserts the absence of the PATH error specifically rather than a clean parse:
// the parser also validates against whatever schema this machine has cached, and
// that cache's age is not what this test is about.
func TestParseDriftfile_AFunctionWithoutADirIsNotPathChecked(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"),
		"name: hello\natomic:\n  functions:\n    - name: \"get:menu\"\n      memory: 8MB\n")

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil && strings.Contains(err.Error(), "atomic.functions[0]") &&
		strings.Contains(err.Error(), "not found at") {
		t.Fatalf("a function declaring no dir was path-checked against a directory "+
			"derived from its route: %v", err)
	}
}
