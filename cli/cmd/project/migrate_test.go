package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// migrateFixtures is where every input, its golden output and the schema live.
const migrateFixtures = "testdata/migrate"

// withMigrateSchema isolates HOME and gives it the schema fixture, the format
// this binary implements, so these tests never read whatever this machine
// happens to hold.
func withMigrateSchema(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(migrateFixtures, "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	withSchema(t, string(raw))
}

// The fixture is a copy of the platform's schema, and a copy nothing compares
// rots. It is pinned to the format this binary implements: the release that
// moves ImplementedDriftfileFormat fails here until the fixture is refreshed
// from the platform (src/common/driftfile/driftfile.schema.json).
func TestMigrateSchemaFixtureIsTheFormatThisBinaryImplements(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrateFixtures, "driftfile.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != common.ImplementedDriftfileFormat {
		t.Errorf("the schema fixture is format %s and this binary implements %s; refresh the fixture from "+
			"the platform's driftfile.schema.json", doc.Version, common.ImplementedDriftfileFormat)
	}
}

// One golden pair per migration: <case>.yaml in, <case>.golden.yaml out, byte
// for byte. The inputs carry comments on and around the moved keys, blank
// lines, an unusual key order, environment overlays and an anchor, because
// those are what a rewrite through a decoded map would lose.
func TestMigrateGolden(t *testing.T) {
	cases := []struct {
		name  string
		steps []string // "old -> new", in file order
		kept  []string
	}{
		{name: "slice_name", steps: []string{"name -> slice"}},
		{name: "nosql_slot", steps: []string{
			"backbone.nosql[].name -> backbone.nosql[].slot",
			"backbone.nosql[].name -> backbone.nosql[].slot",
		}, kept: []string{"backbone.nosql[].size", "backbone.nosql[].size"}},
		{name: "canvas_path", steps: []string{
			"canvas.sites[].route -> canvas.sites[].path",
			"canvas.sites[].route -> canvas.sites[].path",
		}},
		{name: "atomic_egress", steps: []string{"atomic.egress -> edge.egress"}},
		{name: "domains", steps: []string{"domains -> edge.domains"}},
		{name: "rate_limit", steps: []string{"atomic.rate_limit -> edge.rate_limit"}},
		{name: "function_name", steps: []string{
			"atomic.functions -> atomic.pulses",
			"atomic.pulses[].name -> atomic.pulses[].route + method",
			"atomic.pulses[].name -> atomic.pulses[].route + method",
			"atomic.pulses[].name -> atomic.pulses[].route + method",
		}, kept: []string{"atomic.pulses[].memory", "atomic.pulses[].memory", "atomic.pulses[].memory"}},
		{name: "overlay_only", steps: []string{
			"environments.staging.atomic.egress -> environments.staging.edge.egress",
			"environments.prod.domains -> environments.prod.edge.domains",
		}},
		// The list renamed in place, in the base and inside an overlay, with the
		// comments on and above it kept and a `fields` list beside it untouched.
		{name: "pulses", steps: []string{
			"atomic.functions -> atomic.pulses",
			"environments.staging.atomic.functions -> environments.staging.atomic.pulses",
		}},
		{name: "realistic", steps: []string{
			"domains -> edge.domains",
			"atomic.egress -> edge.egress",
			"atomic.rate_limit -> edge.rate_limit",
			"atomic.functions -> atomic.pulses",
			"atomic.pulses[].name -> atomic.pulses[].route + method",
			"atomic.pulses[].name -> atomic.pulses[].route + method",
			"backbone.nosql[].name -> backbone.nosql[].slot",
			"environments.staging.atomic.rate_limit -> environments.staging.edge.rate_limit",
			"environments.staging.atomic.egress -> environments.staging.edge.egress",
			"name -> slice",
			"canvas.sites[].route -> canvas.sites[].path",
		}, kept: []string{"atomic.pulses[].memory", "atomic.pulses[].memory", "backbone.nosql[].size"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMigrateSchema(t)
			src := readFixture(t, tc.name+".yaml")

			if err := ValidateDriftfileSource(src); err != nil {
				t.Fatalf("the input is not a document the schema fixture accepts, so it tests nothing:\n%v", err)
			}

			got, err := MigrateDriftfile(src)
			if err != nil {
				t.Fatalf("migrate refused:\n%v", err)
			}
			want, err := os.ReadFile(filepath.Join(migrateFixtures, tc.name+".golden.yaml"))
			if err != nil {
				t.Fatalf("no golden output for %s (%v); this run produced:\n%s", tc.name, err, got.Output)
			}
			if !bytes.Equal(got.Output, want) {
				t.Fatalf("output differs from %s.golden.yaml\n--- got ---\n%s--- want ---\n%s", tc.name, got.Output, want)
			}
			if !got.Changed() {
				t.Error("a file with retired keys reported no change")
			}

			var steps []string
			for _, s := range got.Steps {
				steps = append(steps, s.Old+" -> "+s.New)
			}
			if strings.Join(steps, "\n") != strings.Join(tc.steps, "\n") {
				t.Errorf("steps:\n got: %q\nwant: %q", steps, tc.steps)
			}
			var kept []string
			for _, k := range got.Kept {
				kept = append(kept, k.Path)
				if k.Advice == "" || k.Line == 0 {
					t.Errorf("%s is kept with no advice or no line: %+v", k.Path, k)
				}
			}
			if strings.Join(kept, "\n") != strings.Join(tc.kept, "\n") {
				t.Errorf("kept:\n got: %q\nwant: %q", kept, tc.kept)
			}

			if err := ValidateDriftfileSource(got.Output); err != nil {
				t.Errorf("the migrated file does not validate against the schema fixture:\n%v", err)
			}
			assertAppliesWithoutAMigrationNotice(t, got.Output)

			// Migrating the result again changes nothing: the rewrite is done in one pass.
			again, err := MigrateDriftfile(got.Output)
			if err != nil {
				t.Fatalf("migrating the migrated file refused:\n%v", err)
			}
			if again.Changed() || !bytes.Equal(again.Output, got.Output) {
				t.Errorf("a second migrate still found changes: %+v", again.Steps)
			}
		})
	}
}

// A file already in the current spelling comes back byte for byte, and says
// it has nothing to do.
func TestMigrateAlreadyCurrentChangesNothing(t *testing.T) {
	withMigrateSchema(t)
	src := readFixture(t, "already_current.yaml")

	got, err := MigrateDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed() || len(got.Steps) != 0 {
		t.Errorf("a current file reported changes: %+v", got.Steps)
	}
	if !bytes.Equal(got.Output, src) {
		t.Errorf("a current file was rewritten:\n%s", got.Output)
	}
	assertAppliesWithoutAMigrationNotice(t, src)
}

// Both spellings of one key is a document contradicting itself. Apply uses the
// new one and ignores the old; migrate deletes neither, and refuses naming
// both paths and both lines.
func TestMigrateRefusesBothSpellings(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want []string
	}{
		"across parents": {
			doc: "slice: shop\natomic:\n  egress:\n    mode: open\n  functions:\n" +
				"    - {route: ping, method: get, handler: GetPing}\nedge:\n  egress:\n    mode: allowlist\n",
			want: []string{"atomic.egress (line 3)", "edge.egress (line 8)", "drift file apply uses edge.egress and ignores atomic.egress"},
		},
		"in place": {
			doc:  "name: shop\nslice: shop\natomic:\n  functions:\n    - {route: ping, method: get, handler: GetPing}\n",
			want: []string{"name (line 1)", "slice (line 2)"},
		},
		"in an overlay": {
			doc: "slice: shop\natomic:\n  functions:\n    - {route: ping, method: get, handler: GetPing}\n" +
				"environments:\n  staging:\n    domains: [{host: a.example.com}]\n    edge:\n      domains: [{host: b.example.com}]\n",
			want: []string{"environments.staging.domains (line 7)", "environments.staging.edge.domains (line 9)"},
		},
		"a pulse's name beside its route": {
			doc: "slice: shop\natomic:\n  functions:\n    - name: get:ping\n      route: pong\n" +
				"      method: get\n      handler: GetPing\n      memory: 16MB\n",
			want: []string{"atomic.pulses[].name (line 4)", "atomic.pulses[].route (line 5)"},
		},
		"both spellings of the pulse list": {
			doc: "slice: shop\natomic:\n  functions:\n    - {route: ping, method: get, handler: GetPing}\n" +
				"  pulses:\n    - {route: pong, method: get, handler: GetPong}\n",
			want: []string{"atomic.functions (line 3)", "atomic.pulses (line 5)",
				"drift file apply uses atomic.pulses and ignores atomic.functions"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			withMigrateSchema(t)
			if err := ValidateDriftfileSource([]byte(tc.doc)); err != nil {
				t.Fatalf("the input is not a valid document, so this proves nothing:\n%v", err)
			}
			_, err := MigrateDriftfile([]byte(tc.doc))
			var refusal *MigrationRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("both spellings were not refused: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%v", w, err)
				}
			}
		})
	}
}

// A key with nowhere to go is reported with what to do instead, and stays in
// the file byte for byte.
func TestMigrateReportsAnIgnoredKeyAndKeepsIt(t *testing.T) {
	withMigrateSchema(t)
	src := []byte("slice: shop\nlog_retention: 72h # a week is too long\natomic:\n  atomic_size: 100MB\n" +
		"  pulses:\n    - route: ping\n      method: get\n      handler: GetPing\n      memory: 16MB\n")

	got, err := MigrateDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed() || !bytes.Equal(got.Output, src) {
		t.Errorf("a file whose only retired keys are ignored ones was changed:\n%s", got.Output)
	}
	want := map[string]int{"log_retention": 2, "atomic.atomic_size": 4, "atomic.pulses[].memory": 9}
	if len(got.Kept) != len(want) {
		t.Fatalf("kept %d keys, want %d: %+v", len(got.Kept), len(want), got.Kept)
	}
	for _, k := range got.Kept {
		if want[k.Path] != k.Line {
			t.Errorf("%s reported at line %d, want %d", k.Path, k.Line, want[k.Path])
		}
		if !strings.Contains(k.Advice, "drift slice resize") {
			t.Errorf("%s is reported without what to do instead: %q", k.Path, k.Advice)
		}
	}
}

// A move that would put an alias before the anchor it names breaks the file,
// so it is refused rather than written.
func TestMigrateRefusesToPutAnAliasBeforeItsAnchor(t *testing.T) {
	withMigrateSchema(t)
	src := []byte(`slice: shop
atomic:
  egress: &eg
    mode: allowlist
    hosts: [api.stripe.com]
  functions:
    - route: ping
      method: get
      handler: GetPing
environments:
  staging:
    edge:
      egress: *eg
edge:
  rate_limit: 600/min
`)
	if err := ValidateDriftfileSource(src); err != nil {
		t.Fatalf("the input is not a valid document:\n%v", err)
	}
	_, err := MigrateDriftfile(src)
	var refusal *MigrationRefusal
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "alias before the anchor") {
		t.Fatalf("an alias left before its anchor was not refused: %v", err)
	}
}

// A section written in flow style has no lines to move a key out of, so a move
// from one is refused with what to do instead.
func TestMigrateRefusesAMoveOutOfFlowStyle(t *testing.T) {
	withMigrateSchema(t)
	src := []byte("slice: shop\natomic: {rate_limit: 600/min, functions: [{route: ping, method: get, handler: GetPing}]}\n")

	_, err := MigrateDriftfile(src)
	if err == nil || !strings.Contains(err.Error(), "flow style") {
		t.Fatalf("a move out of a flow section was not refused: %v", err)
	}
}

// A move out of a section reached through an alias would change every use of
// the anchor, so it is refused.
func TestMigrateRefusesAMoveInsideASharedAnchor(t *testing.T) {
	withMigrateSchema(t)
	src := []byte(`slice: shop
atomic: &atomic
  rate_limit: 600/min
  functions:
    - route: ping
      method: get
      handler: GetPing
environments:
  staging:
    atomic: *atomic
`)
	_, err := MigrateDriftfile(src)
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("a move inside a shared anchor was not refused: %v", err)
	}
}

// A destination that exists but is empty takes the moved key under its own
// key line, with the empty value removed and any comment on that line kept.
func TestMigrateFillsAnEmptyDestination(t *testing.T) {
	head := "slice: shop\natomic:\n  rate_limit: 600/min\n  pulses:\n    - {route: ping, method: get, handler: GetPing}\n"
	body := "slice: shop\natomic:\n  pulses:\n    - {route: ping, method: get, handler: GetPing}\n"
	for in, want := range map[string]string{
		"edge:\n":                "edge:\n  rate_limit: 600/min\n",
		"edge: ~\n":              "edge:\n  rate_limit: 600/min\n",
		"edge: {}  # none yet\n": "edge:  # none yet\n  rate_limit: 600/min\n",
	} {
		got, err := MigrateDriftfile([]byte(head + in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(got.Output) != body+want {
			t.Errorf("%q:\n got: %q\nwant: %q", in, got.Output, body+want)
		}
	}
}

// Line endings, and a file with no final newline, come back as they went in.
func TestMigrateKeepsLineEndings(t *testing.T) {
	withMigrateSchema(t)
	src := []byte("name: shop\r\natomic:\r\n  rate_limit: 600/min\r\n  functions:\r\n" +
		"    - {route: ping, method: get, handler: GetPing}")
	want := "slice: shop\r\natomic:\r\n  pulses:\r\n" +
		"    - {route: ping, method: get, handler: GetPing}\r\nedge:\r\n  rate_limit: 600/min"

	got, err := MigrateDriftfile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Output) != want {
		t.Errorf("line endings changed:\n got: %q\nwant: %q", got.Output, want)
	}
}

// assertAppliesWithoutAMigrationNotice loads a file the way `drift file apply`
// does, base and every environment, and fails on any notice migrate exists to
// remove. An ignored shape key's notice is expected: migrate cannot move it.
func assertAppliesWithoutAMigrationNotice(t *testing.T, src []byte) {
	t.Helper()
	for _, line := range migrationNotices(t, src) {
		t.Errorf("drift file apply still prints a notice migrate should have removed:\n%s", line)
	}
}

// The control for the check above: every golden INPUT draws at least one of
// the notices it looks for, so a check that can never see one cannot pass the
// migrated files for that reason.
func TestMigrateNoticeCheckSeesTheUnmigratedFiles(t *testing.T) {
	for _, name := range []string{
		"slice_name", "nosql_slot", "canvas_path", "atomic_egress", "domains",
		"rate_limit", "function_name", "overlay_only", "pulses", "realistic",
	} {
		t.Run(name, func(t *testing.T) {
			withMigrateSchema(t)
			if len(migrationNotices(t, readFixture(t, name+".yaml"))) == 0 {
				t.Error("the unmigrated file drew no notice, so the check on the migrated one proves nothing")
			}
		})
	}
}

// migrationNotices is every notice `drift file apply` prints for src that
// names `drift file migrate` or a key it rewrites.
func migrationNotices(t *testing.T, src []byte) []string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"site", "docs", "admin"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "Driftfile")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}

	var notices bytes.Buffer
	restore := common.RedirectDeprecationWarnings(&notices)
	t.Cleanup(restore)
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the migrated file does not parse the way apply parses it:\n%v", err)
	}
	for _, env := range m.EnvironmentNames() {
		each, err := ParseDriftfile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := each.SelectEnvironment(env, true); err != nil {
			t.Fatalf("selecting %s: %v", env, err)
		}
	}

	var found []string
	for _, line := range strings.Split(strings.TrimSpace(notices.String()), "\n") {
		if strings.Contains(line, "in favour of") || strings.Contains(line, "both set") ||
			strings.Contains(line, common.MigrateHint) || strings.Contains(line, functionNamePath) {
			found = append(found, line)
		}
	}
	return found
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(migrateFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
