package atomic_cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ondrift/cloud/cli/common"
)

// buildField compiles the generated Field entry point around one handler body,
// with this machine's own toolchain, and returns the binary.
//
// The template imports nothing but the standard library, so the build needs no
// module download: it is the template and the handler shape that are under
// test, not the SDK.
func buildField(t *testing.T, handler string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("this test compiles the Field entry point and needs a Go toolchain on PATH: %v", err)
	}
	dir := t.TempDir()
	if err := generateMain(dir, goEntry{handler: "Sequencer", method: FieldMethod}, goSDK{}); err != nil {
		t.Fatalf("generateMain: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fieldtest\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sequencer.go"), []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "build", "-o", "app", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		src, _ := os.ReadFile(filepath.Join(dir, "main.go"))
		t.Fatalf("the Field entry point does not compile around a `func(ctx context.Context) error` "+
			"handler: %v\n%s\n--- main.go ---\n%s", err, out, src)
	}
	return filepath.Join(dir, "app")
}

// runField starts a built Field, waits for the handler to say it is running,
// optionally sends SIGTERM, and returns the exit code and stderr.
func runField(t *testing.T, bin string, signal bool) (int, string) {
	t.Helper()
	cmd := exec.Command(bin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "running" {
				close(ready)
			}
		}
	}()
	if signal {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("the handler never reported running")
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), stderr.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, stderr.String()
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the Field did not exit within ten seconds")
	}
	return -1, ""
}

const fieldPrelude = "package main\n\nimport (\n\t\"context\"\n\t\"errors\"\n\t\"fmt\"\n)\n\nvar _, _ = errors.New, fmt.Sprint\n\n"

// THE CONTRACT the slice's supervisor relies on: SIGTERM cancels the context,
// and a handler that then returns nil, or the cancellation itself, exits 0. The
// supervisor reads 0 as a clean stop and anything else as a crash it records.
func TestFieldEntryPoint_StopsCleanlyOnSIGTERM(t *testing.T) {
	for name, body := range map[string]string{
		"returns nil": "func Sequencer(ctx context.Context) error {\n" +
			"\tfmt.Println(\"running\")\n\t<-ctx.Done()\n\treturn nil\n}\n",
		"returns the cancellation": "func Sequencer(ctx context.Context) error {\n" +
			"\tfmt.Println(\"running\")\n\t<-ctx.Done()\n\treturn ctx.Err()\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := runField(t, buildField(t, fieldPrelude+body), true)
			if code != 0 {
				t.Errorf("a handler that %s after SIGTERM exited %d, want 0; stderr:\n%s", name, code, stderr)
			}
		})
	}
}

// The other half: a failure is exit 1 with the reason on stderr, whether the
// handler returned an error or returned before anything asked it to stop. The
// stderr line is what the supervisor puts in the Field's last-exit excerpt.
func TestFieldEntryPoint_AFailureExitsOneWithTheReason(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"an error": {
			body: "func Sequencer(ctx context.Context) error {\n\treturn errors.New(\"ledger unreachable\")\n}\n",
			want: "ledger unreachable",
		},
		"nil without being asked": {
			body: "func Sequencer(ctx context.Context) error {\n\treturn nil\n}\n",
			want: "returned before it was asked to stop",
		},
		"an error after SIGTERM": {
			body: "func Sequencer(ctx context.Context) error {\n\tfmt.Println(\"running\")\n" +
				"\t<-ctx.Done()\n\treturn errors.New(\"flush failed\")\n}\n",
			want: "flush failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := runField(t, buildField(t, fieldPrelude+tc.body), name == "an error after SIGTERM")
			if code != 1 {
				t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "Sequencer") {
				t.Errorf("stderr must name the handler and say %q, got:\n%s", tc.want, stderr)
			}
		})
	}
}

// A Field's entry point is its own, never a Pulse's: it reads no envelope, and
// a Pulse template would answer one request and exit into a crash loop.
func TestFieldEntryPoint_IsNotAPulseTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := generateMain(dir, goEntry{handler: "Sequencer", method: FieldMethod}, goSDK{version: "v0.99.0", genmain: true}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"json.NewDecoder", "os.Stdin", "{{"} {
		if bytes.Contains(src, []byte(absent)) {
			t.Errorf("the Field entry point contains %q:\n%s", absent, src)
		}
	}
	for _, present := range []string{"Sequencer(ctx)", "syscall.SIGTERM", "syscall.SIGINT"} {
		if !bytes.Contains(src, []byte(present)) {
			t.Errorf("the Field entry point lacks %q:\n%s", present, src)
		}
	}
}

// A Field carries no trigger, even when a schedule is registered under its key,
// and declares no invocation protocol: nothing calls it.
func TestField_HasNoTriggersAndNoProtocol(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"field:sequencer": "*/1 * * * *"})

	f := ElementFunc{Spec: FunctionSpec{Name: FieldKey("sequencer"), Handler: "Sequencer"}}
	if got := triggersFor(f); len(got) != 0 {
		t.Errorf("a Field was given triggers %+v; the operator refuses a Field carrying one", got)
	}
	if got := slotProtocol(FieldMethod, "go"); got != "" {
		t.Errorf("a Field declared protocol %q; it reads no envelopes", got)
	}
	if got := slotProtocol("get", "go"); got != "loop" {
		t.Errorf("a Go pulse must still declare loop, got %q", got)
	}
	if f.Spec.Trigger() != FieldMethod || f.Spec.QueueSource() != "" {
		t.Errorf("Trigger() = %q, QueueSource() = %q for a Field", f.Spec.Trigger(), f.Spec.QueueSource())
	}
}

// A Field's declaration moves its digest exactly as a Pulse's does, so a changed
// Field is redeployed rather than skipped as unchanged; and a Field never shares
// a digest with a Pulse of the same name.
func TestDeclarationDigest_IncludesFields(t *testing.T) {
	base := FunctionSpec{
		Name: FieldKey("sequencer"), Handler: "Sequencer", Element: "default",
		Secrets: []string{"LEDGER_KEY"}, Env: map[string]string{"BATCH": "50"},
	}
	start := DeclarationDigest(base)
	for name, mutate := range map[string]func(*FunctionSpec){
		"the handler":  func(s *FunctionSpec) { s.Handler = "Drain" },
		"the element":  func(s *FunctionSpec) { s.Element = "workers" },
		"a secret":     func(s *FunctionSpec) { s.Secrets = []string{"OTHER"} },
		"an env value": func(s *FunctionSpec) { s.Env = map[string]string{"BATCH": "10"} },
		"the name":     func(s *FunctionSpec) { s.Name = FieldKey("drainer") },
	} {
		spec := base
		mutate(&spec)
		if DeclarationDigest(spec) == start {
			t.Errorf("changing a Field's %s did not move its digest; it would be skipped as unchanged", name)
		}
	}
	pulse := base
	pulse.Name = "get:sequencer"
	if DeclarationDigest(pulse) == start {
		t.Error("a Field and a Pulse of the same name share a digest")
	}
}

// `drift file run` places a Field like any native slot: its binary is `app` and
// its metadata says `method: field`, which is what the runner's supervisor
// starts at boot.
func TestLocalSlotSink_PlacesAFieldLikeANativeSlot(t *testing.T) {
	runner := t.TempDir()
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := localSlotSink(runner)(FuncArtifact{
		Name: "sequencer", Method: FieldMethod, Language: "go", Element: "default",
		Env: map[string]string{"BATCH": "50"}, SourcePath: bin,
	}); err != nil {
		t.Fatalf("localSlotSink: %v", err)
	}
	slot := filepath.Join(runner, slotDirName("default", FieldMethod, "sequencer"))
	if info, err := os.Stat(filepath.Join(slot, "app")); err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("the Field's binary is not an executable `app` in its slot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(slot, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["method"] != "field" || meta["name"] != "sequencer" || meta["protocol"] != "" {
		t.Errorf("metadata.json = %s, want method field, name sequencer, no protocol", raw)
	}
	if env, _ := meta["env"].(map[string]any); env["BATCH"] != "50" {
		t.Errorf("the Field's env did not reach metadata.json: %s", raw)
	}
}

// What the operator is sent for a Field: method `field`, the Field's name, Go,
// no triggers and no protocol, through the same sink a Pulse uses.
func TestOperatorSink_SendsAFieldAsMethodField(t *testing.T) {
	var meta map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ops/atomic" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
		}
		_ = json.Unmarshal([]byte(r.FormValue("metadata")), &meta)
		file, _, err := r.FormFile("source")
		if err != nil {
			t.Errorf("no source part: %v", err)
		} else {
			body, _ := io.ReadAll(file)
			if string(body) != "binary" {
				t.Errorf("the source part carried %q", body)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	if err := common.SaveSession("test-token", "test-refresh"); err != nil {
		t.Fatal(err)
	}
	prev := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = prev })

	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := FunctionSpec{Name: FieldKey("sequencer"), Handler: "Sequencer", Secrets: []string{"LEDGER_KEY"}}
	method, name := spec.Wire()
	if err := operatorSink(FuncArtifact{
		Name: name, Method: method, Language: "go", Element: "default",
		Secrets: spec.Secrets, Triggers: triggersFor(ElementFunc{Spec: spec}),
		Digest: "d", SourcePath: bin,
	}); err != nil {
		t.Fatalf("operatorSink: %v", err)
	}

	for key, want := range map[string]any{"method": "field", "name": "sequencer", "language": "go", "protocol": ""} {
		if meta[key] != want {
			t.Errorf("metadata %s = %v, want %v (all: %v)", key, meta[key], want, meta)
		}
	}
	if triggers, _ := meta["triggers"].([]any); len(triggers) != 0 {
		t.Errorf("a Field was sent with triggers %v", meta["triggers"])
	}
}
