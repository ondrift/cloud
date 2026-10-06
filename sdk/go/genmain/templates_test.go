package genmain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every Go template a released drift CLI generates its main from is frozen
// under testdata/templates/<the first CLI release that shipped it>/, byte for
// byte as that release embedded it, and compiled here against this SDK. A Go
// build resolves the SDK at @latest, so a change to this module that breaks one
// of them breaks every function deployed with that CLI on its next build.
//
//	cli-v0.1.0   cli/v0.1.0 to cli/v0.9.x    one envelope, then exit
//	cli-v0.10.0  cli/v0.10.0 to cli/v0.56.x  the warm loop, per-call TMPDIR
//	cli-v0.57.0  cli/v0.57.0 onwards         the warm loop, per-call secrets
//
// next/ holds the concurrent templates as the contract writes them, until a
// CLI release ships them; that release freezes its own copies beside the
// others.
//
// Each template is rendered the way the CLI renders it, around the handler in
// testdata/probe, and the binaries are then run as the slice runs them.

// probeFuncs names the handler each template file wraps, and whether it is
// given a context.
var probeFuncs = map[string][]struct {
	suffix, fn, ctxArg string
}{
	"server_post_native.txt": {{"post", "PostProbe", ""}},
	"server_get_native.txt":  {{"get", "GetProbe", ""}},
	"server_post_mux.txt":    {{"post", "PostProbe", ""}, {"post-ctx", "PostProbeCtx", "ctx, "}},
	"server_get_mux.txt":     {{"get", "GetProbe", ""}, {"get-ctx", "GetProbeCtx", "ctx, "}},
}

var probes struct {
	once sync.Once
	dir  string // the built binaries, one per rendered template
	err  error
	out  string // the build's output, for a failure
	all  []string
}

// buildProbes renders every template in testdata/templates around the probe
// handler and builds them all in one go build, against this checkout of the
// SDK. Built once per test binary.
func buildProbes(t *testing.T) string {
	t.Helper()
	probes.once.Do(func() {
		probes.err = func() error {
			root, err := os.MkdirTemp("", "genmain-templates-")
			if err != nil {
				return err
			}
			sdk, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				return err
			}
			gomod := "module probes\n\ngo 1.26\n\nrequire github.com/ondrift/cloud/sdk v0.0.0\n\nreplace github.com/ondrift/cloud/sdk => " + sdk + "\n"
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o600); err != nil {
				return err
			}
			handler, err := os.ReadFile(filepath.Join("testdata", "probe", "probe.go"))
			if err != nil {
				return err
			}
			versions, err := os.ReadDir(filepath.Join("testdata", "templates"))
			if err != nil {
				return err
			}
			for _, v := range versions {
				files, err := os.ReadDir(filepath.Join("testdata", "templates", v.Name()))
				if err != nil {
					return err
				}
				for _, f := range files {
					shapes, ok := probeFuncs[f.Name()]
					if !ok {
						return fmt.Errorf("testdata/templates/%s/%s: no probe handler is named for this template; add it to probeFuncs", v.Name(), f.Name())
					}
					tpl, err := os.ReadFile(filepath.Join("testdata", "templates", v.Name(), f.Name()))
					if err != nil {
						return err
					}
					for _, s := range shapes {
						name := v.Name() + "-" + s.suffix
						code := strings.NewReplacer("{{FUNC}}", s.fn, "{{BODYTYPE}}", "ProbeBody", "{{CTXARG}}", s.ctxArg).Replace(string(tpl))
						dir := filepath.Join(root, "cmd", name)
						if err := os.MkdirAll(dir, 0o700); err != nil {
							return err
						}
						if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600); err != nil {
							return err
						}
						if err := os.WriteFile(filepath.Join(dir, "probe.go"), handler, 0o600); err != nil {
							return err
						}
						probes.all = append(probes.all, name)
					}
				}
			}
			bin := filepath.Join(root, "bin")
			cmd := exec.Command("go", "build", "-o", bin+string(filepath.Separator), "./...")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off", "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			probes.out = string(out)
			probes.dir = bin
			return err
		}()
	})
	if probes.err != nil {
		t.Fatalf("building the rendered templates: %v\n%s", probes.err, probes.out)
	}
	return probes.dir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if probes.dir != "" {
		_ = os.RemoveAll(filepath.Dir(probes.dir))
	}
	os.Exit(code)
}

// run starts a built template as the slice does, with env and stdin, and
// returns its stdout lines and its stderr once it exits.
func run(t *testing.T, name string, env []string, stdin string) ([]string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(buildProbes(t), name))
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "TMPDIR=" + t.TempDir()}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\nstderr: %s", name, err, stderr.String())
	}
	var lines []string
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, stderr.String()
}

// Every frozen template, and the concurrent ones, compiles against this SDK.
func TestEveryTemplateCompiles(t *testing.T) {
	bin := buildProbes(t)
	if len(probes.all) < 10 {
		t.Fatalf("rendered %d templates; the frozen set alone is 6", len(probes.all))
	}
	for _, name := range probes.all {
		if _, err := os.Stat(filepath.Join(bin, name)); err != nil {
			t.Errorf("%s: not built: %v", name, err)
		}
	}
}

var rssBytes = regexp.MustCompile(`"rss_bytes":[0-9]+,`)

// legacyEnvelopes are the calls the byte-compatibility test sends: secrets
// set, rotated and dropped, a scratch directory, response headers, a nil
// payload, characters JSON escapes, a reply over 64 KiB, and path parameters.
func legacyEnvelopes(method string) string {
	op := func(o, text string, n int) string {
		if method == "get" {
			b, _ := json.Marshal(fmt.Sprintf("op=%s&text=%s&n=%d", o, text, n))
			return `"query":` + string(b) + `,"body":null`
		}
		return fmt.Sprintf(`"query":"","body":{"op":%q,"text":%q,"n":%d}`, o, text, n)
	}
	lines := []string{
		`{"path":"/a","headers":{"X-User-Runtime":"5"},` + op("echo", "plain", 0) + `,"tmpdir":"/scratch/one","secrets":{"token":"s1"}}`,
		`{"path":"/b","headers":{},` + op("echo", "<b>&amp;\u00e9\u2028", 0) + `,"secrets":{"token":"s2"},"secrets_previous":{"token":"s1"}}`,
		`{"path":"/c","headers":{},` + op("echo", "dropped", 0) + `}`,
		`{"path":"/d","headers":{},` + op("headers", "h", 0) + `}`,
		`{"path":"/e","headers":{},` + op("nil", "", 0) + `}`,
		`{"path":"/f","headers":{},` + op("big", "", 100000) + `}`,
		`{"path":"/g/7","headers":{},` + op("echo", "p", 0) + `,"params":{"id":"7"},"route_pattern":"g/:id","env":{"K":"v"}}`,
	}
	return strings.Join(lines, "\n") + "\n"
}

// The sequential loop answers every envelope with the bytes today's released
// template writes for it, rss_bytes aside: the one reading that differs from
// process to process.
func TestLegacyLoopMatchesTheReleasedTemplate(t *testing.T) {
	for _, method := range []string{"post", "get"} {
		t.Run(method, func(t *testing.T) {
			in := legacyEnvelopes(method)
			env := []string{"DRIFT_RUNTIME=1"}
			want, _ := run(t, "cli-v0.57.0-"+method, env, in)
			for _, shape := range []string{method, method + "-ctx"} {
				got, _ := run(t, "next-"+shape, env, in)
				if len(got) != len(want) || len(want) != 7 {
					t.Fatalf("%s: %d lines, the released template %d, for 7 envelopes", shape, len(got), len(want))
				}
				for i := range want {
					w, g := rssBytes.ReplaceAllString(want[i], ""), rssBytes.ReplaceAllString(got[i], "")
					if w != g {
						t.Fatalf("%s, envelope %d:\n got %.300s\nwant %.300s", shape, i+1, g, w)
					}
					if !rssBytes.MatchString(got[i]) {
						t.Fatalf("%s, envelope %d carries no rss_bytes: %.200s", shape, i+1, got[i])
					}
				}
			}
		})
	}
}

// A process the slice spawns one-shot, as a trigger or a cron does, answers
// its one envelope and exits when stdin closes.
func TestLegacyLoopServesAOneShotSpawn(t *testing.T) {
	lines, _ := run(t, "next-post-ctx", []string{"DRIFT_RUNTIME=1"}, `{"path":"/once","body":{"op":"echo"}}`)
	if len(lines) != 1 || !strings.Contains(lines[0], `"path":"/once"`) {
		t.Fatalf("one-shot answer: %q", lines)
	}
}

// THE FINDING, against today's released template: a handler that prints to
// stdout puts its line where the slice reads the response. The warm path
// reads one line per call (warmpool.rs, Worker::call) and parses it as the
// response (runner.rs, invoke_warm), so the call fails on the printed line,
// and a printed line that parses as a response is taken for one.
func TestAPrintingHandlerCorruptsTheReleasedTemplatesResponse(t *testing.T) {
	lines, _ := run(t, "cli-v0.57.0-post", []string{"DRIFT_RUNTIME=1"}, `{"path":"/p","body":{"op":"print"}}`+"\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q", lines)
	}
	if lines[0] != "printed by the handler" {
		t.Fatalf("first line the slice would read: %q", lines[0])
	}
	if json.Valid([]byte(lines[0])) {
		t.Fatal("the printed line parses; the finding is that it does not")
	}
	if lines[1] != `{"status":299,"message":"forged by a print"}` {
		t.Fatalf("second line: %q", lines[1])
	}
}

// What a handler prints reaches stderr, the function's log, in every slice
// mode, and the protocol stream carries nothing but protocol.
func TestAPrintingHandlerLeavesTheProtocolClean(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		in   string
	}{
		{"sequential", []string{"DRIFT_RUNTIME=1"}, `{"path":"/p","body":{"op":"print"}}` + "\n"},
		{"concurrent", []string{"DRIFT_RUNTIME=1", "DRIFT_INVOKE=mux1", "DRIFT_MUX_SLOTS=2"},
			`{"id":1,"deadline_ms":5000,"req":{"path":"/p","body":{"op":"print"}}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, stderr := run(t, "next-post-ctx", tc.env, tc.in)
			for _, l := range lines {
				var obj map[string]json.RawMessage
				if json.Unmarshal([]byte(l), &obj) != nil {
					t.Fatalf("a non-protocol line on the protocol stream: %q", l)
				}
				if strings.Contains(l, "forged") {
					t.Fatalf("the handler's print reached the protocol stream: %q", l)
				}
			}
			last := lines[len(lines)-1]
			if !strings.Contains(last, `"status":200`) || !strings.Contains(last, `"path":"/p"`) {
				t.Fatalf("the call's answer: %q", last)
			}
			if !strings.Contains(stderr, "printed by the handler\n") || !strings.Contains(stderr, "forged by a print") {
				t.Fatalf("the handler's prints are not on stderr: %q", stderr)
			}
		})
	}
}

// The concurrent loop over a real pipe: hello first, then sixteen calls with
// replies far over the pipe's atomic write size, every line one whole frame,
// and a cancel frame reaching a handler that waits on its context.
func TestConcurrentLoopOverARealPipe(t *testing.T) {
	var in strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&in, `{"id":%d,"deadline_ms":20000,"req":{"path":"/big%d","body":{"op":"big","n":300000}}}`+"\n", i, i)
	}
	in.WriteString(`{"id":17,"deadline_ms":20000,"req":{"path":"/wait","body":{"op":"wait","n":20000}}}` + "\n")
	in.WriteString(`{"id":17,"cancel":"timeout"}` + "\n")
	lines, _ := run(t, "next-post-ctx", []string{"DRIFT_RUNTIME=1", "DRIFT_INVOKE=mux1", "DRIFT_MUX_SLOTS=17"}, in.String())

	if len(lines) != 18 || !strings.HasPrefix(lines[0], `{"hello":{"proto":"drift-mux","v":1,"slots":17,"pid":`) {
		t.Fatalf("%d lines, first %.120q", len(lines), lines[0])
	}
	var ids []int
	for _, l := range lines[1:] {
		var f struct {
			ID   int `json:"id"`
			Resp struct {
				Status  int `json:"status"`
				Payload struct {
					Text string `json:"text"`
					Err  string `json:"err"`
				} `json:"payload"`
			} `json:"resp"`
		}
		if err := json.Unmarshal([]byte(l), &f); err != nil {
			t.Fatalf("a line that is not one frame (%v): %.200q", err, l)
		}
		switch {
		case f.ID == 17:
			if f.Resp.Status != 504 || f.Resp.Payload.Err != context.Canceled.Error() {
				t.Fatalf("the cancelled call answered %d %q", f.Resp.Status, f.Resp.Payload.Err)
			}
		case len(f.Resp.Payload.Text) != 300000:
			t.Fatalf("call %d: a %d-byte reply, want 300000", f.ID, len(f.Resp.Payload.Text))
		}
		ids = append(ids, f.ID)
	}
	sort.Ints(ids)
	for i, id := range ids {
		if id != i+1 {
			t.Fatalf("answered ids %v, want 1 to 17 once each", ids)
		}
	}
}
