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

// buildWorker compiles the generated Worker entry point around one handler body,
// with this machine's own toolchain, and returns the binary.
//
// The template imports nothing but the standard library, so the build needs no
// module download: it is the template and the handler shape that are under
// test, not the SDK.
func buildWorker(t *testing.T, handler string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("this test compiles the Worker entry point and needs a Go toolchain on PATH: %v", err)
	}
	dir := t.TempDir()
	if err := generateMain(dir, goEntry{handler: "Sequencer", method: WorkerMethod}, goSDK{}); err != nil {
		t.Fatalf("generateMain: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module workertest\n\ngo 1.22\n"), 0o600); err != nil {
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
		t.Fatalf("the Worker entry point does not compile around a `func(ctx context.Context) error` "+
			"handler: %v\n%s\n--- main.go ---\n%s", err, out, src)
	}
	return filepath.Join(dir, "app")
}

// runWorker starts a built Worker, waits for the handler to say it is running,
// optionally sends SIGTERM, and returns the exit code and stderr.
func runWorker(t *testing.T, bin string, signal bool) (int, string) {
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
		t.Fatal("the Worker did not exit within ten seconds")
	}
	return -1, ""
}

const workerPrelude = "package main\n\nimport (\n\t\"context\"\n\t\"errors\"\n\t\"fmt\"\n)\n\nvar _, _ = errors.New, fmt.Sprint\n\n"

// THE CONTRACT the slice's supervisor relies on: SIGTERM cancels the context,
// and a handler that then returns nil, or the cancellation itself, exits 0. The
// supervisor reads 0 as a clean stop and anything else as a crash it records.
func TestWorkerEntryPoint_StopsCleanlyOnSIGTERM(t *testing.T) {
	for name, body := range map[string]string{
		"returns nil": "func Sequencer(ctx context.Context) error {\n" +
			"\tfmt.Println(\"running\")\n\t<-ctx.Done()\n\treturn nil\n}\n",
		"returns the cancellation": "func Sequencer(ctx context.Context) error {\n" +
			"\tfmt.Println(\"running\")\n\t<-ctx.Done()\n\treturn ctx.Err()\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			code, stderr := runWorker(t, buildWorker(t, workerPrelude+body), true)
			if code != 0 {
				t.Errorf("a handler that %s after SIGTERM exited %d, want 0; stderr:\n%s", name, code, stderr)
			}
		})
	}
}

// The other half: a failure is exit 1 with the reason on stderr, whether the
// handler returned an error or returned before anything asked it to stop. The
// stderr line is what the supervisor puts in the Worker's last-exit excerpt.
func TestWorkerEntryPoint_AFailureExitsOneWithTheReason(t *testing.T) {
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
			code, stderr := runWorker(t, buildWorker(t, workerPrelude+tc.body), name == "an error after SIGTERM")
			if code != 1 {
				t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "Sequencer") {
				t.Errorf("stderr must name the handler and say %q, got:\n%s", tc.want, stderr)
			}
		})
	}
}

// A Worker's entry point is its own, never a Function's: it reads no envelope, and
// a Function template would answer one request and exit into a crash loop.
func TestWorkerEntryPoint_IsNotAFunctionTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := generateMain(dir, goEntry{handler: "Sequencer", method: WorkerMethod}, goSDK{version: "v0.99.0", genmain: true}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"json.NewDecoder", "os.Stdin", "{{"} {
		if bytes.Contains(src, []byte(absent)) {
			t.Errorf("the Worker entry point contains %q:\n%s", absent, src)
		}
	}
	for _, present := range []string{"Sequencer(ctx)", "syscall.SIGTERM", "syscall.SIGINT"} {
		if !bytes.Contains(src, []byte(present)) {
			t.Errorf("the Worker entry point lacks %q:\n%s", present, src)
		}
	}
}

// A Worker carries no trigger, even when a schedule is registered under its key,
// and declares no invocation protocol: nothing calls it.
func TestWorker_HasNoTriggersAndNoProtocol(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"worker:sequencer": "*/1 * * * *"})

	f := BackendFunc{Spec: FunctionSpec{Name: WorkerKey("sequencer"), Handler: "Sequencer"}}
	if got := triggersFor(f); len(got) != 0 {
		t.Errorf("a Worker was given triggers %+v; the operator refuses a Worker carrying one", got)
	}
	if got := slotProtocol(WorkerMethod, "go"); got != "" {
		t.Errorf("a Worker declared protocol %q; it reads no envelopes", got)
	}
	if got := slotProtocol("get", "go"); got != "loop" {
		t.Errorf("a Go function must still declare loop, got %q", got)
	}
	if f.Spec.Trigger() != WorkerMethod || f.Spec.QueueSource() != "" {
		t.Errorf("Trigger() = %q, QueueSource() = %q for a Worker", f.Spec.Trigger(), f.Spec.QueueSource())
	}
}

// A Worker's declaration moves its digest exactly as a Function's does, so a changed
// Worker is redeployed rather than skipped as unchanged; and a Worker never shares
// a digest with a Function of the same name.
func TestDeclarationDigest_IncludesWorkers(t *testing.T) {
	base := FunctionSpec{
		Name: WorkerKey("sequencer"), Handler: "Sequencer", Backend: "default",
		Secrets: []string{"LEDGER_KEY"}, Env: map[string]string{"BATCH": "50"},
	}
	start := DeclarationDigest(base)
	for name, mutate := range map[string]func(*FunctionSpec){
		"the handler":  func(s *FunctionSpec) { s.Handler = "Drain" },
		"the backend":  func(s *FunctionSpec) { s.Backend = "workers" },
		"a secret":     func(s *FunctionSpec) { s.Secrets = []string{"OTHER"} },
		"an env value": func(s *FunctionSpec) { s.Env = map[string]string{"BATCH": "10"} },
		"the name":     func(s *FunctionSpec) { s.Name = WorkerKey("drainer") },
	} {
		spec := base
		mutate(&spec)
		if DeclarationDigest(spec) == start {
			t.Errorf("changing a Worker's %s did not move its digest; it would be skipped as unchanged", name)
		}
	}
	function := base
	function.Name = "get:sequencer"
	if DeclarationDigest(function) == start {
		t.Error("a Worker and a Function of the same name share a digest")
	}
}

// `drift file run` places a Worker like any native slot: its binary is `app` and
// its metadata says `method: worker`, which is what the runner's supervisor
// starts at boot.
func TestLocalSlotSink_PlacesAWorkerLikeANativeSlot(t *testing.T) {
	runner := t.TempDir()
	bin := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := localSlotSink(runner)(FuncArtifact{
		Name: "sequencer", Method: WorkerMethod, Language: "go", Backend: "default",
		Env: map[string]string{"BATCH": "50"}, SourcePath: bin,
	}); err != nil {
		t.Fatalf("localSlotSink: %v", err)
	}
	slot := filepath.Join(runner, slotDirName("default", WorkerMethod, "sequencer"))
	if info, err := os.Stat(filepath.Join(slot, "app")); err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("the Worker's binary is not an executable `app` in its slot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(slot, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["method"] != "worker" || meta["name"] != "sequencer" || meta["protocol"] != "" {
		t.Errorf("metadata.json = %s, want method worker, name sequencer, no protocol", raw)
	}
	if env, _ := meta["env"].(map[string]any); env["BATCH"] != "50" {
		t.Errorf("the Worker's env did not reach metadata.json: %s", raw)
	}
}

// What the operator is sent for a Worker: method `worker`, the Worker's name, Go,
// no triggers and no protocol, through the same sink a Function uses.
func TestOperatorSink_SendsAWorkerAsMethodWorker(t *testing.T) {
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
	spec := FunctionSpec{Name: WorkerKey("sequencer"), Handler: "Sequencer", Secrets: []string{"LEDGER_KEY"}}
	method, name := spec.Wire()
	if err := operatorSink(FuncArtifact{
		Name: name, Method: method, Language: "go", Backend: "default",
		Secrets: spec.Secrets, Triggers: triggersFor(BackendFunc{Spec: spec}),
		Digest: "d", SourcePath: bin,
	}); err != nil {
		t.Fatalf("operatorSink: %v", err)
	}

	for key, want := range map[string]any{"method": "worker", "name": "sequencer", "language": "go", "protocol": ""} {
		if meta[key] != want {
			t.Errorf("metadata %s = %v, want %v (all: %v)", key, meta[key], want, meta)
		}
	}
	if triggers, _ := meta["triggers"].([]any); len(triggers) != 0 {
		t.Errorf("a Worker was sent with triggers %v", meta["triggers"])
	}
}
