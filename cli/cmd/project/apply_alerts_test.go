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

// alertPlatform answers the reads an apply makes and records each alert write.
type alertPlatform struct {
	mu      sync.Mutex
	live    string
	posted  []map[string]any
	deleted []string
	base    http.HandlerFunc
}

func (a *alertPlatform) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ops/atomic/alert" {
		a.mu.Lock()
		defer a.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(a.live))
		case http.MethodPost:
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			a.posted = append(a.posted, body)
			_, _ = w.Write([]byte(`{}`))
		case http.MethodDelete:
			a.deleted = append(a.deleted, r.URL.Query().Get("name"))
			_, _ = w.Write([]byte(`{}`))
		}
		return
	}
	a.base(w, r)
}

// alertProject lays out a slice with one GET pulse declaring the alert blocks
// given, and the stub platform holding `live` alerts.
func alertProject(t *testing.T, blocks, live string) *alertPlatform {
	t.Helper()
	p := &alertPlatform{live: live, base: idleSliceStub("custom", []string{"get:ping"})}
	stubAPI(t, p.handler)
	withRealSchema(t)
	applyIn(t, `slice "demo" {}

atomic {
  element "default" {
    pulse "GET /ping" {
      handler = "GetPing"
      memory  = "16MiB"
`+blocks+`
    }
  }
}
`, "atomic")
	src := "function GetPing(req) { return [200, 'OK', {}]; }\nmodule.exports = { GetPing };\n"
	if err := os.WriteFile(filepath.Join(".", "atomic", "handlers.js"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const checkoutAlert = `      alert "ping-errors" {
        threshold = 3
        window    = "10m"
        webhook   = env("PING_HOOK")
      }`

// An alert the file declares is created by apply, with the webhook read from the
// environment, and the plan names it without printing the URL.
func TestApply_CreatesADeclaredAlert(t *testing.T) {
	t.Setenv("PING_HOOK", "https://hooks.example.com/secret-path-123")
	p := alertProject(t, checkoutAlert, `[]`)

	var err error
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(p.posted) != 1 {
		t.Fatalf("apply set %d alerts, want 1: %v", len(p.posted), p.posted)
	}
	got := p.posted[0]
	want := map[string]any{
		"name": "ping-errors", "function": "ping", "trigger": "errors", "threshold": float64(3),
		"window_seconds": float64(600), "notify_type": "webhook", "notify_target": "https://hooks.example.com/secret-path-123",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("the alert sent %s = %v, want %v", k, got[k], v)
		}
	}
	if strings.Contains(out, "secret-path-123") {
		t.Errorf("the webhook URL reached the output:\n%s", out)
	}
	if !strings.Contains(out, "alert ping-errors") {
		t.Errorf("the apply never says it set the alert:\n%s", out)
	}
}

// An alert the slice already holds as declared is left alone: apply sets only
// what differs, and a change is one POST under the same name.
func TestApply_LeavesAnUnchangedAlertAloneAndChangesAChangedOne(t *testing.T) {
	t.Setenv("PING_HOOK", "https://hooks.example.com/a")
	same := `[{"name":"ping-errors","function":"ping","trigger":"errors","threshold":3,"window_seconds":600,"notify_type":"webhook","notify_target":"https://hooks.example.com/a"}]`
	p := alertProject(t, checkoutAlert, same)
	var err error
	if out := captureStdout(t, func() { err = applyAgreeingAtZero(t) }); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(p.posted) != 0 || len(p.deleted) != 0 {
		t.Errorf("an unchanged alert was written: posted %v, deleted %v", p.posted, p.deleted)
	}

	t.Setenv("PING_HOOK", "https://hooks.example.com/b")
	p2 := alertProject(t, checkoutAlert, same)
	out := captureStdout(t, func() { err = applyAgreeingAtZero(t) })
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if len(p2.posted) != 1 || p2.posted[0]["notify_target"] != "https://hooks.example.com/b" {
		t.Errorf("a changed webhook was not set: %v", p2.posted)
	}
}

// An alert the file does not name is a removal, asked about like every other,
// and deleted once it is agreed to.
func TestApply_AnUndeclaredAlertIsARemovalItAsksAbout(t *testing.T) {
	stale := `[{"name":"old-alert","function":"ping","trigger":"errors","threshold":1,"window_seconds":300,"notify_type":"webhook","notify_target":"https://x.example.com/h"}]`
	p := alertProject(t, "", stale)

	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), `--approve-remove "alert old-alert"`) {
		t.Fatalf("an undeclared alert was not put to the person: %v", err)
	}
	if len(p.deleted) != 0 {
		t.Fatalf("the alert was deleted before it was agreed to: %v", p.deleted)
	}

	a := noAnswers()
	a.price = 0
	a.removes["alert old-alert"] = true
	if err := runApply(applyOptions{noEnvFile: true, answers: a, out: io.Discard, in: strings.NewReader("")}); err != nil {
		t.Fatalf("apply with the removal agreed: %v", err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "old-alert" {
		t.Errorf("deleted %v, want old-alert", p.deleted)
	}
}

// A webhook the environment does not hold is refused before anything is sent.
func TestApply_AnAlertWhoseWebhookIsNotSetIsRefused(t *testing.T) {
	t.Setenv("PING_HOOK", "placeholder")
	if err := os.Unsetenv("PING_HOOK"); err != nil {
		t.Fatal(err)
	}
	p := alertProject(t, checkoutAlert, `[]`)
	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), "PING_HOOK") {
		t.Fatalf("an unset webhook was not refused by name: %v", err)
	}
	if len(p.posted) != 0 {
		t.Errorf("an alert was written without a webhook: %v", p.posted)
	}
}

// A value that is not an http(s) URL cannot be a webhook, and is refused before
// the slice has to say so.
func TestApply_AnAlertWhoseWebhookIsNotAURLIsRefused(t *testing.T) {
	t.Setenv("PING_HOOK", "not a url")
	p := alertProject(t, checkoutAlert, `[]`)
	err := applyAgreeingAtZero(t)
	if err == nil || !strings.Contains(err.Error(), "not an http(s) URL") {
		t.Fatalf("a webhook that is no URL was not refused: %v", err)
	}
	if strings.Contains(err.Error(), "not a url") {
		t.Errorf("the refusal prints the value: %v", err)
	}
	if len(p.posted) != 0 {
		t.Errorf("an alert was written: %v", p.posted)
	}
}

// The webhook is a credential: a literal in the file is refused, as every other
// write-only value is.
func TestAnAlertWebhookMustBeAReference(t *testing.T) {
	withRealSchema(t)
	one := func(name string, lines ...string) string {
		return "      alert \"" + name + "\" {\n        " + strings.Join(lines, "\n        ") + "\n      }"
	}
	for _, tc := range []struct{ blocks, want string }{
		{one("a", `webhook = "https://hooks.example.com/x"`), "write-only"},
		{one("a", `threshold = 1`), "Missing webhook"},
		{one("A", `webhook = env("H")`), "Not an alert name"},
		{one("a", `webhook = env("H")`, `on = "latency"`), "Not a condition"},
		{one("a", `webhook = env("H")`, `threshold = 0`), "Out of range"},
		{one("a", `webhook = env("H")`, `window = "30s"`), "at least 1m"},
		{one("a", `webhook = env("H")`) + "\n" + one("a", `webhook = env("H")`), "Duplicate alert"},
	} {
		_, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {}\natomic {\n  element \"default\" {\n    pulse \"GET /ping\" {\n      handler = \"Get\"\n      memory = \"16MiB\"\n"+
			tc.blocks+"\n    }\n  }\n}\n", "atomic/"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v; want it to say %q", tc.blocks, err, tc.want)
		}
	}
}
