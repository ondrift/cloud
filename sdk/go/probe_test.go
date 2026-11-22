package drift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The start check. Before a deploy replaces the function that serves, the slice
// starts the new version once with DRIFT_INVOKE=probe1 and stdin closed. It
// wants one line on stdout and exit status 0, and the handler never called. The
// platform owns the contract (protocol.rs, "The start check"); these literals
// are this SDK's own copy of it.
//
// Answering means leaving the process, so each entry point runs as a process of
// its own: the test binary, started again with TestStartCheckEntry selected.
const (
	startCheckHello = `{"hello":{"proto":"drift-probe","v":1}}` + "\n"
	handlerCalled   = "HANDLER_CALLED"
)

// TestStartCheckEntry is the program the tests below start, and no test of its
// own: when DRIFT_PROBE_TEST_ENTRY names an entry point it hands that entry a
// handler that ends the process with status 97 and a marker on stderr, and does
// nothing else. Run any other way it returns at once.
func TestStartCheckEntry(t *testing.T) {
	entry := os.Getenv("DRIFT_PROBE_TEST_ENTRY")
	if entry == "" {
		return
	}
	called := func() {
		fmt.Fprintln(os.Stderr, handlerCalled)
		os.Exit(97)
	}
	switch entry {
	case "Run":
		Run(func(Request) Response { called(); return Response{} })
	case "RunSSE":
		RunSSE(func(Request, Emitter) { called() })
	case "RunWS":
		RunWS(func(Request, Conn) { called() })
	}
	// An entry that came back has answered nothing. Leaving here keeps the test
	// binary's own report off stdout, which belongs to the entry.
	os.Exit(0)
}

// startedEntry starts one entry point as a process, as the slice starts a
// function: DRIFT_RUNTIME set, under env, with stdin as given, where nil is the
// null device the slice gives a check. It returns what the process wrote and
// its exit status, and fails the test when the process is still running after
// the ten seconds the slice allows a check.
func startedEntry(t *testing.T, entry string, env []string, stdin io.Reader) (stdout, stderr string, exit int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestStartCheckEntry$")
	cmd.Env = append([]string{"DRIFT_PROBE_TEST_ENTRY=" + entry, "DRIFT_RUNTIME=1"}, env...)
	cmd.Stdin = stdin
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s was still running after 10 s; stdout %q, stderr %q", entry, out.String(), errs.String())
	}
	var ended *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ended):
		exit = ended.ExitCode()
	default:
		t.Fatalf("%s: %v", entry, err)
	}
	return out.String(), errs.String(), exit
}

// Every entry that reads a request answers a start check: the hello, once, on
// stdout, then status 0, with the handler left alone. Run is the entry a
// hand-built main calls, and RunSSE and RunWS are the streaming ones.
func TestEveryEntryAnswersTheStartCheck(t *testing.T) {
	for _, entry := range []string{"Run", "RunSSE", "RunWS"} {
		t.Run(entry, func(t *testing.T) {
			stdout, stderr, exit := startedEntry(t, entry, []string{"DRIFT_INVOKE=probe1"}, nil)
			if stdout != startCheckHello {
				t.Errorf("stdout = %q, want exactly %q", stdout, startCheckHello)
			}
			if exit != 0 {
				t.Errorf("exit status %d, want 0; stderr: %q", exit, stderr)
			}
			if strings.Contains(stderr, handlerCalled) {
				t.Errorf("the handler was called: %q", stderr)
			}
		})
	}
}

// THE CONTROLS. Without the start check an entry reads its request and calls the
// handler, which ends the process with status 97, so the marker above can
// appear. DRIFT_INVOKE unset and another version of the check are both that,
// and neither is answered as a check.
func TestEveryEntryStillReachesTheHandlerWithoutTheStartCheck(t *testing.T) {
	request := `{"path":"/","headers":{},"query":"","body":null}` + "\n"
	for name, env := range map[string][]string{"unset": nil, "another version": {"DRIFT_INVOKE=probe2"}} {
		for _, entry := range []string{"Run", "RunSSE", "RunWS"} {
			t.Run(name+"/"+entry, func(t *testing.T) {
				stdout, stderr, exit := startedEntry(t, entry, env, strings.NewReader(request))
				if exit != 97 || !strings.Contains(stderr, handlerCalled) || strings.Contains(stdout, "drift-probe") {
					t.Errorf("exit %d, stdout %q, stderr %q, want the handler reached (status 97, marker on stderr) and no hello", exit, stdout, stderr)
				}
			})
		}
	}
}
