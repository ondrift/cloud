// sql.go — Driftfile reconcile of the SQL primitive. For each
// declared `sql:` entry the CLI uploads the schema + seed SQL files
// to the slice's admin endpoints. Idempotent — schemas are expected
// to be `CREATE … IF NOT EXISTS`, seeds are only applied when the
// database has no user tables yet (the slice handles this).
//
// A manifest names a database because it has a schema or a seed for
// it, not because it is the slice's whole inventory. A database the
// document does not mention is left alone — dropping one closes its
// connection and removes the .db, -wal and -shm files, so it has to
// be a deliberate act and never the consequence of an omission. It is
// reported instead: see unnamed.go.
package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

func applySQL(m *Manifest) ([]string, error) {
	declared := map[string]Node{}
	for _, e := range m.Slice().Entries("name", "backbone", "sql") {
		name := strings.ToLower(strings.TrimSpace(e.Str("name")))
		if name == "" {
			continue
		}
		e["name"] = name
		declared[name] = e
	}

	for name, entry := range declared {
		if entry.Str("schema") != "" {
			if err := uploadSchema(m.baseDir, name, entry.Str("schema")); err != nil {
				fmt.Printf("  %s sql %s schema: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Printf("  %s sql schema applied: %s ← %s\n",
				common.Check(), name, entry.Str("schema"))
		}
		if entry.Str("seed") != "" {
			if err := uploadSeed(m.baseDir, name, entry.Str("seed")); err != nil {
				fmt.Printf("  %s sql %s seed: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Printf("  %s sql seed applied (if empty): %s ← %s\n",
				common.Check(), name, entry.Str("seed"))
		}
	}

	// The live inventory is read only to report it, and only after the uploads,
	// so a slice that cannot answer this still gets its schemas and seeds. A
	// failure is STATED rather than swallowed: silence here would read as "every
	// database is named", which is the one thing this must never say wrongly.
	live, err := fetchLiveSQL()
	if err != nil {
		fmt.Printf("  %s couldn't check for databases the Driftfile no longer names: %v\n",
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

func uploadSchema(baseDir, name, schemaPath string) error {
	abs := schemaPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, schemaPath)
	}
	raw, err := os.ReadFile(abs) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", schemaPath, err)
	}
	body, _ := json.Marshal(map[string]any{
		"db":     name,
		"schema": string(raw),
	})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/load-schema",
		strings.NewReader(string(body)))
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

func uploadSeed(baseDir, name, seedPath string) error {
	abs := seedPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, seedPath)
	}
	raw, err := os.ReadFile(abs) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", seedPath, err)
	}
	body, _ := json.Marshal(map[string]any{
		"db":   name,
		"seed": string(raw),
	})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/load-seed",
		strings.NewReader(string(body)))
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
