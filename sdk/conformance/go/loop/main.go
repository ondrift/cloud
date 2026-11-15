// Command loop is the Go program the loop cases run (sdk/conformance/loop_check.py).
//
// It serves one handler through genmain.Serve, the entry point every Go
// function the CLI builds is served through, so what the cases assert is what a
// slice's warm worker gets. It re-implements no loop.
//
// The handler reports what a function can see of its call: its pid, TMPDIR and
// HOME, and every DRIFT_SECRET_ variable. Two requests change what it does: a
// body with "print" writes that text to stdout, as a function's own print does,
// and a body with "fail" panics.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	drift "github.com/ondrift/cloud/sdk/go"
	"github.com/ondrift/cloud/sdk/go/genmain"
)

// directive is what a request body can ask of the handler.
type directive struct {
	Print *string `json:"print"`
	Fail  bool    `json:"fail"`
}

// view is the part of the process a call can observe.
type view struct {
	Pid     int               `json:"pid"`
	Tmpdir  *string           `json:"tmpdir"`
	Home    *string           `json:"home"`
	Secrets map[string]string `json:"secrets"`
}

func main() {
	genmain.Serve(func(_ context.Context, req drift.Request) drift.Response {
		var ask directive
		_ = json.Unmarshal(req.Body, &ask)
		if ask.Print != nil {
			fmt.Println(*ask.Print)
		}
		if ask.Fail {
			panic("loop conformance: the handler was asked to fail")
		}
		payload, _ := json.Marshal(view{
			Pid:     os.Getpid(),
			Tmpdir:  lookup("TMPDIR"),
			Home:    lookup("HOME"),
			Secrets: secrets(),
		})
		return drift.Response{Status: 200, Message: "ok", Payload: payload}
	})
}

// lookup is an environment variable's value, nil when it is not set.
func lookup(name string) *string {
	if v, ok := os.LookupEnv(name); ok {
		return &v
	}
	return nil
}

// secrets is every DRIFT_SECRET_ variable the process holds.
func secrets() map[string]string {
	seen := map[string]string{}
	for _, kv := range os.Environ() {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "DRIFT_SECRET_") {
			seen[name] = value
		}
	}
	return seen
}
