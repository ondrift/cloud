// Package genmain is imported only by the main the drift CLI generates.
// It is not API: no compatibility promise to anyone except the templates of
// released drift CLIs, which a test in this package compiles against it. Do not
// import it.
//
// One process answers the calls the slice sends it, in one of three modes
// chosen once at start:
//
//   - DRIFT_INVOKE=mux1: the concurrent loop. Id-tagged frames on stdin and
//     the protocol stream, up to DRIFT_MUX_SLOTS calls at once, each on its own
//     goroutine with its own context (mux.go).
//   - DRIFT_RUNTIME set: the sequential loop, one envelope in and one response
//     line out at a time, as the CLI's native templates have always served a
//     warm worker and a one-shot spawn (legacy.go).
//   - neither: a local HTTP server for development (local.go).
//
// In both slice modes the protocol owns the process's real stdout, and fd 1 is
// pointed at stderr before any handler runs (stdio_*.go). A handler that
// prints therefore writes to the function's log, never into a frame or a
// response line.
package genmain

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	drift "github.com/ondrift/cloud/sdk/go"
)

// maxSlots is the most calls one process serves at once: a Function's
// `concurrency` maximum.
const maxSlots = 64

// defaultBudget is how long a call may run when its envelope carries no
// runtime budget, the slice's own default.
const defaultBudget = 30 * time.Second

// runtimeBudgetHeader carries the call's runtime budget in whole seconds, set
// by the edge from the tier's function timeout.
const runtimeBudgetHeader = "X-User-Runtime"

// handlerFunc is the shape the generated main hands to Serve.
type handlerFunc = func(ctx context.Context, req drift.Request) drift.Response

// Serve answers every call the slice sends this process: concurrently, up to
// the slots the slice asks for, when it runs the process as a concurrent Function;
// one envelope at a time otherwise; and over local HTTP outside a slice.
//
// It returns when the slice closes stdin, after the calls in flight have been
// answered. In local mode it serves until the process is stopped.
func Serve(handler func(ctx context.Context, req drift.Request) drift.Response) {
	switch {
	case os.Getenv("DRIFT_INVOKE") == "mux1":
		serveMux(os.Stdin, protocolOut(), slotsFromEnv(os.Getenv("DRIFT_MUX_SLOTS")), handler)
	case os.Getenv("DRIFT_RUNTIME") != "":
		serveLegacy(os.Stdin, protocolOut(), handler)
	default:
		serveLocal(handler)
	}
}

// protocolOut is the stream the slice reads frames or response lines from:
// the process's real stdout, kept on its own descriptor, with fd 1 pointed at
// stderr. When the descriptors cannot be rearranged it is os.Stdout, as it
// always was, and the reason goes to the log.
func protocolOut() *os.File {
	f, err := separateProtocolStream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "drift: handler output shares the protocol stream: %v\n", err)
		return os.Stdout
	}
	return f
}

// slotsFromEnv reads DRIFT_MUX_SLOTS: 1 to 64, and 1 for anything else.
func slotsFromEnv(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 1
	}
	if n > maxSlots {
		return maxSlots
	}
	return n
}

// runtimeBudget is the call's runtime budget from its X-User-Runtime header,
// matched in any letter case, or the default when it is absent, unparseable or
// not positive. The slice reads the same header the same way.
func runtimeBudget(headers map[string]string) time.Duration {
	for k, v := range headers {
		if !strings.EqualFold(k, runtimeBudgetHeader) {
			continue
		}
		secs, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil || secs == 0 {
			return defaultBudget
		}
		return time.Duration(secs) * time.Second
	}
	return defaultBudget
}

// secretState applies an envelope's secrets to the process environment, where
// the SDK's secret accessor reads them, with the native templates' rule: every
// carried value is set, and a name the envelope no longer carries is removed
// if this process set it.
type secretState struct {
	injected map[string]bool
}

func newSecretState() *secretState {
	return &secretState{injected: map[string]bool{}}
}

func (s *secretState) apply(secrets, previous map[string]string) {
	for k := range s.injected {
		if _, still := secrets[k]; !still {
			_ = os.Unsetenv("DRIFT_SECRET_" + strings.ToUpper(k))
			delete(s.injected, k)
		}
		if _, still := previous[k]; !still {
			_ = os.Unsetenv("DRIFT_SECRET_" + strings.ToUpper(k) + "_PREVIOUS")
		}
	}
	for k, v := range secrets {
		_ = os.Setenv("DRIFT_SECRET_"+strings.ToUpper(k), v)
		s.injected[k] = true
	}
	for k, v := range previous {
		_ = os.Setenv("DRIFT_SECRET_"+strings.ToUpper(k)+"_PREVIOUS", v)
		s.injected[k] = true
	}
}

// envelopeExtras are the members of a slice envelope beyond drift.Request.
type envelopeExtras struct {
	Tmpdir   string            `json:"tmpdir"`
	Secrets  map[string]string `json:"secrets"`
	Previous map[string]string `json:"secrets_previous"`
}
