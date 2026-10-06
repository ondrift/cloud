package slice

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// The operator's 409 bodies, byte for byte as applyResize encodes them (a map,
// so the keys come out sorted). The slice is `lab`.
const (
	// Takes a deployed slot away. confirm_slice_name is the slice's NAME.
	removalRefusal = `{"confirmation_required":{"confirm_slice_name":"lab","destroys":[` +
		`{"resource":"atomic.functions.get:bench","current":"serving from slot atomic-2","wanted":"removed",` +
		`"destroys":"the route stops answering, and its code, triggers, deploy history and stored artifacts are deleted"}]},` +
		`"error":"this resize takes away atomic.functions.get:bench; confirm by naming the slice, with confirm_slice_name set to \"lab\"",` +
		`"violations":[{"resource":"atomic.functions.get:bench","used":1,"new_limit":0}]}`

	// Moves the price AND takes the slot away: both questions in one refusal.
	bothRefusal = `{"confirmation_required":{"confirm_slice_name":"lab","destroys":[` +
		`{"resource":"atomic.functions.get:bench","current":"serving from slot atomic-2","wanted":"removed",` +
		`"destroys":"the route stops answering, and its code, triggers, deploy history and stored artifacts are deleted"}]},` +
		`"error":"this resize changes what the slice costs, from EUR 12.00 to EUR 9.00 a month; re-send it with ` +
		`acknowledge_monthly_cents set to 900; and this resize takes away atomic.functions.get:bench; confirm by naming ` +
		`the slice, with confirm_slice_name set to \"lab\"",` +
		`"price_change":{"acknowledge_field":"acknowledge_monthly_cents","current_monthly_cents":1200,"new_monthly_cents":900},` +
		`"violations":[{"resource":"atomic.functions.get:bench","used":1,"new_limit":0}]}`
)

// resizeAPI answers every resize with a 409 carrying body, and records each
// request body it was sent.
func resizeAPI(t *testing.T, body string) *[]string {
	t.Helper()
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = append(sent, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatal(err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })
	return &sent
}

// shapeFile writes a minimal shape for --config.
func shapeFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shape.json")
	if err := os.WriteFile(p, []byte(`{"atomic":{"MaxNumberOfFunctions":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The removal question reached the CLI as the slice's NAME, a string, and the
// CLI decoded it as a bool. The whole reply then failed to decode, so every
// removal fell to "resize refused: ..." and neither the form nor --config could
// ever answer it.
func TestResizeFile_ARemovalAsksForTheSliceName(t *testing.T) {
	resizeAPI(t, removalRefusal)
	err := resizeFromFile("lab", shapeFile(t), 1, -1, "")
	if err == nil {
		t.Fatal("a refused removal was reported as applied")
	}
	for _, want := range []string{
		"--confirm lab",
		"atomic.functions.get:bench: serving from slot atomic-2 → removed",
		"the route stops answering",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, err)
		}
	}
}

// The server asks both questions in ONE refusal, and a caller answering by
// flags must learn both on the first run, not one now and the other next time.
func TestResizeFile_ReportsBothQuestionsFromOneRefusal(t *testing.T) {
	resizeAPI(t, bothRefusal)
	err := resizeFromFile("lab", shapeFile(t), 1, -1, "")
	if err == nil {
		t.Fatal("a refused resize was reported as applied")
	}
	for _, want := range []string{
		"EUR 12.00", "EUR 9.00",
		"atomic.functions.get:bench",
		"--acknowledge-monthly-cents 900 --confirm lab",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q, got:\n%v", want, err)
		}
	}
}

// The form answers both questions from one refusal, so its next press sends
// both answers, and lists what goes before it is agreed to.
func TestAnswerRefusal_AnswersBothQuestions(t *testing.T) {
	sent := resizeAPI(t, bothRefusal)
	ok, refusal, err := postResize(map[string]any{"name": "lab", "config": map[string]any{}})
	if ok || err != nil || refusal == nil {
		t.Fatalf("postResize = (%v, %v, %v), want the refusal decoded", ok, refusal, err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d requests", len(*sent))
	}

	var listed bytes.Buffer
	a := answerRefusal(refusal, &listed)
	if a.ack == nil || *a.ack != 900 {
		t.Errorf("the next press acknowledges %v, want 900", a.ack)
	}
	if a.confirm != "lab" {
		t.Errorf("the next press confirms %q, want the slice's name", a.confirm)
	}
	if !strings.Contains(listed.String(), "the route stops answering") {
		t.Errorf("what goes was not listed before it is agreed to:\n%s", listed.String())
	}
	if !strings.Contains(a.label, "EUR 9.00") || !strings.Contains(a.label, "confirm") {
		t.Errorf("the Apply button reads %q, which does not say it agrees to both", a.label)
	}
}
