package account

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A platform with enough in it to exercise the walk: two slices, a collection
// bigger than one page, a bucket, secrets, and Deed records.
//
// The LIVE export could not reach any of this. Run against a real account it
// produced a correct archive — and that account declares no collections and no
// buckets, so the two loops that matter never executed. This is what runs them.
func fakePlatform(t *testing.T, seenSliceHeaders *[]string) *httptest.Server {
	t.Helper()

	// 1200 documents, so the walk has to page: one request is capped at 1000 and
	// says nothing about being short.
	const total = 1200
	docs := make([]json.RawMessage, total)
	for i := range docs {
		docs[i] = json.RawMessage(fmt.Sprintf(`{"_key":"%d","n":%d}`, i+1, i+1))
	}

	mux := http.NewServeMux()
	record := func(r *http.Request) {
		if seenSliceHeaders != nil {
			*seenSliceHeaders = append(*seenSliceHeaders, r.Header.Get("X-Slice"))
		}
	}

	mux.HandleFunc("/ops/slice/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "alpha", "tier": "hacker", "monthly_cost_cents": 0},
			{"name": "beta", "tier": "custom", "monthly_cost_cents": 241},
		})
	})
	mux.HandleFunc("/ops/slice/get", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		cfg := map[string]any{"backbone": map[string]any{}}
		// Only `alpha` declares anything, so the test also proves a slice with
		// nothing is written without inventing empty files for it.
		if name == "alpha" {
			cfg = map[string]any{"backbone": map[string]any{
				"nosql": map[string]any{"Collections": map[string]int{"events": 1}},
				"blobs": map[string]any{"Buckets": map[string]int{"assets": 1}},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tier": "custom", "config": cfg})
	})

	mux.HandleFunc("/ops/backbone/nosql/list", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 1000
		}
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			n, _ := strconv.Atoi(after)
			start = n // keys are 1-based, so "after key n" starts at index n
		}
		end := start + limit
		if end > len(docs) {
			end = len(docs)
		}
		if start > len(docs) {
			start = len(docs)
		}
		_ = json.NewEncoder(w).Encode(docs[start:end])
	})

	mux.HandleFunc("/ops/backbone/blob/list", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode([]string{"logo.png", "nested/report.pdf"})
	})
	mux.HandleFunc("/ops/backbone/blob/get", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte("BLOB:" + r.URL.Query().Get("key")))
	})

	mux.HandleFunc("/ops/backbone/secret/list", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode([]string{"STRIPE_KEY", "DB_URL"})
	})
	mux.HandleFunc("/ops/backbone/secret/get", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte("THE-PLAINTEXT-VALUE"))
	})

	mux.HandleFunc("/ops/deed/admin/vault/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]string{})
	})
	mux.HandleFunc("/ops/deed/admin/link/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]string{})
	})

	// Anything unrouted is a 404, so a path this command gets wrong shows up as a
	// recorded problem rather than as a silently empty section.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// loginAgainst points the CLI at the fake platform with a session whose token
// carries a username, since the export refuses without one.
func loginAgainst(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.TokenEnv, "")
	t.Setenv(common.SliceEnv, "")
	t.Setenv(common.AccountEnv, "")

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"username":"tester","exp":9999999999}`))
	if err := common.SaveSession("h."+payload+".s", "refresh"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
}

// runExport executes the command in a temp working directory and returns the
// archive it wrote.
func runExport(t *testing.T, args ...string) *zip.ReadCloser {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "export.zip")

	cmd := GetExportCmd()
	cmd.SetArgs(append([]string{"--out", out}, args...))
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}
	z, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() { z.Close() })
	return z
}

func read(t *testing.T, z *zip.ReadCloser, suffix string) string {
	t.Helper()
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, suffix) {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", f.Name, err)
			}
			defer rc.Close()
			b := new(strings.Builder)
			if _, err := io_Copy(b, rc); err != nil {
				t.Fatalf("read %s: %v", f.Name, err)
			}
			return b.String()
		}
	}
	t.Fatalf("no entry ending %q in %v", suffix, names(z))
	return ""
}

func has(z *zip.ReadCloser, suffix string) bool {
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, suffix) {
			return true
		}
	}
	return false
}

func names(z *zip.ReadCloser) []string {
	var out []string
	for _, f := range z.File {
		out = append(out, f.Name)
	}
	return out
}

// io.Copy, spelled locally so the import list stays the one this file needs.
func io_Copy(dst *strings.Builder, src interface{ Read([]byte) (int, error) }) (int, error) {
	buf := make([]byte, 32*1024)
	n := 0
	for {
		c, err := src.Read(buf)
		if c > 0 {
			dst.Write(buf[:c])
			n += c
		}
		if err != nil {
			if err.Error() == "EOF" {
				return n, nil
			}
			return n, nil
		}
	}
}

// A COLLECTION BIGGER THAN ONE PAGE IS EXPORTED WHOLE.
//
// One list request is capped at 1000 documents and its reply carries nothing
// marking it short, so an export that took a single page would silently truncate
// every collection past that — and rows arrive in key order rather than
// newest-first, so what it dropped would be arbitrary rather than merely recent.
// An archive missing 200 of 1200 documents looks exactly like a complete one.
func TestACollectionBiggerThanOnePageIsExportedWhole(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)
	z := runExport(t)

	var docs []map[string]any
	if err := json.Unmarshal([]byte(read(t, z, "nosql/events.json")), &docs); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(docs) != 1200 {
		t.Fatalf("exported %d documents, want 1200 — the walk stopped at a page boundary", len(docs))
	}
	// Order and identity, not just the count: a walk that re-requested page one
	// would also produce 1200.
	if docs[0]["n"] != float64(1) || docs[len(docs)-1]["n"] != float64(1200) {
		t.Errorf("first/last are %v/%v, want 1/1200", docs[0]["n"], docs[len(docs)-1]["n"])
	}
}

// Every blob is fetched and written under its own key, slashes included.
func TestBlobsAreWrittenUnderTheirOwnKeys(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)
	z := runExport(t)

	if got := read(t, z, "blobs/assets/logo.png"); got != "BLOB:logo.png" {
		t.Errorf("logo.png holds %q", got)
	}
	// A key containing a slash lands as the nested path it already is.
	if got := read(t, z, "blobs/assets/nested/report.pdf"); got != "BLOB:nested/report.pdf" {
		t.Errorf("nested key holds %q", got)
	}
}

// THE DEFAULT MUST NOT PUT CREDENTIALS IN THE FILE.
//
// A leaked export should not be the same thing as a leaked credential store, so
// the names are in and the values are not — and the assertion is on the whole
// archive's bytes rather than on one entry, because a value that leaked into
// some other file would pass a check that only read secrets.json.
func TestSecretValuesAreAbsentUnlessAskedFor(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)
	z := runExport(t)

	all := new(strings.Builder)
	for _, f := range z.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		_, _ = io_Copy(all, rc)
		rc.Close()
	}
	if strings.Contains(all.String(), "THE-PLAINTEXT-VALUE") {
		t.Fatal("a secret VALUE is in the archive by default")
	}
	if !strings.Contains(all.String(), "STRIPE_KEY") {
		t.Error("secret NAMES should be exported — they are the tenant's own map of what they integrate with")
	}
}

// EVERY REQUEST NAMES THE SLICE IT IS FOR.
//
// `/ops/*` reads X-Slice to decide which slice it addresses, and the CLI fills
// it from the session. An export walks every slice, so without an explicit
// header it would read the ACTIVE slice's data once per slice and write the same
// documents under each name — a wrong archive that looks complete.
func TestEveryReadNamesTheSliceItIsFor(t *testing.T) {
	var seen []string
	srv := fakePlatform(t, &seen)
	loginAgainst(t, srv)
	_ = runExport(t)

	if len(seen) == 0 {
		t.Fatal("no per-slice reads were recorded")
	}
	for _, s := range seen {
		if s == "" {
			t.Fatalf("a read went out with no X-Slice header: %v", seen)
		}
	}
	// BOTH slices must appear. Only `alpha` declares collections and buckets,
	// but secrets and Deed are read for every slice — so seeing both names is
	// what proves the header is per-slice rather than the session's one active
	// slice repeated. A run that addressed everything to one name would satisfy
	// the non-empty check above and still be the bug.
	var sawAlpha, sawBeta bool
	for _, s := range seen {
		switch s {
		case "alpha":
			sawAlpha = true
		case "beta":
			sawBeta = true
		default:
			t.Errorf("a read was addressed to unknown slice %q", s)
		}
	}
	if !sawAlpha || !sawBeta {
		t.Errorf("reads covered alpha=%v beta=%v; both are expected, or the walk is "+
			"using one slice's name for every slice: %v", sawAlpha, sawBeta, seen)
	}
}

// The README is the first thing a person opens, and it has to state what is NOT
// in the archive — otherwise the omissions read as "there was nothing".
func TestTheArchiveSaysWhatItDoesNotContain(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)
	z := runExport(t)

	readme := read(t, z, "README.txt")
	for _, must := range []string{
		"Secret VALUES",
		"Pocket VALUES",
		"function CODE",
		"password",
	} {
		if !strings.Contains(readme, must) {
			t.Errorf("README does not account for %q:\n%s", must, readme)
		}
	}
}

// A slice that declares nothing still gets its shape written, and no invented
// empty collections beside it.
func TestASliceThatDeclaresNothingStillGetsItsShape(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)
	z := runExport(t)

	if !has(z, "slices/beta/slice.json") {
		t.Fatalf("beta's shape is missing: %v", names(z))
	}
	for _, f := range z.File {
		if strings.Contains(f.Name, "slices/beta/nosql/") || strings.Contains(f.Name, "slices/beta/blobs/") {
			t.Errorf("beta declares no collections or buckets, but the archive holds %s", f.Name)
		}
	}
}

// The archive is written 0600. It can hold every document an account has, and
// with --include-secret-values it holds credentials; a world-readable default
// would undo the confirmation that guards them.
func TestTheArchiveIsNotWorldReadable(t *testing.T) {
	srv := fakePlatform(t, nil)
	loginAgainst(t, srv)

	dir := t.TempDir()
	out := filepath.Join(dir, "export.zip")
	cmd := GetExportCmd()
	cmd.SetArgs([]string{"--out", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("export: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("archive mode is %o, want 600", mode)
	}
}
