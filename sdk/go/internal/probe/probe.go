// Package probe is the SDK's side of the slice's start check, held once for the
// two packages that start a process: drift (Run, RunSSE, RunWS) and genmain
// (Serve).
//
// Before a deploy replaces the function that serves, the slice starts the new
// version once with DRIFT_INVOKE=probe<N> and stdin closed. It wants one hello
// line on the protocol stream and exit status 0, with the handler never called.
// The platform owns the contract (src/slice/src/atomic/protocol.rs in
// cloud/platform, "The start check"), and this package is this SDK's copy of its
// literals. The number N is drift.ProbeProtocol; it is a parameter here so that
// it has one home.
package probe

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// Requested reports whether the slice started this process to check that it
// starts, at the given version of the check.
func Requested(version int) bool {
	return os.Getenv("DRIFT_INVOKE") == "probe"+strconv.Itoa(version)
}

// Hello is the line the check reads for the given version, newline included.
func Hello(version int) string {
	return fmt.Sprintf(`{"hello":{"proto":"drift-probe","v":%d}}`+"\n", version)
}

// Answer writes the hello for the given version to w, in one write, and ends the
// process with status 0. A hello that cannot be written ends it with status 1
// and says why on stderr, which is what the slice reports a failed check from.
func Answer(w io.Writer, version int) {
	if _, err := io.WriteString(w, Hello(version)); err != nil {
		fmt.Fprintf(os.Stderr, "drift: the start check's hello could not be written: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
