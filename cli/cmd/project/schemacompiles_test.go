package project

import (
	"os"
	"path/filepath"
	"testing"
)

// THE SCHEMA THE PLATFORM SHIPS MUST COMPILE WITH THE LIBRARY THIS CLI USES.
//
// `loadSchema` reports a compile failure as an error, and `SchemaAvailable()`
// turns any error into "no schema". Everything downstream then behaves as if
// this machine had never met the platform: `drift file lint` prints
// ErrNoSchema and validates NOTHING, and `drift file apply` refuses outright.
// One unusable regex anywhere in the document does that to every Driftfile on
// the machine.
//
// It has happened. A `propertyNames` pattern shipped with a negative lookahead;
// Go's regexp is RE2 and has none, so the whole schema stopped compiling. The
// platform now sweeps its own patterns (drift-common/driftfile), but that
// sweep tests Go's `regexp` and this tests the JSON Schema COMPILER — a
// document can satisfy the first and still fail the second on an unsupported
// keyword or a bad `$ref`, and it is the second that decides whether the CLI
// works.
//
// Skipped rather than failed when the machine holds no schema: that is a fresh
// checkout or an offline CI runner, and it is the condition
// TestSchemaMustBePresentOrTheSuiteIsTheatre already reports loudly once.
func TestTheCachedSchemaActuallyCompiles(t *testing.T) {
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })

	sch, err := loadSchema()
	if err != nil {
		t.Fatalf("the schema on this machine does not compile, so the CLI validates "+
			"nothing and `drift file apply` refuses every manifest:\n  %v", err)
	}
	if sch == nil {
		t.Skip("no schema cached on this machine; TestSchemaMustBePresentOrTheSuiteIsTheatre owns that complaint")
	}
}

// The same compile, against a schema handed in by path — so a change to the
// PLATFORM's document can be checked against this CLI's compiler before it is
// served to anyone.
//
// Point DRIFT_SCHEMA_UNDER_TEST at a candidate file:
//
//	DRIFT_SCHEMA_UNDER_TEST=../../../platform/src/common/driftfile/driftfile.schema.json \
//	  go test ./cmd/project -run TestACandidateSchemaCompiles
//
// Unset, it skips. The two repos are separate and this side cannot reach into
// the other's checkout on its own, so the path is supplied rather than
// guessed — a hardcoded relative path would fail on every machine that does not
// lay the repos out the way one developer's does.
func TestACandidateSchemaCompiles(t *testing.T) {
	path := os.Getenv("DRIFT_SCHEMA_UNDER_TEST")
	if path == "" {
		t.Skip("set DRIFT_SCHEMA_UNDER_TEST to a schema file to check it against this CLI's compiler")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a path the operator supplied, in a test
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".drift"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".drift", "driftfile.schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	compiledSchema = nil
	t.Cleanup(func() { compiledSchema = nil })

	sch, err := loadSchema()
	if err != nil {
		t.Fatalf("%s does not compile with this CLI's schema library — serving it would "+
			"leave every client validating nothing:\n  %v", path, err)
	}
	if sch == nil {
		t.Fatalf("%s produced no schema and no error", path)
	}
}
