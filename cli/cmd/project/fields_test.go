package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"
)

// fieldProject writes a Driftfile beside a Go element holding a Pulse handler
// and a Field handler, under the schema fixture, and returns its path.
func fieldProject(t *testing.T, driftfile string) string {
	t.Helper()
	withMigrateSchema(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "atomic"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nimport \"context\"\n\n" +
		"func GetPing() {}\n\nfunc Sequencer(ctx context.Context) error { <-ctx.Done(); return nil }\n"
	if err := os.WriteFile(filepath.Join(root, "atomic", "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Driftfile")
	if err := os.WriteFile(path, []byte(driftfile), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const fieldDriftfile = `slice: demo
atomic:
  pulses:
    - route: ping
      method: get
      handler: GetPing
  fields:
    - name: sequencer
      handler: Sequencer
      secrets: [LEDGER_KEY]
      env:
        BATCH: "50"
`

// `atomic.fields` parses into specs booked as `field:<name>`, carrying the
// handler, the element directory, the secrets and the env the operator is sent,
// and nothing a Pulse has that a Field cannot.
func TestFieldSpecs_ParseTheFieldsList(t *testing.T) {
	m, err := ParseDriftfile(fieldProject(t, fieldDriftfile))
	if err != nil {
		t.Fatalf("a Driftfile declaring a Field must parse:\n%v", err)
	}

	fields := FieldSpecs(m)
	if len(fields) != 1 {
		t.Fatalf("want one Field spec, got %d: %+v", len(fields), fields)
	}
	f := fields[0]
	if f.Name != "field:sequencer" || !f.IsField() {
		t.Errorf("the Field is booked as %q (IsField=%v), want field:sequencer", f.Name, f.IsField())
	}
	if method, name := f.Wire(); method != "field" || name != "sequencer" {
		t.Errorf("the operator is sent method %q and name %q, want field and sequencer", method, name)
	}
	if f.Handler != "Sequencer" || f.Dir != filepath.Join(m.BaseDir(), "atomic") {
		t.Errorf("handler %q in %s, want Sequencer in the default element", f.Handler, f.Dir)
	}
	if strings.Join(f.Secrets, ",") != "LEDGER_KEY" || f.Env["BATCH"] != "50" {
		t.Errorf("secrets %v and env %v did not reach the spec", f.Secrets, f.Env)
	}
	if f.Auth != "" || f.Stream != "" || f.Response != "" {
		t.Errorf("a Field has no gate, stream or reply shape, got %+v", f)
	}

	// Every slot is in AtomicSpecs, the Pulse first, and only the Pulse is in
	// FunctionSpecs.
	var all []string
	for _, s := range AtomicSpecs(m) {
		all = append(all, s.Name)
	}
	if strings.Join(all, ",") != "get:ping,field:sequencer" {
		t.Errorf("AtomicSpecs = %v, want the Pulse then the Field", all)
	}
	if len(FunctionSpecs(m)) != 1 {
		t.Errorf("FunctionSpecs is the Pulses alone, got %d", len(FunctionSpecs(m)))
	}

	// A Field that writes a `memory` is refused by the format: the figure would
	// be read by nothing.
	withMemory := strings.Replace(fieldDriftfile, "handler: Sequencer\n", "handler: Sequencer\n      memory: 32MB\n", 1)
	if _, err := ParseDriftfile(fieldProject(t, withMemory)); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("a Field writing memory must be refused naming the key, got %v", err)
	}

	// The Field is declared, and writes no booking: the slice shape owns it.
	if got, ok := declaredBookings(m)["field:sequencer"]; !ok || got != "" {
		t.Errorf("the Field must be declared with no written booking, got %q (declared=%v)", got, ok)
	}
}

// A Field's booking is its live slot's, so the compiled floor is checked
// against the slice shape: under the floor is refused naming the Field and
// the resize that raises it; at the floor passes; and with no live slice in
// hand there is nothing to check a Field against.
func TestCheckSlotBookings_AFieldIsCheckedAgainstItsLiveSlot(t *testing.T) {
	m, err := ParseDriftfile(fieldProject(t, fieldDriftfile))
	if err != nil {
		t.Fatal(err)
	}
	els, err := atomic_cmd.BuildElements(AtomicSpecs(m))
	if err != nil {
		t.Fatal(err)
	}
	slots := func(fieldBytes int) *LiveSlice {
		l := liveWith([]string{"get:ping"}, nil, nil, nil)
		l.Config.Atomic.Functions = append(l.Config.Atomic.Functions,
			AtomicFunction{Name: "field:sequencer", MemoryBytes: fieldBytes})
		return l
	}

	err = CheckSlotBookings(m, els, slots(8<<20))
	if err == nil {
		t.Fatal("a Go Field booked at 8 MB on the slice must be refused")
	}
	for _, want := range []string{"field:sequencer booked 8 MB", "16MB", "drift slice resize"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, err)
		}
	}
	if err := CheckSlotBookings(m, els, slots(16<<20)); err != nil {
		t.Errorf("a Field booked at the floor must pass: %v", err)
	}
	if err := CheckCompiledBookings(m, els); err != nil {
		t.Errorf("offline, a Field has no booking to check: %v", err)
	}
}

// The retired `atomic.functions` is renamed to `atomic.pulses` in the base and
// inside an environment overlay, announced once naming `drift file migrate`,
// and every reader sees the Pulses under the new key whichever environment is
// selected.
func TestPulsesAlias_RenamesTheBaseAndEveryOverlay(t *testing.T) {
	path := fieldProject(t, `slice: demo
atomic:
  functions:
    - route: ping
      method: get
      handler: GetPing
environments:
  prod: {}
  staging:
    atomic:
      functions:
        - route: ping
          method: post
          handler: GetPing
`)
	var notices bytes.Buffer
	t.Cleanup(common.RedirectDeprecationWarnings(&notices))
	common.ResetDeprecationState()
	t.Cleanup(common.ResetDeprecationState)

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Slice().Has("atomic", "functions") || len(PulseEntries(m.Slice())) != 1 {
		t.Errorf("the base still carries `functions`, or `pulses` is missing: %v", m.Slice().Sub("atomic"))
	}
	if overlay := m.Doc().Sub("environments", "staging", "atomic"); overlay.Has("functions") || !overlay.Has("pulses") {
		t.Errorf("the overlay was not renamed in place: %v", overlay)
	}
	if n := strings.Count(notices.String(), `"atomic.functions" is deprecated in favour of "atomic.pulses"`); n != 1 {
		t.Errorf("the alias was announced %d times, want once:\n%s", n, notices.String())
	}
	if !strings.Contains(notices.String(), "drift file migrate") {
		t.Errorf("the notice must name the command that rewrites the file:\n%s", notices.String())
	}

	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	specs := FunctionSpecs(m)
	if len(specs) != 1 || specs[0].Name != "post:ping" {
		t.Errorf("staging's overlay must replace the base's Pulses, got %+v", specs)
	}
}

// A Field may not share its name with a Pulse's route: the slice keys logs and
// metrics by that bare name. Refused at the parse, naming both, before anything
// is built; and refused again when an overlay is what introduces the clash.
func TestFieldAndPulseNames_MayNotCollide(t *testing.T) {
	clash := `slice: demo
atomic:
  pulses:
    - route: sequencer
      method: post
      handler: GetPing
  fields:
    - name: sequencer
      handler: Sequencer
`
	_, err := ParseDriftfile(fieldProject(t, clash))
	if err == nil {
		t.Fatal("a Field named like a Pulse route must be refused")
	}
	want := `the Field "sequencer" and the Pulse route "sequencer" would share logs and metrics; rename one`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal must say %q, got:\n%v", want, err)
	}

	// Through an overlay: the base is fine, staging introduces the clash.
	viaOverlay := fieldDriftfile + `environments:
  prod: {}
  staging:
    atomic:
      pulses:
        - route: sequencer
          method: get
          handler: GetPing
`
	m, err := ParseDriftfile(fieldProject(t, viaOverlay))
	if err != nil {
		t.Fatalf("the base declares no clash and must parse: %v", err)
	}
	if _, err := m.SelectEnvironment("prod", true); err != nil {
		t.Errorf("prod has no clash and must select: %v", err)
	}
	m, _ = ParseDriftfile(fieldProject(t, viaOverlay))
	if _, err := m.SelectEnvironment("staging", true); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the clash staging introduces must be refused, got %v", err)
	}

	// The control: the fixture's names differ, and it parses.
	if _, err := ParseDriftfile(fieldProject(t, fieldDriftfile)); err != nil {
		t.Errorf("a Field whose name no Pulse route shares must parse: %v", err)
	}
}

// A Field built from an element in any language but Go is refused by the
// build-elements pass, before anything is staged, naming the rule; and the
// apply preflight recognises that refusal as one to stop on.
func TestFieldInANonGoElement_IsRefusedBeforeBuilding(t *testing.T) {
	path := fieldProject(t, fieldDriftfile)
	root := filepath.Dir(path)
	if err := os.Remove(filepath.Join(root, "atomic", "main.go")); err != nil {
		t.Fatal(err)
	}
	src := "function GetPing(req) { return [200, 'OK', {}]; }\n" +
		"async function Sequencer() {}\nmodule.exports = { GetPing, Sequencer };\n"
	if err := os.WriteFile(filepath.Join(root, "atomic", "handlers.js"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := ParseDriftfile(path)
	if err != nil {
		t.Fatalf("the document is legal; the language is what is refused: %v", err)
	}
	_, berr := atomic_cmd.BuildElements(AtomicSpecs(m))
	if berr == nil {
		t.Fatal("a Field in a node element must be refused")
	}
	for _, want := range []string{`field "sequencer"`, "node", "a Field runs Go in this release"} {
		if !strings.Contains(berr.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, berr)
		}
	}
	if !errors.Is(berr, atomic_cmd.ErrFieldLanguage) {
		t.Errorf("the apply preflight stops on ErrFieldLanguage, and this refusal does not carry it: %v", berr)
	}

	// The same element with the Pulse alone builds: the refusal is the Field's.
	if _, err := atomic_cmd.BuildElements(FunctionSpecs(m)); err != nil {
		t.Errorf("the Pulse alone in a node element must build: %v", err)
	}
}
