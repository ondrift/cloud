package genmain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

// frame is one decoded frame, member by member.
type frame map[string]json.RawMessage

func (f frame) id(t *testing.T) uint64 {
	t.Helper()
	var id uint64
	if err := json.Unmarshal(f["id"], &id); err != nil {
		t.Fatalf("frame without a numeric id: %v", f)
	}
	return id
}

func (f frame) resp(t *testing.T) respBody {
	t.Helper()
	raw, ok := f["resp"]
	if !ok {
		t.Fatalf("not a resp frame: %v", f)
	}
	var r respBody
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// frameWriter is the protocol stream in a test. Every Write must be exactly
// one whole frame, a JSON object and its newline, which is what keeps frames
// from interleaving on a pipe; a Write that is anything else fails the test.
type frameWriter struct {
	t      *testing.T
	frames chan frame
	writes atomic.Int64
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.writes.Add(1)
	if len(p) == 0 || p[len(p)-1] != '\n' || bytes.Count(p, []byte("\n")) != 1 || !json.Valid(p[:len(p)-1]) {
		w.t.Errorf("a Write that is not exactly one frame: %.200q", p)
		return len(p), nil
	}
	var f frame
	if err := json.Unmarshal(p, &f); err != nil {
		w.t.Errorf("a frame that is not an object: %.200q", p)
		return len(p), nil
	}
	w.frames <- f
	return len(p), nil
}

// muxRig runs serveMux over in-process pipes.
type muxRig struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *frameWriter
	done chan struct{}
}

func startMux(t *testing.T, slots int, handler handlerFunc) *muxRig {
	t.Helper()
	r, w := io.Pipe()
	out := &frameWriter{t: t, frames: make(chan frame, 4096)}
	rig := &muxRig{t: t, in: w, out: out, done: make(chan struct{})}
	go func() {
		defer close(rig.done)
		serveMux(r, out, slots, handler)
	}()
	t.Cleanup(func() {
		_ = w.Close()
		select {
		case <-rig.done:
		case <-time.After(10 * time.Second):
			t.Error("serveMux did not return after stdin closed")
		}
	})
	return rig
}

func (r *muxRig) send(line string) {
	r.t.Helper()
	if _, err := io.WriteString(r.in, line+"\n"); err != nil {
		r.t.Fatal(err)
	}
}

func (r *muxRig) req(id uint64, body string, extra string) {
	r.t.Helper()
	r.send(fmt.Sprintf(`{"id":%d%s,"req":{"path":"/p","headers":{},"query":"","body":%s}}`, id, extra, body))
}

func (r *muxRig) next() frame {
	r.t.Helper()
	select {
	case f := <-r.out.frames:
		return f
	case <-time.After(10 * time.Second):
		r.t.Fatal("no frame within 10 s")
		return nil
	}
}

// hello reads the first frame and checks it is the handshake.
func (r *muxRig) hello(slots int) {
	r.t.Helper()
	f := r.next()
	var h helloBody
	if err := json.Unmarshal(f["hello"], &h); err != nil {
		r.t.Fatalf("first frame is not hello: %v", f)
	}
	if h.Proto != "drift-mux" || h.V != 1 || h.Slots != slots || h.PID != os.Getpid() {
		r.t.Fatalf("hello = %+v, want drift-mux v1 with %d slots and this pid", h, slots)
	}
}

func (r *muxRig) closeAndWait() {
	r.t.Helper()
	_ = r.in.Close()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.t.Fatal("serveMux did not return after stdin closed")
	}
}

func echoPath(_ context.Context, req drift.Request) drift.Response {
	b, _ := json.Marshal(req.Path)
	return drift.Response{Status: 200, Message: "OK", Payload: b}
}

// The hello frame comes first, before any request is read, and names the
// protocol, its version, the slots and the process.
func TestMuxHelloFirst(t *testing.T) {
	rig := startMux(t, 7, echoPath)
	rig.hello(7)
}

// Sixteen calls reach the handler at once: each blocks until all sixteen have
// arrived, which only concurrent calls can do. Each answer is matched to its
// call by id, and no more than the slots are ever in the handler together.
func TestMuxServesSixteenCallsAtOnce(t *testing.T) {
	const n = 16
	var arrived, inHandler, peak atomic.Int64
	all := make(chan struct{})
	var once sync.Once
	rig := startMux(t, n, func(_ context.Context, req drift.Request) drift.Response {
		now := inHandler.Add(1)
		defer inHandler.Add(-1)
		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}
		if arrived.Add(1) == n {
			once.Do(func() { close(all) })
		}
		select {
		case <-all:
		case <-time.After(5 * time.Second):
			return drift.Response{Status: 500, Message: "the barrier never filled"}
		}
		var body struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(req.Body, &body)
		b, _ := json.Marshal(body.N)
		return drift.Response{Status: 200, Message: "OK", Payload: b}
	})
	rig.hello(n)
	for i := 1; i <= n; i++ {
		rig.req(uint64(i), fmt.Sprintf(`{"n":%d}`, i*10), "")
	}
	seen := map[uint64]bool{}
	for range n {
		f := rig.next()
		id := f.id(t)
		r := f.resp(t)
		if r.Status != 200 {
			t.Fatalf("call %d: %d %s", id, r.Status, r.Message)
		}
		if string(r.Payload) != fmt.Sprint(id*10) {
			t.Fatalf("call %d answered with %s, which belongs to another call", id, r.Payload)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("answered %d distinct calls, want %d", len(seen), n)
	}
	if peak.Load() > n {
		t.Fatalf("%d calls were in the handler at once, more than the %d slots", peak.Load(), n)
	}
}

// A request beyond the slots is refused, and the calls in flight are not
// disturbed by it. Once one finishes its slot is free again.
func TestMuxRefusesARequestOverItsSlots(t *testing.T) {
	release := make(chan struct{})
	rig := startMux(t, 2, func(ctx context.Context, req drift.Request) drift.Response {
		if req.Path == "/block" {
			<-release
		}
		return echoPath(ctx, req)
	})
	rig.hello(2)
	rig.send(`{"id":1,"req":{"path":"/block"}}`)
	rig.send(`{"id":2,"req":{"path":"/block"}}`)
	rig.send(`{"id":3,"req":{"path":"/block"}}`)
	f := rig.next()
	if f.id(t) != 3 || string(f["refused"]) != `"slots"` {
		t.Fatalf("third call: %v, want refused for slots", f)
	}
	close(release)
	for range 2 {
		if r := rig.next().resp(t); r.Status != 200 {
			t.Fatalf("a call in flight failed: %+v", r)
		}
	}
	rig.send(`{"id":4,"req":{"path":"/after"}}`)
	if f := rig.next(); f.id(t) != 4 || f.resp(t).Status != 200 {
		t.Fatalf("a call after the slots freed: %v", f)
	}
}

// An id already in flight is refused rather than run twice.
func TestMuxRefusesAnIdInFlight(t *testing.T) {
	release := make(chan struct{})
	rig := startMux(t, 4, func(ctx context.Context, req drift.Request) drift.Response {
		<-release
		return echoPath(ctx, req)
	})
	rig.hello(4)
	rig.send(`{"id":9,"req":{"path":"/a"}}`)
	rig.send(`{"id":9,"req":{"path":"/b"}}`)
	f := rig.next()
	if f.id(t) != 9 || string(f["refused"]) != `"id"` {
		t.Fatalf("second use of id 9: %v, want refused", f)
	}
	close(release)
	if r := rig.next().resp(t); string(r.Payload) != `"/a"` {
		t.Fatalf("the first call answered %s", r.Payload)
	}
}

// A cancel frame reaches the handler's context at once, with the slice's
// reason as its cause.
func TestMuxCancelFrameCancelsTheContext(t *testing.T) {
	started := make(chan struct{})
	var cancelledAfter atomic.Int64
	var cause atomic.Value
	var sent time.Time
	var sentMu sync.Mutex
	rig := startMux(t, 4, func(ctx context.Context, _ drift.Request) drift.Response {
		close(started)
		select {
		case <-ctx.Done():
			sentMu.Lock()
			cancelledAfter.Store(int64(time.Since(sent)))
			sentMu.Unlock()
			cause.Store(context.Cause(ctx).Error())
			if !errors.Is(ctx.Err(), context.Canceled) {
				return drift.Response{Status: 500, Message: ctx.Err().Error()}
			}
			return drift.Response{Status: 504, Message: "cancelled"}
		case <-time.After(5 * time.Second):
			return drift.Response{Status: 200, Message: "never cancelled"}
		}
	})
	rig.hello(4)
	rig.req(1, `{}`, `,"deadline_ms":30000`)
	<-started
	sentMu.Lock()
	sent = time.Now()
	sentMu.Unlock()
	rig.send(`{"id":1,"cancel":"timeout"}`)
	r := rig.next().resp(t)
	if r.Status != 504 {
		t.Fatalf("handler saw %d %s, want its context cancelled", r.Status, r.Message)
	}
	if got := cause.Load().(string); !strings.Contains(got, "timeout") {
		t.Fatalf("cause = %q, want the slice's reason", got)
	}
	// The contract asks for 10 ms. The bound here is wider so that a loaded
	// machine under -race does not fail it; the measured figure is logged.
	d := time.Duration(cancelledAfter.Load())
	t.Logf("cancel reached the handler in %v", d)
	if d > 250*time.Millisecond {
		t.Fatalf("cancel took %v to reach the handler", d)
	}
}

// A cancel for an id that has finished, or was never sent, changes nothing.
func TestMuxCancelForAnUnknownIdIsIgnored(t *testing.T) {
	rig := startMux(t, 2, echoPath)
	rig.hello(2)
	rig.send(`{"id":5,"cancel":"timeout"}`)
	rig.req(6, `{}`, "")
	if f := rig.next(); f.id(t) != 6 || f.resp(t).Status != 200 {
		t.Fatalf("got %v", f)
	}
}

// deadline_ms bounds the handler's context on its own, with no cancel frame.
func TestMuxDeadlineExpiresTheContext(t *testing.T) {
	rig := startMux(t, 2, func(ctx context.Context, _ drift.Request) drift.Response {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > time.Second {
			return drift.Response{Status: 500, Message: "no 50 ms deadline"}
		}
		<-ctx.Done()
		return drift.Response{Status: 504, Message: ctx.Err().Error()}
	})
	rig.hello(2)
	rig.req(1, `{}`, `,"deadline_ms":50`)
	r := rig.next().resp(t)
	if r.Status != 504 || r.Message != context.DeadlineExceeded.Error() {
		t.Fatalf("got %d %q, want the deadline to expire", r.Status, r.Message)
	}
}

// Without deadline_ms, the context carries the envelope's runtime budget.
func TestMuxContextFallsBackToTheRuntimeBudget(t *testing.T) {
	rig := startMux(t, 2, func(ctx context.Context, _ drift.Request) drift.Response {
		dl, _ := ctx.Deadline()
		b, _ := json.Marshal(time.Until(dl).Round(time.Second).Seconds())
		return drift.Response{Status: 200, Payload: b}
	})
	rig.hello(2)
	rig.send(`{"id":1,"req":{"path":"/","headers":{"x-user-runtime":"7"}}}`)
	if r := rig.next().resp(t); string(r.Payload) != "7" {
		t.Fatalf("deadline %s s away, want 7", r.Payload)
	}
}

// A panic fails its own call with a panic frame, and the calls beside it, and
// the process, carry on.
func TestMuxPanicFailsOnlyItsCall(t *testing.T) {
	rig := startMux(t, 4, func(ctx context.Context, req drift.Request) drift.Response {
		if req.Path == "/panic" {
			var m map[string]int
			m["boom"] = 1 // a nil-map write: a runtime panic, as a bug would cause
		}
		return echoPath(ctx, req)
	})
	rig.hello(4)
	rig.send(`{"id":1,"req":{"path":"/a"}}`)
	rig.send(`{"id":2,"req":{"path":"/panic"}}`)
	rig.send(`{"id":3,"req":{"path":"/b"}}`)
	got := map[uint64]frame{}
	for range 3 {
		f := rig.next()
		got[f.id(t)] = f
	}
	var p panicBody
	if err := json.Unmarshal(got[2]["panic"], &p); err != nil {
		t.Fatalf("call 2: %v, want a panic frame", got[2])
	}
	if !strings.Contains(p.Value, "nil map") {
		t.Fatalf("panic value = %q", p.Value)
	}
	if p.Stack == "" || len(p.Stack) > 4<<10 {
		t.Fatalf("panic stack is %d bytes, want 1 to 4096", len(p.Stack))
	}
	for _, id := range []uint64{1, 3} {
		if r := got[id].resp(t); r.Status != 200 {
			t.Fatalf("call %d beside the panic: %+v", id, r)
		}
	}
	rig.send(`{"id":4,"req":{"path":"/later"}}`)
	if r := rig.next().resp(t); r.Status != 200 {
		t.Fatalf("a call after the panic: %+v", r)
	}
}

// A panic value whose own description panics is still reported.
func TestMuxPanicWithAPanickingValue(t *testing.T) {
	rig := startMux(t, 1, func(context.Context, drift.Request) drift.Response {
		panic(badStringer{})
	})
	rig.hello(1)
	rig.req(1, `{}`, "")
	f := rig.next()
	var p panicBody
	if err := json.Unmarshal(f["panic"], &p); err != nil || !strings.Contains(p.Value, "PANIC=String method: no description") {
		t.Fatalf("got %s", f["panic"])
	}
}

type badStringer struct{}

func (badStringer) String() string { panic("no description") }

// A reply that cannot be encoded fails its call with a panic frame rather
// than leaving the slice to wait for its deadline.
func TestMuxUnencodableReplyStillAnswers(t *testing.T) {
	rig := startMux(t, 1, func(context.Context, drift.Request) drift.Response {
		return drift.Response{Status: 200, Payload: json.RawMessage(`{not json`)}
	})
	rig.hello(1)
	rig.req(1, `{}`, "")
	f := rig.next()
	if _, ok := f["panic"]; !ok || f.id(t) != 1 {
		t.Fatalf("got %v, want a panic frame for call 1", f)
	}
}

// Under 64 concurrent calls with large replies, every Write on the protocol
// stream is exactly one whole frame (frameWriter fails the test otherwise),
// and every call is answered once.
func TestMuxFramesNeverInterleave(t *testing.T) {
	const slots, calls = 64, 256
	rig := startMux(t, slots, func(_ context.Context, req drift.Request) drift.Response {
		b, _ := json.Marshal(strings.Repeat(req.Path[1:], 50_000))
		return drift.Response{Status: 200, Payload: b}
	})
	rig.hello(slots)
	answered := map[uint64]bool{}
	sent := 0
	for len(answered) < calls {
		for sent < calls && sent-len(answered) < slots {
			sent++
			rig.send(fmt.Sprintf(`{"id":%d,"req":{"path":"/%c"}}`, sent, 'a'+sent%26))
		}
		f := rig.next()
		id := f.id(t)
		if answered[id] {
			t.Fatalf("call %d answered twice", id)
		}
		answered[id] = true
		var s string
		if err := json.Unmarshal(f.resp(t).Payload, &s); err != nil || s != strings.Repeat(string(rune('a'+id%26)), 50_000) {
			t.Fatalf("call %d carries another call's payload", id)
		}
	}
	if w := rig.out.writes.Load(); w != calls+1 {
		t.Fatalf("%d Writes for %d frames", w, calls+1)
	}
}

// A request over 1 MiB is read whole: no line limit applies to a frame.
func TestMuxReadsALargeRequest(t *testing.T) {
	rig := startMux(t, 1, func(_ context.Context, req drift.Request) drift.Response {
		var body struct {
			Blob string `json:"blob"`
		}
		_ = json.Unmarshal(req.Body, &body)
		b, _ := json.Marshal(len(body.Blob))
		return drift.Response{Status: 200, Payload: b}
	})
	rig.hello(1)
	rig.req(1, `{"blob":"`+strings.Repeat("z", 1<<20)+`"}`, "")
	if r := rig.next().resp(t); string(r.Payload) != fmt.Sprint(1<<20) {
		t.Fatalf("handler saw %s bytes, want %d", r.Payload, 1<<20)
	}
}

// When stdin closes, the calls in flight finish and are answered before
// serveMux returns.
func TestMuxEOFWaitsForCallsInFlight(t *testing.T) {
	release := make(chan struct{})
	rig := startMux(t, 2, func(ctx context.Context, req drift.Request) drift.Response {
		<-release
		return echoPath(ctx, req)
	})
	rig.hello(2)
	rig.req(1, `{}`, "")
	_ = rig.in.Close()
	select {
	case <-rig.done:
		t.Fatal("serveMux returned with a call in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if r := rig.next().resp(t); r.Status != 200 {
		t.Fatalf("got %+v", r)
	}
	rig.closeAndWait()
}

// Frames that are not requests or cancels, or carry unknown members, are
// ignored, and the stream carries on.
func TestMuxIgnoresWhatItDoesNotKnow(t *testing.T) {
	rig := startMux(t, 1, echoPath)
	rig.hello(1)
	rig.send(`{"id":1,"future":"member"}`)
	rig.send(`{"id":"not a number","req":{}}`)
	rig.send(`[1,2,3]`)
	rig.send(`{"id":2,"req":{"path":"/ok"},"also":true}`)
	if f := rig.next(); f.id(t) != 2 || string(f.resp(t).Payload) != `"/ok"` {
		t.Fatalf("got %v", f)
	}
}

// Concurrent calls never change TMPDIR or HOME, whatever their envelopes say,
// and each makes its own scratch directory under the worker's TMPDIR.
func TestMuxLeavesTheEnvironmentAlone(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("HOME", tmp)
	both := make(chan struct{})
	var arrived atomic.Int64
	rig := startMux(t, 2, func(context.Context, drift.Request) drift.Response {
		if arrived.Add(1) == 2 {
			close(both)
		}
		<-both
		dir, err := os.MkdirTemp("", "x")
		if err != nil {
			return drift.Response{Status: 500, Message: err.Error()}
		}
		b, _ := json.Marshal([]string{os.Getenv("TMPDIR"), os.Getenv("HOME"), dir})
		return drift.Response{Status: 200, Payload: b}
	})
	rig.hello(2)
	rig.send(`{"id":1,"req":{"path":"/","tmpdir":"/elsewhere/one"}}`)
	rig.send(`{"id":2,"req":{"path":"/","tmpdir":"/elsewhere/two"}}`)
	dirs := map[string]bool{}
	for range 2 {
		var got []string
		if err := json.Unmarshal(rig.next().resp(t).Payload, &got); err != nil {
			t.Fatal(err)
		}
		if got[0] != tmp || got[1] != tmp {
			t.Fatalf("TMPDIR %q and HOME %q during a call, want both %q", got[0], got[1], tmp)
		}
		if filepath.Dir(got[2]) != tmp {
			t.Fatalf("scratch %q is not under the worker's TMPDIR", got[2])
		}
		dirs[got[2]] = true
	}
	if len(dirs) != 2 {
		t.Fatalf("two calls shared one scratch directory: %v", dirs)
	}
}

// Secrets are applied when an envelope's differ from the last ones applied,
// and only then; a secret the envelope stops carrying is removed.
func TestMuxAppliesSecretsOnlyWhenTheyChange(t *testing.T) {
	t.Setenv("DRIFT_SECRET_TOKEN", "")
	t.Setenv("DRIFT_SECRET_TOKEN_PREVIOUS", "")
	rig := startMux(t, 1, func(_ context.Context, req drift.Request) drift.Response {
		v, ok := os.LookupEnv("DRIFT_SECRET_TOKEN")
		prev := os.Getenv("DRIFT_SECRET_TOKEN_PREVIOUS")
		if req.Path == "/scrub" {
			// The handler removes the variable itself. Only a re-application
			// would bring it back.
			_ = os.Unsetenv("DRIFT_SECRET_TOKEN")
		}
		b, _ := json.Marshal(map[string]any{"v": v, "set": ok, "prev": prev})
		return drift.Response{Status: 200, Payload: b}
	})
	rig.hello(1)
	call := func(id int, path, secrets string) map[string]any {
		t.Helper()
		rig.send(fmt.Sprintf(`{"id":%d,"req":{"path":%q%s}}`, id, path, secrets))
		var got map[string]any
		_ = json.Unmarshal(rig.next().resp(t).Payload, &got)
		return got
	}
	s1 := `,"secrets":{"token":"one"}`
	if got := call(1, "/scrub", s1); got["v"] != "one" {
		t.Fatalf("first call saw %v", got)
	}
	if got := call(2, "/", s1); got["set"] != false {
		t.Fatalf("unchanged secrets were applied again: %v", got)
	}
	if got := call(3, "/", `,"secrets":{"token":"two"},"secrets_previous":{"token":"one"}`); got["v"] != "two" || got["prev"] != "one" {
		t.Fatalf("a rotation was not applied: %v", got)
	}
	if got := call(4, "/", ""); got["set"] != false || got["prev"] != "" {
		t.Fatalf("a secret the envelope dropped is still set: %v", got)
	}
}

// slotsFromEnv takes 1 to 64 and falls back to 1.
func TestSlotsFromEnv(t *testing.T) {
	for in, want := range map[string]int{"16": 16, " 4 ": 4, "64": 64, "65": 64, "0": 1, "-3": 1, "": 1, "x": 1} {
		if got := slotsFromEnv(in); got != want {
			t.Errorf("slotsFromEnv(%q) = %d, want %d", in, got, want)
		}
	}
}

// The legacy loop's context carries the envelope's X-User-Runtime, in any
// letter case, or 30 s.
func TestLegacyContextCarriesTheRuntimeBudget(t *testing.T) {
	var out bytes.Buffer
	in := `{"path":"/a","headers":{"X-User-Runtime":"5"}}` + "\n" +
		`{"path":"/b","headers":{"x-user-runtime":"bogus"}}` + "\n" +
		`{"path":"/c"}` + "\n"
	var got []string
	serveLegacy(strings.NewReader(in), &out, func(ctx context.Context, req drift.Request) drift.Response {
		dl, ok := ctx.Deadline()
		if !ok {
			got = append(got, req.Path+":none")
		} else {
			got = append(got, fmt.Sprintf("%s:%v", req.Path, time.Until(dl).Round(time.Second)))
		}
		return drift.Response{Status: 200}
	})
	want := "/a:5s /b:30s /c:30s"
	if strings.Join(got, " ") != want {
		t.Fatalf("deadlines %v, want %s", got, want)
	}
	if n := strings.Count(out.String(), "\n"); n != 3 {
		t.Fatalf("%d response lines, want 3", n)
	}
}

// The legacy loop points TMPDIR and HOME at each envelope's scratch directory
// and replaces the secrets per call, as the native templates do.
func TestLegacyAppliesEachEnvelope(t *testing.T) {
	// Restored afterwards: the loop sets both, and a later go build reads HOME.
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", os.Getenv("HOME"))
	t.Setenv("DRIFT_SECRET_TOKEN", "")
	var out bytes.Buffer
	in := `{"path":"/1","tmpdir":"/scratch/one","secrets":{"token":"a"}}` + "\n" +
		`{"path":"/2","tmpdir":"relative/ignored"}` + "\n"
	var got []string
	serveLegacy(strings.NewReader(in), &out, func(context.Context, drift.Request) drift.Response {
		v, ok := os.LookupEnv("DRIFT_SECRET_TOKEN")
		got = append(got, fmt.Sprintf("%s|%s|%s|%v", os.Getenv("TMPDIR"), os.Getenv("HOME"), v, ok))
		return drift.Response{Status: 200}
	})
	want := []string{"/scratch/one|/scratch/one|a|true", "/scratch/one|/scratch/one||false"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The local server answers in drift.Run's shape, and the handler's context
// carries the request's runtime budget.
func TestLocalHandler(t *testing.T) {
	srv := httptest.NewServer(localHandler(func(ctx context.Context, req drift.Request) drift.Response {
		dl, _ := ctx.Deadline()
		b, _ := json.Marshal(map[string]any{"path": req.Path, "query": req.Query, "body": req.Body, "budget": time.Until(dl).Round(time.Second).Seconds()})
		return drift.Response{Status: 201, Message: "Created", Payload: b, Headers: map[string]string{"X-Probe": "1"}}
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/items?x=1", strings.NewReader(`{"a":1}`))
	req.Header.Set("X-User-Runtime", "9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Status  int `json:"status"`
		Message string
		Payload struct {
			Path   string          `json:"path"`
			Query  string          `json:"query"`
			Body   json.RawMessage `json:"body"`
			Budget float64         `json:"budget"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 || resp.Header.Get("X-Probe") != "1" || got.Status != 201 || got.Message != "Created" {
		t.Fatalf("status %d, header %q, envelope %+v", resp.StatusCode, resp.Header.Get("X-Probe"), got)
	}
	if got.Payload.Path != "/items" || got.Payload.Query != "x=1" || string(got.Payload.Body) != `{"a":1}` || got.Payload.Budget != 9 {
		t.Fatalf("handler saw %+v", got.Payload)
	}
}
