package project

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// applyPlatform is a fake platform for `drift apply`: the reads a plan makes,
// and the writes an apply may make, each recorded with its body.
type applyPlatform struct {
	mu      sync.Mutex
	live    string
	writes  []string
	bodies  map[string][]map[string]any
	refuse  string // a resize answer to give instead of 200
	created bool
}

func (a *applyPlatform) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		if r.Method != http.MethodGet && r.URL.Path != "/ops/slice/price" {
			a.writes = append(a.writes, key)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.bodies[key] = append(a.bodies[key], body)
			switch r.URL.Path {
			case "/ops/slice/create":
				a.created = true
				_, _ = io.WriteString(w, `{}`)
			case "/ops/slice/resize":
				if a.refuse != "" {
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, a.refuse)
					return
				}
				_, _ = io.WriteString(w, `{}`)
			default:
				_, _ = io.WriteString(w, `{}`)
			}
			return
		}
		switch r.URL.Path {
		case "/ops/slice/get":
			if a.live == "" && !a.created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			live := a.live
			if live == "" {
				live = `{"name":"fxevm-testnode","tier":"custom","config":{}}`
			}
			_, _ = io.WriteString(w, live)
		case "/ops/slice/status":
			_, _ = io.WriteString(w, `{"ready":true,"components":[]}`)
		case "/ops/atomic/list":
			_, _ = io.WriteString(w, `[]`)
		case "/ops/canvas":
			_, _ = io.WriteString(w, `{"deployed":true,"sites":[{"slug":"default","route":"/"}]}`)
		case "/ops/backbone/secret/list":
			_, _ = io.WriteString(w, `[]`)
		case "/ops/atomic/auth":
			if r.URL.Query().Get("function") == "admin/reset" {
				_, _ = io.WriteString(w, `[{"method":"POST","path":"admin/reset","fingerprint":"...4321"}]`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		case "/ops/slice/auth":
			_, _ = io.WriteString(w, `{"enabled":true,"realm":"fxevm testnode","users":[{"name":"drift"}]}`)
		case "/ops/slice/price":
			var body struct {
				Config SliceConfig `json:"config"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			total := 0
			for _, f := range body.Config.Atomic.Functions {
				total += f.MemoryBytes
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"monthly_cents": total / bytesPerMiB * 3})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (a *applyPlatform) count(key string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, w := range a.writes {
		if w == key {
			n++
		}
	}
	return n
}

// applyFixture sets up the fxevm fixture with a fake platform answering
// live, and the deploy step replaced by a recorder. It returns the platform
// and how many times the deploy step ran.
func applyFixture(t *testing.T, live string, edit func(string) string) (*applyPlatform, *int) {
	t.Helper()
	p := &applyPlatform{live: live, bodies: map[string][]map[string]any{}}
	stubAPI(t, p.handler(t))
	withRealSchema(t)

	dir := t.TempDir()
	if err := copyTree(filepath.Join(mustAbs(t, "testdata"), "fxevm"), dir); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		path := filepath.Join(dir, "Driftfile")
		raw, _ := os.ReadFile(path)
		if err := os.WriteFile(path, []byte(edit(string(raw))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("RESET_KEY", "reset-key-4321")
	t.Setenv("EXPLORER_PASS", "explorer-pass")
	t.Setenv("DRIFT_SLICE", "")

	deployed := 0
	old := deployInto
	deployInto = func(m *Manifest, out io.Writer) error { deployed++; return nil }
	t.Cleanup(func() { deployInto = old })
	return p, &deployed
}

func runApplyTest(t *testing.T, a applyAnswers, planFile string) (string, error) {
	t.Helper()
	if a.removes == nil {
		a.removes = map[string]bool{}
	}
	if a.dataLoss == nil {
		a.dataLoss = map[string]bool{}
	}
	var err error
	out := captureStdout(t, func() {
		err = runApply(applyOptions{noEnvFile: true, answers: a, planFile: planFile, out: os.Stdout, in: strings.NewReader("")})
	})
	return out, err
}

func noAnswers() applyAnswers {
	return applyAnswers{price: -1, removes: map[string]bool{}, dataLoss: map[string]bool{}}
}

// The fixture's file against a slice that IS the file: nothing to agree to,
// nothing resized, and the deploy runs.
func matchingLive(t *testing.T) string {
	t.Helper()
	withRealSchema(t)
	c, err := CompileDriftfile(filepath.Join(mustAbs(t, "testdata"), "fxevm", "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	live, _ := json.Marshal(map[string]any{"name": "fxevm-testnode", "tier": "custom", "config": c.Shape})
	return string(live)
}

func TestApply_ASliceThatIsTheFileIsDeployedWithoutAResize(t *testing.T) {
	p, deployed := applyFixture(t, matchingLive(t), nil)
	if _, err := runApplyTest(t, noAnswers(), ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := p.count("POST /ops/slice/resize") + p.count("POST /ops/slice/create"); n != 0 {
		t.Errorf("a slice already the file's shape was created or resized (%d)", n)
	}
	if *deployed != 1 {
		t.Errorf("the deploy ran %d times, want 1", *deployed)
	}
}

func TestApply_APriceChangeStopsUntilTheQuotedFigureIsAgreed(t *testing.T) {
	double := func(s string) string {
		return strings.Replace(s, "concurrent = true\n    memory     = \"64MiB\"", "concurrent = true\n    memory     = \"128MiB\"", 1)
	}
	p, deployed := applyFixture(t, matchingLive(t), double)

	_, err := runApplyTest(t, noAnswers(), "")
	if err == nil || !strings.Contains(err.Error(), "--approve-price 768") {
		t.Fatalf("an unanswered price change was not refused with its flag: %v", err)
	}
	if p.count("POST /ops/slice/resize") != 0 || *deployed != 0 {
		t.Fatalf("something was sent before the price was agreed: %v, deployed %d", p.writes, *deployed)
	}

	a := noAnswers()
	a.price = 700
	if _, err := runApplyTest(t, a, ""); err == nil || !strings.Contains(err.Error(), "€7.68") {
		t.Fatalf("a figure other than the quote was accepted: %v", err)
	}
	if p.count("POST /ops/slice/resize") != 0 {
		t.Fatalf("a wrong figure reached the platform")
	}

	a.price = 768
	if _, err := runApplyTest(t, a, ""); err != nil {
		t.Fatalf("apply with the quoted figure: %v", err)
	}
	bodies := p.bodies["POST /ops/slice/resize"]
	if len(bodies) != 1 {
		t.Fatalf("resized %d times, want 1", len(bodies))
	}
	if got := bodies[0]["acknowledge_monthly_cents"]; got != float64(768) {
		t.Errorf("the resize sent acknowledge_monthly_cents %v, want the quoted 768", got)
	}
	if _, asked := bodies[0]["confirm_slice_name"]; asked {
		t.Errorf("a resize that removes nothing sent the removal confirmation")
	}
}

func TestApply_EachRemovalAndEachDataLossIsNamedBeforeAnythingIsSent(t *testing.T) {
	// The live testnode holds the free slice's placeholder storage, which the
	// file does not name: removals, and each a loss of what it holds.
	p, deployed := applyFixture(t, fxevmLive, nil)

	_, err := runApplyTest(t, noAnswers(), "")
	if err == nil {
		t.Fatal("removals and data losses went through unanswered")
	}
	for _, want := range []string{
		`--approve-remove "sql database-1"`, `--approve-remove "blobs bucket-01"`, `--approve-remove "nosql default"`,
		`--allow-data-loss "sql database-1"`, `--allow-data-loss "queue queue-1"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s:\n%v", want, err)
		}
	}
	if len(p.writes) != 0 || *deployed != 0 {
		t.Fatalf("something was sent before the removals were agreed: %v", p.writes)
	}

	// Agreeing to the removals is not agreeing to the data loss.
	a := noAnswers()
	for _, r := range []string{"sql database-1", "blobs bucket-01", "blobs bucket-02", "nosql default", "nosql collection-2", "queue queue-1", "slots", "pulse GET /bench"} {
		a.removes[r] = true
	}
	_, err = runApplyTest(t, a, "")
	if err == nil || !strings.Contains(err.Error(), "--allow-data-loss") || strings.Contains(err.Error(), "--approve-remove") {
		t.Fatalf("with every removal agreed, the data losses are still asked, and only they: %v", err)
	}
	if len(p.writes) != 0 {
		t.Fatalf("something was sent before the data loss was agreed: %v", p.writes)
	}
}

func TestApply_ASliceThatDoesNotExistIsCreatedWithTheFilesShape(t *testing.T) {
	p, deployed := applyFixture(t, "", nil)
	a := noAnswers()
	a.price = 576
	if _, err := runApplyTest(t, a, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	bodies := p.bodies["POST /ops/slice/create"]
	if len(bodies) != 1 {
		t.Fatalf("created %d times, want 1 (writes: %v)", len(bodies), p.writes)
	}
	cfg, _ := bodies[0]["config"].(map[string]any)
	atomic, _ := cfg["atomic"].(map[string]any)
	fns, _ := atomic["functions"].([]any)
	if bodies[0]["tier"] != "custom" || len(fns) != 4 {
		t.Errorf("the slice was not created with the file's shape: tier %v, %d slots", bodies[0]["tier"], len(fns))
	}
	if *deployed != 1 {
		t.Errorf("the deploy ran %d times after the create, want 1", *deployed)
	}
}

func TestApply_AResizeTheSliceRefusesIsReportedAndNothingIsDeployed(t *testing.T) {
	double := func(s string) string {
		return strings.Replace(s, "concurrent = true\n    memory     = \"64MiB\"", "concurrent = true\n    memory     = \"128MiB\"", 1)
	}
	p, deployed := applyFixture(t, matchingLive(t), double)
	p.refuse = `{"error":"price changed","price_change":{"current_monthly_cents":576,"new_monthly_cents":800}}`
	a := noAnswers()
	a.price = 768
	_, err := runApplyTest(t, a, "")
	if err == nil || !strings.Contains(err.Error(), "run `drift plan` again") {
		t.Fatalf("a refused resize was not reported as the slice having moved: %v", err)
	}
	if *deployed != 0 {
		t.Fatalf("the deploy ran after the resize was refused")
	}
}

func TestApply_APlanFileAppliesWhatItRecordsAndRefusesWhenTheFileMoved(t *testing.T) {
	double := func(s string) string {
		return strings.Replace(s, "concurrent = true\n    memory     = \"64MiB\"", "concurrent = true\n    memory     = \"128MiB\"", 1)
	}
	p, deployed := applyFixture(t, matchingLive(t), double)
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if _, err := planOutput(t, "--no-env-file", "--out", planFile); err != nil {
		t.Fatalf("plan --out: %v", err)
	}
	if _, err := runApplyTest(t, noAnswers(), planFile); err != nil {
		t.Fatalf("apply plan.json: %v", err)
	}
	if got := p.bodies["POST /ops/slice/resize"]; len(got) != 1 || got[0]["acknowledge_monthly_cents"] != float64(768) {
		t.Fatalf("the plan file's answer did not reach the resize: %v", got)
	}
	if *deployed != 1 {
		t.Fatalf("deployed %d times", *deployed)
	}

	// The same plan file, after the Driftfile changed: refused, nothing sent.
	raw, _ := os.ReadFile("Driftfile")
	if err := os.WriteFile("Driftfile", append(raw, []byte("\n# an edit\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(p.writes)
	_, err := runApplyTest(t, noAnswers(), planFile)
	if err == nil || !strings.Contains(err.Error(), "the Driftfile changed after the plan was made") {
		t.Fatalf("a plan file for an older Driftfile was applied: %v", err)
	}
	if !strings.Contains(err.Error(), "run `drift plan --out` again") {
		t.Errorf("the refusal does not name the flag drift plan accepts: %v", err)
	}
	if len(p.writes) != before {
		t.Fatalf("something was sent for a stale plan file: %v", p.writes[before:])
	}
}

// A YAML Driftfile is refused before anything reaches the slice.
func TestApply_AYAMLDriftfileIsRefused(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	withRealSchema(t)
	applyIn(t, "slice: demo\ncanvas:\n  sites:\n    - ./site\n", "site")
	_, err := runApplyTest(t, noAnswers(), "")
	if err == nil || !strings.Contains(err.Error(), "the Driftfile is HCL now") {
		t.Fatalf("a YAML Driftfile was not refused as YAML: %v", err)
	}
	for _, hit := range rec.hits {
		if strings.Contains(hit, " /ops/") {
			t.Errorf("a YAML Driftfile reached the slice: %s", hit)
		}
	}
}
