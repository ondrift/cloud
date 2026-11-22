// Command probe is the Go program the start check cases run
// (sdk/conformance/probe_check.py).
//
// It hands a handler that must not run to the SDK's own entry points: genmain.Serve,
// which every Go function the CLI builds is served through, for the entry "run",
// and drift.RunSSE and drift.RunWS for "run_sse" and "run_ws". Its two arguments
// are the entry's name and the marker the handler writes to stderr, before it
// exits 97, if it is ever reached.
package main

import (
	"context"
	"fmt"
	"os"

	drift "github.com/ondrift/cloud/sdk/go"
	"github.com/ondrift/cloud/sdk/go/genmain"
)

func main() {
	if len(os.Args) != 3 {
		usage()
	}
	entry, marker := os.Args[1], os.Args[2]
	reached := func() {
		fmt.Fprintln(os.Stderr, marker)
		os.Exit(97)
	}
	switch entry {
	case "run":
		genmain.Serve(func(context.Context, drift.Request) drift.Response {
			reached()
			return drift.Response{}
		})
	case "run_sse":
		drift.RunSSE(func(drift.Request, drift.Emitter) { reached() })
	case "run_ws":
		drift.RunWS(func(drift.Request, drift.Conn) { reached() })
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: probe <run|run_sse|run_ws> <marker>")
	os.Exit(2)
}
