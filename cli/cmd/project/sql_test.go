package project

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// loadSchemaPath and loadSeedPath are the two SQL admin calls a deploy makes.
const (
	loadSchemaPath = "/ops/backbone/sql/admin/load-schema"
	loadSeedPath   = "/ops/backbone/sql/admin/load-seed"
)

// sqlCalls records what reached the SQL admin endpoints, in order.
type sqlCalls struct {
	mu     sync.Mutex
	order  []string
	bodies map[string][]byte
}

func (c *sqlCalls) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = append(c.order, r.URL.Path)
	if c.bodies == nil {
		c.bodies = map[string][]byte{}
	}
	c.bodies[r.URL.Path] = b
}

func (c *sqlCalls) body(path string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[path]
}

// stubSQL answers load-schema with the given status and body, and every other
// path the way liveEverything does, recording every SQL admin call.
func stubSQL(t *testing.T, status int, reply string) *sqlCalls {
	t.Helper()
	calls := &sqlCalls{}
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case loadSchemaPath:
			calls.record(r)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		case loadSeedPath:
			calls.record(r)
			w.WriteHeader(http.StatusNoContent)
		default:
			liveEverything(w, r)
		}
	})
	return calls
}

// migrationProject lays out a project with a migrations directory holding two
// migrations, and files beside them that are not migrations.
func migrationProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "migrations")
	mustMkdir(t, filepath.Join(dir, "archive"))
	mustWrite(t, filepath.Join(dir, "0002_add_email.sql"), "ALTER TABLE users ADD COLUMN email TEXT;")
	mustWrite(t, filepath.Join(dir, "0001_users.sql"), "CREATE TABLE users (id INTEGER PRIMARY KEY);")
	mustWrite(t, filepath.Join(dir, "README.md"), "how we migrate")
	mustWrite(t, filepath.Join(dir, "0003_draft.sql.bak"), "DROP TABLE users;")
	mustWrite(t, filepath.Join(dir, "archive", "0000_old.sql"), "DROP TABLE users;")
	mustWrite(t, filepath.Join(root, "orders.sql"), "CREATE TABLE IF NOT EXISTS o (id TEXT);")
	mustWrite(t, filepath.Join(root, "seed.sql"), "INSERT INTO users VALUES (1);")
	return root
}

func sqlManifest(root string, entry map[string]any) *Manifest {
	return manifestRooted(Node{"name": "demo", "backbone": map[string]any{
		"sql": []any{entry},
	}}, root)
}

const reportBody = `{"migrations":{"applied":["0002_add_email.sql"],"already_applied":1}}`

type sentLoad struct {
	DB         string            `json:"db"`
	Schema     *string           `json:"schema"`
	Migrations []json.RawMessage `json:"migrations"`
}

func decodeLoad(t *testing.T, raw []byte) (sentLoad, []map[string]string, map[string]json.RawMessage) {
	t.Helper()
	var s sentLoad
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("the load-schema body does not parse: %v (%s)", err, raw)
	}
	var files []map[string]string
	for _, m := range s.Migrations {
		var f map[string]string
		if err := json.Unmarshal(m, &f); err != nil {
			t.Fatalf("a migration entry does not parse: %v (%s)", err, m)
		}
		files = append(files, f)
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	return s, files, keys
}

// The directory's .sql files ride the load-schema call beside the schema, one
// {name, sql} per file, and nothing else in the directory is sent: not a README,
// not a backup with .sql inside its name, not a file in a subdirectory.
func TestApplySQL_SendsEveryMigrationFileWithTheSchema(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, reportBody)
	root := migrationProject(t)

	var out bytes.Buffer
	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "schema": "orders.sql", "migrations": "./migrations",
	}), &out); err != nil {
		t.Fatalf("applySQL: %v", err)
	}

	s, files, _ := decodeLoad(t, calls.body(loadSchemaPath))
	if s.DB != "orders" {
		t.Errorf("db = %q, want orders", s.DB)
	}
	if s.Schema == nil || *s.Schema != "CREATE TABLE IF NOT EXISTS o (id TEXT);" {
		t.Errorf("schema = %v, want the schema file's contents", s.Schema)
	}
	if len(files) != 2 {
		t.Fatalf("sent %d migration files, want the 2 .sql files: %v", len(files), files)
	}
	if files[0]["name"] != "0001_users.sql" || files[1]["name"] != "0002_add_email.sql" {
		t.Errorf("sent names %q and %q, want 0001_users.sql and 0002_add_email.sql",
			files[0]["name"], files[1]["name"])
	}
	if files[1]["sql"] != "ALTER TABLE users ADD COLUMN email TEXT;" {
		t.Errorf("sent sql %q, want the file's contents unchanged", files[1]["sql"])
	}
	if n := len(calls.order); n != 1 {
		t.Errorf("made %d SQL admin calls, want exactly one load-schema", n)
	}
}

// A database with migrations and no schema still sends the key, empty: the
// slice reads `schema` as the baseline to run first, and "" is "none".
func TestApplySQL_MigrationsWithoutASchemaSendAnEmptySchema(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, reportBody)
	root := migrationProject(t)

	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations",
	}), io.Discard); err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	s, files, _ := decodeLoad(t, calls.body(loadSchemaPath))
	if s.Schema == nil || *s.Schema != "" {
		t.Errorf("schema = %v, want the key present and empty", s.Schema)
	}
	if len(files) != 2 {
		t.Errorf("sent %d migration files, want 2", len(files))
	}
}

// An EMPTY directory is still a declaration of migrations: the list goes as [],
// which is what makes the slice answer with a report rather than a bare 204.
func TestApplySQL_AnEmptyMigrationsDirectorySendsAnEmptyList(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, `{"migrations":{"applied":[],"already_applied":0}}`)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "migrations"))

	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations",
	}), io.Discard); err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	if !strings.Contains(string(calls.body(loadSchemaPath)), `"migrations":[]`) {
		t.Errorf("body %s does not carry an empty migration list", calls.body(loadSchemaPath))
	}
}

// What the slice applied is printed by name, and what it had already applied by
// count, so the person deploying sees which changes landed on this run.
func TestApplySQL_ReportsWhatWasAppliedAndHowManyWereAlready(t *testing.T) {
	stubSQL(t, http.StatusOK, reportBody)
	root := migrationProject(t)

	var out bytes.Buffer
	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations",
	}), &out); err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	got := out.String()
	for _, want := range []string{"orders", "0002_add_email.sql", "1 already applied"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report must carry %q, got:\n%s", want, got)
		}
	}
}

// A refusal FAILS THE DEPLOY. The schema's own failure is a hint and the deploy
// carries on; a migration that did not apply must not read as `Done`, because
// every migration after it was not attempted either.
func TestApplySQL_AMigrationRefusalFailsTheDeploy(t *testing.T) {
	for _, tc := range []struct {
		what   string
		status int
		reply  string
		file   string
		reason string
	}{
		{"a migration that failed and was rolled back", http.StatusBadRequest,
			`{"error":"migration 0002_add_email.sql failed and was rolled back: no such table: users.","file":"0002_add_email.sql"}`,
			"0002_add_email.sql", "no such table: users"},
		{"an applied migration whose contents changed", http.StatusConflict,
			`{"error":"migration 0001_users.sql was applied at 2026-10-01T10:00:00Z (as 0001_users.sql) and its contents have changed since","file":"0001_users.sql"}`,
			"0001_users.sql", "contents have changed"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			calls := stubSQL(t, tc.status, tc.reply)
			root := migrationProject(t)

			_, err := applySQL(sqlManifest(root, map[string]any{
				"name": "orders", "migrations": "migrations", "seed": "seed.sql",
			}), io.Discard)
			if err == nil {
				t.Fatal("a refused migration was reported as success, so the deploy exits 0")
			}
			for _, want := range []string{"orders", tc.file, tc.reason} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error must carry %q, got: %v", want, err)
				}
			}
			for _, p := range calls.order {
				if p == loadSeedPath {
					t.Error("the seed ran after the migrations were refused")
				}
			}
		})
	}
}

// A slice that predates migrations takes the call, ignores the field and
// answers the bare 204 it always gave. The files were never applied, so a
// success here would be a lie.
func TestApplySQL_ASliceThatPredatesMigrationsFailsTheDeploy(t *testing.T) {
	stubSQL(t, http.StatusNoContent, "")
	root := migrationProject(t)

	_, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations",
	}), io.Discard)
	if err == nil {
		t.Fatal("a 204 was taken as applied migrations")
	}
	if !strings.Contains(err.Error(), "does not apply migrations yet") {
		t.Errorf("the error must say the slice does not apply migrations, got: %v", err)
	}
}

// The seed runs after the load-schema call that carries the migrations, so it
// lands in the tables they made.
func TestApplySQL_TheSeedRunsAfterTheMigrations(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, reportBody)
	root := migrationProject(t)

	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations", "seed": "seed.sql",
	}), io.Discard); err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	if strings.Join(calls.order, ",") != loadSchemaPath+","+loadSeedPath {
		t.Errorf("calls = %v, want load-schema then load-seed", calls.order)
	}
}

// The api and the operator each read at most 8 MiB of this body and forward the
// prefix, which the slice then refuses as invalid JSON. So a load that large is
// refused HERE, with its size, and never sent.
func TestApplySQL_ABodyNearThePlatformCapIsRefusedBeforeItIsSent(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, reportBody)
	root := migrationProject(t)
	mustWrite(t, filepath.Join(root, "migrations", "0003_bulk.sql"),
		"-- "+strings.Repeat("x", maxSQLLoadBody)+"\n")

	_, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "migrations": "migrations",
	}), io.Discard)
	if err == nil {
		t.Fatal("a body over the platform's cap was sent")
	}
	if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("the refusal must name the size, got: %v", err)
	}
	if len(calls.order) != 0 {
		t.Errorf("made %d SQL admin calls for a body that cannot arrive whole", len(calls.order))
	}
}

// A migrations directory that is not there fails the deploy rather than sending
// an empty set, which would apply nothing and report success.
func TestApplySQL_AMissingMigrationsDirectoryFailsTheDeploy(t *testing.T) {
	calls := stubSQL(t, http.StatusOK, reportBody)

	_, err := applySQL(sqlManifest(t.TempDir(), map[string]any{
		"name": "orders", "migrations": "migrations",
	}), io.Discard)
	if err == nil {
		t.Fatal("a missing migrations directory was reported as success")
	}
	if len(calls.order) != 0 {
		t.Errorf("made %d SQL admin calls with no migrations to send", len(calls.order))
	}
}

// THE CONTROL. A database that declares no migrations sends no `migrations`
// key at all, takes the 204 every slice gives that call, and a failing schema
// stays a hint, exactly as before.
func TestApplySQL_ASchemaWithoutMigrationsIsUnchanged(t *testing.T) {
	calls := stubSQL(t, http.StatusNoContent, "")
	root := migrationProject(t)

	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "schema": "orders.sql",
	}), io.Discard); err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	_, _, keys := decodeLoad(t, calls.body(loadSchemaPath))
	if _, ok := keys["migrations"]; ok {
		t.Error("a database with no migrations sent the key, so the slice would create a history table it never asked for")
	}

	stubSQL(t, http.StatusBadRequest, `{"error":"near \"CREAT\": syntax error"}`)
	var out bytes.Buffer
	if _, err := applySQL(sqlManifest(root, map[string]any{
		"name": "orders", "schema": "orders.sql",
	}), &out); err != nil {
		t.Errorf("a failing schema with no migrations fails the deploy, which it never did: %v", err)
	}
	if !strings.Contains(out.String(), "syntax error") {
		t.Errorf("the schema's failure must still be printed, got:\n%s", out.String())
	}
}

// A migrations directory is a path the file names, so its absence is reported
// at parse time, at the attribute that names it, with every other missing path.
func TestParseDriftfile_ReportsAMissingMigrationsDirectory(t *testing.T) {
	withRealSchema(t)
	body := "slice \"demo\" { billing = \"monthly\" }\n" +
		"sql \"orders\" {\n  size       = \"10MiB\"\n  migrations = \"./migrations\"\n}\n"
	path := writeDriftfile(t, body)

	_, err := ParseDriftfile(path)
	if err == nil || !strings.Contains(err.Error(), "migrations") || !strings.Contains(err.Error(), "Driftfile:4:") {
		t.Fatalf("got %v, want one error naming the missing migrations directory at line 4", err)
	}

	mustMkdir(t, filepath.Join(filepath.Dir(path), "migrations"))
	if _, err := ParseDriftfile(path); err != nil {
		t.Errorf("the directory exists, so nothing must be reported: %v", err)
	}
}

// `migrations` passes through the parse untouched, under a schema that defines
// it. The CLI holds no typed mirror of a database entry, so this is the whole of
// what "implements 1.18.0" asks of the parse.
//
// The schema is the one under test when DRIFT_SCHEMA_UNDER_TEST is set, and the
// cached one otherwise. A schema older than 1.18.0 refuses the key, so it skips.
func TestParse_CarriesSQLMigrationsThrough(t *testing.T) {
	raw, err := schemaForRoundTrip(t)
	if err != nil {
		t.Skip(err.Error())
	}
	var doc struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(raw, &doc)
	if common.DriftfileMajor(doc.Version) == 1 && minorOf(doc.Version) < 18 {
		t.Skipf("the schema here is %s, which predates `migrations`", doc.Version)
	}
	withSchema(t, string(raw))

	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "migrations"))
	mustWrite(t, filepath.Join(root, "Driftfile"),
		"slice \"demo\" { billing = \"monthly\" }\n"+
			"sql \"orders\" {\n  size       = \"10MiB\"\n  migrations = \"./migrations\"\n}\n")
	m, err := ParseDriftfile(filepath.Join(root, "Driftfile"))
	if err != nil {
		t.Fatalf("a Driftfile declaring migrations does not parse under %s: %v", doc.Version, err)
	}
	entries := m.Slice().Entries("name", "backbone", "sql")
	if len(entries) != 1 || entries[0].Str("migrations") != "./migrations" {
		t.Errorf("the parsed entry lost `migrations`: %v", entries)
	}
}

func schemaForRoundTrip(t *testing.T) ([]byte, error) {
	t.Helper()
	if p := os.Getenv("DRIFT_SCHEMA_UNDER_TEST"); p != "" {
		return os.ReadFile(p) // #nosec G304 -- a path the operator supplied, in a test
	}
	return common.LocalDriftfileSchema()
}

func minorOf(v string) int {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 {
		return 0
	}
	n := 0
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
