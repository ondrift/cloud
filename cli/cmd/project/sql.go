// sql.go — Driftfile reconcile of the SQL primitive. For each declared `sql:`
// entry the CLI uploads the schema, the migrations and the seed to the slice's
// admin endpoints, in that order.
//
//   - `schema` is idempotent DDL (`CREATE … IF NOT EXISTS`), run on every deploy.
//   - `migrations` is a directory of `<number>_<name>.sql` files. Every `.sql`
//     file in it rides the load-schema call beside the schema, and the slice
//     applies each one it has not recorded, once, in numeric order. The slice
//     owns the ordering, the validation and the record; this side only reads
//     the directory and reports what came back.
//   - `seed` runs only while the database holds no data (the slice decides).
//
// A refused migration FAILS THE DEPLOY. A schema with no migrations beside it
// that fails is printed as a hint and the deploy carries on, as it always has:
// a schema written without IF NOT EXISTS fails on every deploy after its first,
// and turning that into a failed deploy would break projects that work today.
// With migrations the same failure is fatal, because no migration ran.
//
// A manifest names a database because it has a schema or a seed for it, not
// because it is the slice's whole inventory. A database the document does not
// mention is left alone — dropping one closes its connection and removes the
// .db, -wal and -shm files, so it has to be a deliberate act and never the
// consequence of an omission. It is reported instead: see unnamed.go.
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// maxSQLLoadBody is the most the api and the operator each read of a SQL admin
// body. Neither refuses a larger one: both forward the first 8 MiB, and the
// slice answers that prefix with a 400 "invalid JSON" that names nothing the
// user wrote.
const maxSQLLoadBody = 8 << 20

// sqlLoadHeadroom keeps a refusal here clear of the cap itself, so a body is
// never sent that only fits while the relays read exactly what they read today.
const sqlLoadHeadroom = 64 << 10

// migrationFile is one file of a `migrations:` directory as the slice takes it.
type migrationFile struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// migrationReport is the slice's answer to a load that carries migrations.
type migrationReport struct {
	Migrations *struct {
		Applied        []string `json:"applied"`
		AlreadyApplied int      `json:"already_applied"`
	} `json:"migrations"`
}

func applySQL(m *Manifest, out io.Writer) ([]string, error) {
	declared := map[string]Node{}
	for _, e := range m.Slice().Entries("name", "backbone", "sql") {
		name := strings.ToLower(strings.TrimSpace(e.Str("name")))
		if name == "" {
			continue
		}
		e["name"] = name
		declared[name] = e
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	// Every database is attempted, so one refused migration does not hide the
	// state of the others; the deploy fails after all of them were tried.
	var failed []error
	for _, name := range names {
		entry := declared[name]
		if entry.Str("migrations") != "" {
			if err := applyMigrations(m, name, entry, out); err != nil {
				failed = append(failed, fmt.Errorf("sql %s: %w", name, err))
				continue
			}
		} else if entry.Str("schema") != "" {
			if err := uploadSchema(m, name, entry.Str("schema")); err != nil {
				fmt.Fprintf(out, "    %s sql %s schema: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Fprintf(out, "    %s sql schema applied: %s ← %s\n",
				common.Check(), name, entry.Str("schema"))
		}
		if entry.Str("seed") != "" {
			if err := uploadSeed(m, name, entry.Str("seed")); err != nil {
				fmt.Fprintf(out, "    %s sql %s seed: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Fprintf(out, "    %s sql seed applied (if empty): %s ← %s\n",
				common.Check(), name, entry.Str("seed"))
		}
	}
	if len(failed) > 0 {
		return nil, errors.Join(failed...)
	}

	// The live inventory is read only to report it, and only after the uploads,
	// so a slice that cannot answer this still gets its schemas and seeds. A
	// failure is STATED rather than swallowed: silence here would read as "every
	// database is named", which is the one thing this must never say wrongly.
	live, err := fetchLiveSQL()
	if err != nil {
		fmt.Fprintf(out, "  %s couldn't check for databases the Driftfile no longer names: %v\n",
			common.Hint("·"), err)
		return nil, nil
	}
	liveNames := make([]string, 0, len(live))
	for _, d := range live {
		liveNames = append(liveNames, strings.ToLower(strings.TrimSpace(d.Name)))
	}
	declaredNames := make(map[string]bool, len(declared))
	for name := range declared {
		declaredNames[name] = true
	}
	return unnamedIn(liveNames, declaredNames), nil
}

// liveSQLDatabase is one row of the slice's `/ops/backbone/sql/admin/list`. Only
// the name is read here; the size belongs to `drift backbone sql list`.
type liveSQLDatabase struct {
	Name string `json:"name"`
}

func fetchLiveSQL() ([]liveSQLDatabase, error) {
	resp, err := common.DoRequest(http.MethodGet,
		common.APIBaseURL+"/ops/backbone/sql/admin/list", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list sql databases")
	if err != nil {
		return nil, err
	}
	var out []liveSQLDatabase
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// applyMigrations sends the schema and every migration file on one load-schema
// call and prints what the slice applied.
//
// The 200 report is REQUIRED. A slice that predates migrations takes the call,
// runs the schema, ignores the worker and answers 204, so anything but a report
// means the files were never applied.
func applyMigrations(m *Manifest, name string, entry Node, out io.Writer) error {
	dir := entry.Str("migrations")
	files, err := readMigrations(m.ResolvePath(dir))
	if err != nil {
		return err
	}
	schema := ""
	if p := entry.Str("schema"); p != "" {
		raw, err := os.ReadFile(m.ResolvePath(p)) // #nosec G304 -- caller-supplied path inside the project root.
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		schema = string(raw)
	}

	resp, err := postSQLAdmin("load-schema", map[string]any{
		"db":         name,
		"schema":     schema,
		"migrations": files,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		return fmt.Errorf("this slice does not apply migrations yet: it ran the schema and ignored "+
			"the %d file(s) in %s, so none of them was applied. Its runtime predates migrations; "+
			"deploy again once it has been updated", len(files), dir)

	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict:
		raw, _ := io.ReadAll(resp.Body)
		var refusal struct {
			Error string `json:"error"`
			File  string `json:"file"`
		}
		if json.Unmarshal(raw, &refusal) != nil || refusal.Error == "" {
			return &common.APIError{Op: "apply migrations", Status: resp.StatusCode,
				Raw: strings.TrimSpace(string(raw))}
		}
		if refusal.File == "" {
			// No file means nothing in the migration set was at fault: the
			// schema itself failed, and no migration ran after it.
			return fmt.Errorf("the schema failed, so no migration ran: %s", refusal.Error)
		}
		return fmt.Errorf("migration %s was refused: %s", refusal.File, refusal.Error)
	}

	body, err := common.CheckResponse(resp, "apply migrations")
	if err != nil {
		return err
	}
	var report migrationReport
	if err := json.Unmarshal(body, &report); err != nil || report.Migrations == nil {
		return fmt.Errorf("the slice answered %d without a migration report, so whether the "+
			"files in %s were applied is unknown", resp.StatusCode, dir)
	}

	if entry.Str("schema") != "" {
		fmt.Fprintf(out, "    %s sql schema applied: %s ← %s\n",
			common.Check(), name, entry.Str("schema"))
	}
	applied := report.Migrations.Applied
	switch len(applied) {
	case 0:
		fmt.Fprintf(out, "    %s sql migrations: %s ← %s %s\n", common.Check(), name, dir,
			common.Hint(fmt.Sprintf("(nothing new, %d already applied)", report.Migrations.AlreadyApplied)))
	default:
		fmt.Fprintf(out, "    %s sql migrations applied: %s ← %s %s\n", common.Check(), name,
			strings.Join(applied, ", "),
			common.Hint(fmt.Sprintf("(%d already applied)", report.Migrations.AlreadyApplied)))
	}
	return nil
}

// readMigrations reads every `*.sql` file directly inside dir, in name order.
// Anything else — a README, a subdirectory — is not a migration and is not
// sent. Whether a name is a valid migration name is the slice's to decide.
func readMigrations(dir string) ([]migrationFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}
	files := []migrationFile{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- a file inside the project's own migrations directory.
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		files = append(files, migrationFile{Name: e.Name(), SQL: string(raw)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// postSQLAdmin sends one SQL admin call, refusing a body the relays would cut.
func postSQLAdmin(verb string, payload map[string]any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(body) > maxSQLLoadBody-sqlLoadHeadroom {
		return nil, fmt.Errorf("this %s comes to %.1f MiB, and the platform accepts at most "+
			"%d MiB in one call. A larger body arrives cut short and is refused as invalid JSON, "+
			"so it is not sent. Make the files it carries smaller",
			verb, float64(len(body))/(1<<20), maxSQLLoadBody>>20)
	}
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/"+verb, bytes.NewReader(body))
	if err != nil {
		return nil, common.TransportError(strings.ReplaceAll(verb, "-", " "), err)
	}
	return resp, nil
}

func uploadSchema(m *Manifest, name, schemaPath string) error {
	raw, err := os.ReadFile(m.ResolvePath(schemaPath)) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", schemaPath, err)
	}
	resp, err := postSQLAdmin("load-schema", map[string]any{
		"db":     name,
		"schema": string(raw),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "load schema")
		return e
	}
	return nil
}

func uploadSeed(m *Manifest, name, seedPath string) error {
	raw, err := os.ReadFile(m.ResolvePath(seedPath)) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", seedPath, err)
	}
	resp, err := postSQLAdmin("load-seed", map[string]any{
		"db":   name,
		"seed": string(raw),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "load seed")
		return e
	}
	return nil
}
