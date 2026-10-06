package atomic_cmd

// mux_template_test.go: the mains this package generates for a Go slot, held
// to the SDK in this repository.
//
// Every Go build resolves the SDK at @latest, so a template that stops
// compiling against the SDK breaks every Go deploy at the tenant's build step,
// in a file they never wrote. Here every template generateMain can render is
// rendered through generateMain itself, around a handler of each shape, and
// compiled against ../../../../../sdk (cli/ and sdk/ are one repository); the
// built mains are then run as the slice runs them.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// sdkRoot is the SDK module in this repository.
var sdkRoot = filepath.Join("..", "..", "..", "..", "..", "sdk")

// withGenmain is an SDK that carries genmain, as every SDK built from this
// repository does.
var withGenmain = goSDK{version: "v0.99.0", genmain: true}

// --- which template ----------------------------------------------------------

// Every buffered Pulse is served through genmain, at any concurrency and with
// or without a context; a stream keeps the native template, whose stdout is the
// stream; and an SDK that predates genmain gets the native template for what it
// can serve and a refusal, before anything compiles, for what it cannot.
func TestPulseMainTemplate_ChoosesByStreamSDKAndShape(t *testing.T) {
	old := goSDK{version: "v0.9.0"}
	for name, tc := range map[string]struct {
		e        goEntry
		takesCtx bool
		sdk      goSDK
		want     string // the template, or "" for a refusal
		refusal  string
	}{
		"post at 1":                {goEntry{method: "post"}, false, withGenmain, defaultMuxServerPost, ""},
		"post at 16":               {goEntry{method: "post", concurrency: 16}, false, withGenmain, defaultMuxServerPost, ""},
		"post with ctx at 1":       {goEntry{method: "post"}, true, withGenmain, defaultMuxServerPost, ""},
		"get with ctx at 16":       {goEntry{method: "get", concurrency: 16}, true, withGenmain, defaultMuxServerGet, ""},
		"queue":                    {goEntry{method: QueueMethod}, false, withGenmain, defaultMuxServerPost, ""},
		"a stream":                 {goEntry{method: "get", stream: "sse"}, false, withGenmain, defaultNativeServerGet, ""},
		"a stream with ctx":        {goEntry{name: "get:events", method: "get", stream: "sse"}, true, withGenmain, "", "streams its reply"},
		"old SDK at 1":             {goEntry{method: "post"}, false, old, defaultNativeServerPost, ""},
		"old SDK, get":             {goEntry{method: "get"}, false, old, defaultNativeServerGet, ""},
		"old SDK at 16":            {goEntry{name: "post:rpc", method: "post", concurrency: 16}, false, old, "", "(v0.9.0) predates concurrent Pulses"},
		"old SDK with ctx":         {goEntry{name: "post:rpc", method: "post"}, true, old, "", "(v0.9.0) predates handlers"},
		"no SDK resolved, ctx, 16": {goEntry{name: "post:rpc", method: "post", concurrency: 16}, true, goSDK{}, "", "this build resolved predates"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := pulseMainTemplate(tc.e, tc.takesCtx, tc.sdk)
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("want a refusal saying %q, got %v", tc.refusal, err)
				}
				if tc.e.name != "" && !strings.Contains(err.Error(), tc.e.name) {
					t.Errorf("the refusal does not name the pulse: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got != tc.want {
				t.Errorf("the wrong template:\n%.200s", got)
			}
		})
	}
}

// The SDK compiles its own copies of the templates this CLI ships
// (genmain/testdata/templates/next/), and that is the compatibility genmain
// owes. Those copies are only a guarantee while they are these bytes.
func TestMuxTemplatesAreTheSDKsCopies(t *testing.T) {
	for file, embedded := range map[string]string{
		"server_post_mux.txt": defaultMuxServerPost,
		"server_get_mux.txt":  defaultMuxServerGet,
	} {
		sdkCopy, err := os.ReadFile(filepath.Join(sdkRoot, "go", "genmain", "testdata", "templates", "next", file))
		if err != nil {
			t.Fatalf("the SDK's copy of %s: %v", file, err)
		}
		if string(sdkCopy) != embedded {
			t.Errorf("default/%s differs from the SDK's genmain/testdata/templates/next/%s: the SDK compiles "+
				"its copy against genmain, so a difference is a template nothing checks", file, file)
		}
	}
}

// What the staged go.mod requires, and whether that version in the module
// cache carries genmain.
func TestResolvedGoSDK_ReadsTheRequirementAndTheCache(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "github.com", "ondrift", "cloud", "sdk@v0.11.0", "go", "genmain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "github.com", "ondrift", "cloud", "sdk@v0.10.0", "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		gomod string
		want  goSDK
	}{
		"a block, with genmain": {"module x\n\ngo 1.26\n\nrequire (\n\tgithub.com/ondrift/cloud/sdk v0.11.0\n\tgolang.org/x/sys v0.1.0 // indirect\n)\n",
			goSDK{version: "v0.11.0", genmain: true}},
		"one line, without genmain": {"module x\n\nrequire github.com/ondrift/cloud/sdk v0.10.0\n", goSDK{version: "v0.10.0"}},
		"not in the cache":          {"module x\n\nrequire github.com/ondrift/cloud/sdk v0.12.0 // indirect\n", goSDK{version: "v0.12.0"}},
		"no SDK at all":             {"module x\n\nrequire github.com/ondrift/cloud/sdk/go v0.0.0-2024\n", goSDK{}},
	} {
		t.Run(name, func(t *testing.T) {
			stage := t.TempDir()
			if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte(tc.gomod), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := resolvedGoSDK(stage, cache); got != tc.want {
				t.Errorf("resolvedGoSDK = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// --- every template, compiled and run ----------------------------------------

// probeHandlers is the user code every rendered main wraps: a handler of each
// shape, and a Field. What a call does is chosen by `op`, from the body for a
// POST and the query string for a GET.
const probeHandlers = `package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

type ProbeBody struct {
	Op string ` + "`json:\"op\"`" + `
	N  int    ` + "`json:\"n\"`" + `
}

func PostProbe(body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	return probe(context.Background(), body, req)
}

func PostProbeCtx(ctx context.Context, body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	return probe(ctx, body, req)
}

func GetProbe(req drift.Request) (int, string, any, map[string]string) {
	return probe(context.Background(), fromQuery(req.Query), req)
}

func GetProbeCtx(ctx context.Context, req drift.Request) (int, string, any, map[string]string) {
	return probe(ctx, fromQuery(req.Query), req)
}

func Sequencer(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func fromQuery(q string) ProbeBody {
	v, _ := url.ParseQuery(q)
	var n int
	_, _ = fmt.Sscan(v.Get("n"), &n)
	return ProbeBody{Op: v.Get("op"), N: n}
}

// The barrier: a call waits until n calls are inside it at once. Calls served
// one at a time never get there.
var (
	mu      sync.Mutex
	inside  int
	reached = make(chan struct{})
	opened  bool
)

func barrier(n int) bool {
	mu.Lock()
	inside++
	if inside >= n && !opened {
		opened = true
		close(reached)
	}
	mu.Unlock()
	select {
	case <-reached:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func probe(ctx context.Context, body ProbeBody, req drift.Request) (int, string, any, map[string]string) {
	switch body.Op {
	case "print":
		fmt.Println("printed by the handler")
	case "barrier":
		if !barrier(body.N) {
			return 504, "Gateway Timeout", map[string]string{"err": "the barrier was never reached"}, nil
		}
	case "wait":
		select {
		case <-ctx.Done():
			return 504, "Gateway Timeout", map[string]string{"err": ctx.Err().Error()}, nil
		case <-time.After(time.Duration(body.N) * time.Millisecond):
		}
	}
	return 200, "OK", map[string]any{"path": req.Path, "pid": os.Getpid()}, nil
}
`

// renderedMain is one main generateMain renders: the entry it is rendered for
// and the SDK it believes it resolved.
type renderedMain struct {
	name string
	e    goEntry
	sdk  goSDK
}

// everyRenderedMain is every template generateMain can render, for every
// handler shape it can call.
var everyRenderedMain = []renderedMain{
	{"mux-post", goEntry{handler: "PostProbe", method: "post"}, withGenmain},
	{"mux-post-ctx", goEntry{handler: "PostProbeCtx", method: "post", concurrency: 16}, withGenmain},
	{"mux-get", goEntry{handler: "GetProbe", method: "get"}, withGenmain},
	{"mux-get-ctx", goEntry{handler: "GetProbeCtx", method: "get", concurrency: 16}, withGenmain},
	{"native-post", goEntry{handler: "PostProbe", method: "post"}, goSDK{}},
	{"native-get", goEntry{handler: "GetProbe", method: "get"}, goSDK{}},
	{"native-stream", goEntry{handler: "GetProbe", method: "get", stream: "sse"}, withGenmain},
	{"field", goEntry{handler: "Sequencer", method: FieldMethod}, withGenmain},
}

var rendered struct {
	once sync.Once
	bin  string
	out  string
	err  error
}

// buildRenderedMains renders every main into one module that requires this
// repository's SDK, builds them all in one go build, and returns the directory
// holding the binaries. Built once per test binary.
func buildRenderedMains(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("this test compiles the generated mains and needs a Go toolchain on PATH: %v", err)
	}
	rendered.once.Do(func() {
		rendered.err = func() error {
			root, err := os.MkdirTemp("", "drift-rendered-mains-")
			if err != nil {
				return err
			}
			sdk, err := filepath.Abs(sdkRoot)
			if err != nil {
				return err
			}
			gomod := "module probes\n\ngo 1.26\n\nrequire github.com/ondrift/cloud/sdk v0.0.0\n\nreplace github.com/ondrift/cloud/sdk => " + sdk + "\n"
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600); err != nil {
				return err
			}
			for _, m := range everyRenderedMain {
				dir := filepath.Join(root, "cmd", m.name)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(probeHandlers), 0o600); err != nil {
					return err
				}
				if err := generateMain(dir, m.e, m.sdk); err != nil {
					return fmt.Errorf("%s: %w", m.name, err)
				}
			}
			rendered.bin = filepath.Join(root, "bin")
			cmd := exec.Command(goBin, "build", "-o", rendered.bin+string(filepath.Separator), "./...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off", "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			rendered.out = string(out)
			return err
		}()
	})
	if rendered.err != nil {
		t.Fatalf("building the rendered mains: %v\n%s", rendered.err, rendered.out)
	}
	return rendered.bin
}

func TestMain(m *testing.M) {
	code := m.Run()
	if rendered.bin != "" {
		_ = os.RemoveAll(filepath.Dir(rendered.bin))
	}
	os.Exit(code)
}

// Every main generateMain can render compiles against the SDK in this
// repository: genmain's signature, the drift package's names, and each handler
// shape the templates call.
func TestEveryRenderedMainCompilesAgainstTheSDK(t *testing.T) {
	bin := buildRenderedMains(t)
	for _, m := range everyRenderedMain {
		if _, err := os.Stat(filepath.Join(bin, m.name)); err != nil {
			t.Errorf("%s: not built: %v", m.name, err)
		}
	}
}

// slice is a stand-in for the slice's side of one worker: the process, its
// stdin, and its stdout read line by line.
type slice struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr *bytes.Buffer
}

func spawn(t *testing.T, name string, env ...string) *slice {
	t.Helper()
	cmd := exec.Command(filepath.Join(buildRenderedMains(t), name))
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "DRIFT_RUNTIME=1"}, env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &slice{cmd: cmd, stdin: stdin, lines: make(chan string, 64), stderr: &bytes.Buffer{}}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	return s
}

func (s *slice) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		t.Fatalf("writing to the worker: %v", err)
	}
}

func (s *slice) read(t *testing.T) string {
	t.Helper()
	select {
	case l, ok := <-s.lines:
		if !ok {
			t.Fatalf("the worker closed stdout; stderr:\n%s", s.stderr.String())
		}
		return l
	case <-time.After(15 * time.Second):
		t.Fatalf("no line from the worker in 15s; stderr:\n%s", s.stderr.String())
	}
	return ""
}

// close ends stdin, which retires the worker, and waits for it to exit 0.
func (s *slice) close(t *testing.T) {
	t.Helper()
	_ = s.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("the worker exited %d after stdin closed; stderr:\n%s", exit.ExitCode(), s.stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not exit within 10s of stdin closing")
	}
}

// THE CONCURRENT PULSE, end to end through the main this CLI generates for
// `concurrency: 16` and a context-first handler: a hello first, then sixteen
// request frames whose handler returns only once all sixteen are inside it at
// once (calls served one at a time never get there), answered by sixteen
// response frames matched by id; then a seventeenth call that waits on its
// context, ended by a cancel frame, which proves the handler was given the
// call's context.
func TestTheGeneratedMuxMainServesCallsConcurrently(t *testing.T) {
	const n = 16
	w := spawn(t, "mux-post-ctx", "DRIFT_INVOKE=mux1", fmt.Sprintf("DRIFT_MUX_SLOTS=%d", n+1))

	var hello struct {
		Hello struct {
			Proto string `json:"proto"`
			V     int    `json:"v"`
			Slots int    `json:"slots"`
		} `json:"hello"`
	}
	if first := w.read(t); json.Unmarshal([]byte(first), &hello) != nil || hello.Hello.Proto != "drift-mux" ||
		hello.Hello.V != 1 || hello.Hello.Slots != n+1 {
		t.Fatalf("the first frame is not the hello for %d slots: %s", n+1, first)
	}

	for i := 1; i <= n; i++ {
		w.send(t, fmt.Sprintf(`{"id":%d,"deadline_ms":20000,"req":{"path":"/c%d","headers":{},"query":"","body":{"op":"barrier","n":%d}}}`, i, i, n))
	}
	var ids []int
	pids := map[float64]bool{}
	for range n {
		line := w.read(t)
		var f struct {
			ID   int `json:"id"`
			Resp struct {
				Status  int            `json:"status"`
				Payload map[string]any `json:"payload"`
			} `json:"resp"`
		}
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("a line that is not a frame (%v): %s", err, line)
		}
		if f.Resp.Status != 200 {
			t.Fatalf("call %d answered %d: %s", f.ID, f.Resp.Status, line)
		}
		if f.Resp.Payload["path"] != fmt.Sprintf("/c%d", f.ID) {
			t.Fatalf("call %d was answered with another call's request: %s", f.ID, line)
		}
		pid, _ := f.Resp.Payload["pid"].(float64)
		pids[pid] = true
		ids = append(ids, f.ID)
	}
	sort.Ints(ids)
	for i, id := range ids {
		if id != i+1 {
			t.Fatalf("answered ids %v, want 1 to %d once each", ids, n)
		}
	}
	if len(pids) != 1 {
		t.Errorf("the calls were answered by %d processes", len(pids))
	}

	w.send(t, fmt.Sprintf(`{"id":%d,"deadline_ms":20000,"req":{"path":"/wait","headers":{},"query":"","body":{"op":"wait","n":20000}}}`, n+1))
	w.send(t, fmt.Sprintf(`{"id":%d,"cancel":"timeout"}`, n+1))
	cancelled := w.read(t)
	if !strings.Contains(cancelled, fmt.Sprintf(`"id":%d`, n+1)) || !strings.Contains(cancelled, `"status":504`) ||
		!strings.Contains(cancelled, context.Canceled.Error()) {
		t.Fatalf("the cancelled call's answer: %s", cancelled)
	}
	w.close(t)
}

// THE TEMPLATE DECISION, at concurrency 1: the generated main of an ordinary
// Pulse answers one envelope at a time, as the native template did, and what
// its handler prints reaches stderr, the function's log, instead of the line
// the slice reads as the response. Under the native template the printed line
// IS the first line on stdout. A context-first handler is served here too, its
// context carrying the call's runtime budget.
func TestTheGeneratedMainAtConcurrencyOneKeepsHandlerOutputOffTheResponse(t *testing.T) {
	for _, name := range []string{"mux-post", "mux-post-ctx"} {
		t.Run(name, func(t *testing.T) {
			w := spawn(t, name)
			w.send(t, `{"path":"/p","headers":{},"query":"","body":{"op":"print"}}`)
			w.send(t, `{"path":"/q","headers":{},"query":"","body":{"op":"echo"}}`)
			for _, path := range []string{"/p", "/q"} {
				line := w.read(t)
				var resp struct {
					Status   int            `json:"status"`
					Payload  map[string]any `json:"payload"`
					RSSBytes int64          `json:"rss_bytes"`
				}
				if err := json.Unmarshal([]byte(line), &resp); err != nil {
					t.Fatalf("the slice would read %q as the response to %s", line, path)
				}
				if resp.Status != 200 || resp.Payload["path"] != path || resp.RSSBytes <= 0 {
					t.Fatalf("the response to %s: %s", path, line)
				}
			}
			if name == "mux-post-ctx" {
				w.send(t, `{"path":"/budget","headers":{"X-User-Runtime":"1"},"query":"","body":{"op":"wait","n":20000}}`)
				line := w.read(t)
				if !strings.Contains(line, `"status":504`) || !strings.Contains(line, context.DeadlineExceeded.Error()) {
					t.Fatalf("a context-first handler at concurrency 1 was not given the call's budget: %s", line)
				}
			}
			w.close(t)
			if !strings.Contains(w.stderr.String(), "printed by the handler") {
				t.Errorf("the handler's print did not reach stderr: %q", w.stderr.String())
			}
		})
	}
}
