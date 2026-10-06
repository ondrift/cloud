package genmain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"runtime/debug"
	"sync"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

// The concurrent protocol, version 1. Frames are one JSON object per line.
//
// Slice to worker:
//
//	{"id":7,"deadline_ms":29500,"req":{<the envelope>}}
//	{"id":7,"cancel":"timeout"}
//
// Worker to slice, hello first:
//
//	{"hello":{"proto":"drift-mux","v":1,"slots":16,"pid":1234}}
//	{"id":7,"resp":{"status":200,"message":"OK","payload":{...},"headers":{...}}}
//	{"id":7,"panic":{"value":"...","stack":"<at most 4 KiB>"}}
//	{"id":7,"refused":"slots"}
const (
	muxProto   = "drift-mux"
	muxVersion = 1
	// maxPanicStack bounds the stack a panic frame carries.
	maxPanicStack = 4 << 10
)

// inFrame is any frame the slice sends. Which member is present says what it
// is; unknown members are ignored.
type inFrame struct {
	ID         uint64          `json:"id"`
	DeadlineMS int64           `json:"deadline_ms"`
	Req        json.RawMessage `json:"req"`
	Cancel     *string         `json:"cancel"`
}

type helloFrame struct {
	Hello helloBody `json:"hello"`
}

type helloBody struct {
	Proto string `json:"proto"`
	V     int    `json:"v"`
	Slots int    `json:"slots"`
	PID   int    `json:"pid"`
}

type respFrame struct {
	ID   uint64   `json:"id"`
	Resp respBody `json:"resp"`
}

// respBody is the slice's response shape without rss_bytes, which means
// nothing per call in a shared process.
type respBody struct {
	Status  int               `json:"status"`
	Message string            `json:"message"`
	Payload json.RawMessage   `json:"payload"`
	Headers map[string]string `json:"headers,omitempty"`
}

type panicFrame struct {
	ID    uint64    `json:"id"`
	Panic panicBody `json:"panic"`
}

type panicBody struct {
	Value string `json:"value"`
	Stack string `json:"stack"`
}

type refusedFrame struct {
	ID      uint64 `json:"id"`
	Refused string `json:"refused"`
}

// muxServer is one process's side of the concurrent protocol.
type muxServer struct {
	handler handlerFunc
	slots   int

	// wmu serialises frames: each is one Write of its JSON and a newline, so
	// no two interleave on the stream.
	wmu sync.Mutex
	out io.Writer

	// mu guards calls: the cancel function of every call in flight, by id.
	mu    sync.Mutex
	calls map[uint64]context.CancelCauseFunc

	wg sync.WaitGroup

	// Touched by the reader goroutine only.
	secrets      *secretState
	lastSecrets  map[string]string
	lastPrevious map[string]string
	applied      bool
}

// serveMux runs the concurrent loop: hello, then every frame on in until it
// ends, then waits for the calls in flight and returns.
//
// The process environment is never changed per call except for secrets:
// TMPDIR and HOME stay the worker's own directory, shared by the calls it
// serves, and a call that needs scratch space makes its own there with
// os.MkdirTemp. Secrets are the function's own declared values, the same for
// every call except across a rotation, so they are applied when they change,
// in the reader, before the call that carries the change starts.
func serveMux(in io.Reader, out io.Writer, slots int, handler handlerFunc) {
	s := &muxServer{
		handler: handler,
		slots:   slots,
		out:     out,
		calls:   map[uint64]context.CancelCauseFunc{},
		secrets: newSecretState(),
	}
	_ = s.write(helloFrame{Hello: helloBody{Proto: muxProto, V: muxVersion, Slots: slots, PID: os.Getpid()}})

	dec := json.NewDecoder(in)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			// The slice closed stdin, which it does only once nothing it is
			// waiting for is in flight; or the stream is no longer JSON, and
			// nothing more can be read from it. Either way, finish what runs.
			break
		}
		var f inFrame
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		switch {
		case f.Req != nil:
			s.start(f)
		case f.Cancel != nil:
			s.cancel(f.ID, *f.Cancel)
		}
	}
	s.wg.Wait()
}

// start admits one call and runs it on its own goroutine, or refuses it.
func (s *muxServer) start(f inFrame) {
	var req drift.Request
	_ = json.Unmarshal(f.Req, &req)
	var extras envelopeExtras
	_ = json.Unmarshal(f.Req, &extras)

	budget := runtimeBudget(req.Headers)
	if f.DeadlineMS > 0 {
		budget = time.Duration(f.DeadlineMS) * time.Millisecond
	}
	parent, cancel := context.WithCancelCause(context.Background())

	s.mu.Lock()
	refusal := ""
	if _, dup := s.calls[f.ID]; dup {
		refusal = "id"
	} else if len(s.calls) >= s.slots {
		refusal = "slots"
	} else {
		s.calls[f.ID] = cancel
	}
	s.mu.Unlock()
	if refusal != "" {
		cancel(nil)
		_ = s.write(refusedFrame{ID: f.ID, Refused: refusal})
		return
	}

	s.applySecrets(extras.Secrets, extras.Previous)

	ctx, stop := context.WithTimeout(parent, budget)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer stop()
		defer cancel(nil)
		s.run(ctx, f.ID, req)
	}()
}

// run calls the handler and answers the call. The call leaves the in-flight
// set before its frame is written, so a slice that sends its next request the
// moment it reads this answer always finds the slot free.
func (s *muxServer) run(ctx context.Context, id uint64, req drift.Request) {
	resp, p := callRecovering(ctx, s.handler, req)

	s.mu.Lock()
	delete(s.calls, id)
	s.mu.Unlock()

	if p != nil {
		_ = s.write(panicFrame{ID: id, Panic: *p})
		return
	}
	frame := respFrame{ID: id, Resp: respBody{
		Status:  resp.Status,
		Message: resp.Message,
		Payload: payloadOrNull(resp.Payload),
		Headers: resp.Headers,
	}}
	if err := s.write(frame); errors.Is(err, errMarshal) {
		// The caller still gets an answer, a failure, rather than waiting for
		// its deadline on a frame that was never sent.
		_ = s.write(panicFrame{ID: id, Panic: panicBody{
			Value: "drift: the handler's reply cannot be encoded: " + err.Error(),
		}})
	}
}

// callRecovering calls the handler, turning a panic in its own goroutine into a
// panic report for this call alone.
func callRecovering(ctx context.Context, h handlerFunc, req drift.Request) (resp drift.Response, p *panicBody) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			if len(stack) > maxPanicStack {
				stack = stack[:maxPanicStack]
			}
			// fmt reports a value whose own String or Error method panics
			// rather than panicking again.
			p = &panicBody{Value: fmt.Sprint(r), Stack: string(stack)}
		}
	}()
	return h(ctx, req), nil
}

// cancel ends one call's context early. A call that has already finished, or
// an id never seen, is nothing to cancel.
func (s *muxServer) cancel(id uint64, reason string) {
	s.mu.Lock()
	c := s.calls[id]
	s.mu.Unlock()
	if c != nil {
		c(fmt.Errorf("drift: the slice cancelled this call: %s", reason))
	}
}

// applySecrets applies a call's secrets when they differ from the last ones
// applied.
func (s *muxServer) applySecrets(secrets, previous map[string]string) {
	if s.applied && maps.Equal(secrets, s.lastSecrets) && maps.Equal(previous, s.lastPrevious) {
		return
	}
	s.secrets.apply(secrets, previous)
	s.lastSecrets, s.lastPrevious, s.applied = maps.Clone(secrets), maps.Clone(previous), true
}

// errMarshal marks a frame that could not be encoded.
var errMarshal = errors.New("drift: frame encoding")

// write sends one frame as one Write. An encoding failure is returned and
// nothing is written; a write failure means the slice has gone, and the
// reader finds out from stdin.
func (s *muxServer) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %w", errMarshal, err)
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.out.Write(b)
	return err
}
