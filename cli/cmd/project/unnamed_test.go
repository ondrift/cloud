package project

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqlListBody is the live inventory the slice reports for the SQL class. The
// manifest in each test names `orders` only, so the other two are unnamed.
const sqlListBody = `[{"name":"orders","size_bytes":1},{"name":"ledger","size_bytes":2},{"name":"audit","size_bytes":3}]`

// liveEverything answers each class's list endpoint with a set larger than any
// test manifest names, so a reconcile that reports nothing has failed to join
// rather than found nothing.
func liveEverything(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ops/backbone/sql/admin/list":
		_, _ = w.Write([]byte(sqlListBody))
	case "/ops/atomic/alert":
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"name":"post:checkout-0"},{"name":"get:health-0"}]`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	case "/ops/slice/domain":
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"host":"shop.example.com","status":"live"},{"host":"admin.example.com","status":"live"}]`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}
}

// schemaProject writes the one schema file applySQL uploads, so the declared
// database is exercised rather than skipped.
func schemaProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "orders.sql"),
		[]byte("CREATE TABLE IF NOT EXISTS o (id TEXT);"), 0o600); err != nil {
		t.Fatalf("writing the schema file: %v", err)
	}
	return root
}

// A database live on the slice that the Driftfile does not name is invisible:
// nothing on the apply path removes it and nothing reports it, so a renamed or
// dropped `sql:` entry leaves its data on disk with no mention anywhere.
func TestApplySQL_ReportsADatabaseTheManifestDoesNotName(t *testing.T) {
	rec := stubAPI(t, liveEverything)

	m := manifestRooted(Node{"name": "demo", "backbone": map[string]any{
		"sql": []any{map[string]any{"name": "orders", "schema": "orders.sql"}},
	}}, schemaProject(t))

	unnamed, err := applySQL(m)
	if err != nil {
		t.Fatalf("applySQL: %v", err)
	}

	if got := strings.Join(unnamed, ","); got != "audit,ledger" {
		t.Errorf("the databases the manifest does not name must be reported in a stable order, got %q", got)
	}
	if n := rec.count("POST /ops/backbone/sql/admin/drop"); n != 0 {
		t.Errorf("reporting must not delete: got %d drop request(s)", n)
	}
}

// The alert registry is keyed on the derived `<function>-<index>` name, so the
// join has to be against that spelling and not against the function.
func TestApplyAlerts_ReportsAnAlertTheManifestDoesNotName(t *testing.T) {
	rec := stubAPI(t, liveEverything)

	m := manifestRooted(Node{"name": "demo", "atomic": map[string]any{
		"functions": []any{map[string]any{
			"name": "post:checkout", "handler": "PostCheckout",
			"alerts": []any{map[string]any{
				"on": "errors", "threshold": 3, "window": "5m", "notify": "webhook=https://example.com/hook",
			}},
		}},
	}}, t.TempDir())

	unnamed, err := applyAlerts(m)
	if err != nil {
		t.Fatalf("applyAlerts: %v", err)
	}

	if got := strings.Join(unnamed, ","); got != "get:health-0" {
		t.Errorf("the alert the manifest does not declare must be reported, got %q", got)
	}
	if n := rec.count("DELETE /ops/atomic/alert"); n != 0 {
		t.Errorf("reporting must not delete: got %d delete request(s)", n)
	}
}

// A host is matched lowercased, because that is how applyDomains declares one
// and how the slice stores it.
func TestApplyDomains_ReportsAHostTheManifestDoesNotName(t *testing.T) {
	rec := stubAPI(t, liveEverything)

	m := manifestRooted(Node{"name": "demo", "domains": []any{
		map[string]any{"host": "SHOP.example.com"},
	}}, t.TempDir())

	unnamed, err := applyDomains(m)
	if err != nil {
		t.Fatalf("applyDomains: %v", err)
	}

	if got := strings.Join(unnamed, ","); got != "admin.example.com" {
		t.Errorf("the host the manifest does not declare must be reported, and a declared host must match case-insensitively, got %q", got)
	}
	if n := rec.count("DELETE /ops/slice/domain"); n != 0 {
		t.Errorf("reporting must not delete: got %d delete request(s)", n)
	}
}

// The control: a manifest naming everything live reports nothing from any of
// the three classes. Without it every test above passes for a reconcile that
// returns its whole live set.
func TestApply_ReportsNothingWhenTheManifestNamesEverythingLive(t *testing.T) {
	stubAPI(t, liveEverything)

	root := schemaProject(t)
	slice := Node{
		"name": "demo",
		"domains": []any{
			map[string]any{"host": "shop.example.com"},
			map[string]any{"host": "admin.example.com"},
		},
		"backbone": map[string]any{"sql": []any{
			map[string]any{"name": "orders", "schema": "orders.sql"},
			map[string]any{"name": "ledger"},
			map[string]any{"name": "audit"},
		}},
		"atomic": map[string]any{"functions": []any{
			map[string]any{
				"name": "post:checkout", "handler": "PostCheckout",
				"alerts": []any{map[string]any{
					"on": "errors", "threshold": 3, "window": "5m", "notify": "webhook=https://example.com/hook",
				}},
			},
			map[string]any{
				"name": "get:health", "handler": "GetHealth",
				"alerts": []any{map[string]any{
					"on": "errors", "threshold": 1, "window": "5m", "notify": "webhook=https://example.com/hook",
				}},
			},
		}},
	}
	m := manifestRooted(slice, root)

	dbs, err := applySQL(m)
	if err != nil {
		t.Fatalf("applySQL: %v", err)
	}
	alerts, err := applyAlerts(m)
	if err != nil {
		t.Fatalf("applyAlerts: %v", err)
	}
	domains, err := applyDomains(m)
	if err != nil {
		t.Fatalf("applyDomains: %v", err)
	}

	for _, c := range []struct {
		class string
		got   []string
	}{{"sql database", dbs}, {"alert", alerts}, {"domain", domains}} {
		if len(c.got) != 0 {
			t.Errorf("every live %s is named by this manifest, so none must be reported, got %q", c.class, c.got)
		}
	}
}

// The report names each class, the resource, and the command that removes it —
// the three things that turn "something is live" into an action.
func TestReportUnnamedResources_NamesEachClassAndHowToRemoveIt(t *testing.T) {
	var out bytes.Buffer
	reportUnnamedResources(unnamedResources{
		Databases: []string{"ledger"},
		Alerts:    []string{"get:health-0"},
		Domains:   []string{"admin.example.com"},
	}, &out)

	got := out.String()
	for _, want := range []string{
		"ledger", "drift backbone sql drop ledger",
		"get:health-0", "drift atomic alert remove get:health-0",
		"admin.example.com", "drift slice domain remove admin.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report must carry %q, got %q", want, got)
		}
	}
	if !strings.Contains(got, "Nothing was removed") {
		t.Errorf("the report must say that nothing was deleted, and why, got %q", got)
	}
}

// The control: nothing unnamed prints nothing at all, so an ordinary apply
// gains no noise. It matches ReportOrphanedFunctions, which is the section this
// one sits beside.
func TestReportUnnamedResources_NothingUnnamedPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	reportUnnamedResources(unnamedResources{}, &out)

	if out.Len() != 0 {
		t.Errorf("nothing is unnamed, so nothing must be printed, got %q", out.String())
	}
}
