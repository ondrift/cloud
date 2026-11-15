#!/usr/bin/env python3
"""loop_check.py - hold one language's program to the loop a slice's warm worker speaks.

    python3 loop_check.py <language> [--expected FILE] [--reply-timeout SECONDS] -- <program> [args...]

The program is a small handler served through the SDK's own entry point (Go's
genmain.Serve, Rust's drift_sdk::run, Ruby's Drift.run, PHP's \\Drift\\run). The
controller starts it the way a slice does, with DRIFT_RUNTIME=1 and nothing else
in its environment, and talks to it over a pipe. The cases are in expected.json
under "$loop": each names the envelopes to send, what to observe and what a
looping SDK must show. check.py does the same for the primitives, from a
driver's printed output; this reads a conversation instead, so it cannot be fed
by a driver that prints what it is expected to print.

The protocol, which is warmpool.rs and protocol.rs in cloud/platform:

  - an envelope is one JSON object on one line; the worker answers each with
    exactly one line, and the slice sends the next envelope only after reading it
  - a trigger's one-shot spawn sends ONE envelope with no trailing newline and
    closes stdin: one reply, exit status 0
  - end of input ends the process with status 0
  - a handler that raises or panics ends the process, with no further output

A language is in one of three states in "$loop":

  not applicable   its calls never reach the SDK's run entry: nothing is run
  pending          its loop is not built: the cases run and failing is reported,
                   not failed; passing FAILS, because the entry must then come
                   off the list
  neither          every case must pass

Exit status: 0 the language is where the lists say it is, 1 it is not, 2 the
controller could not be run as asked.
"""

import json
import os
import select
import shutil
import signal
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))

# How long a worker has to end after its input does, or after a handler fails:
# the slice does not wait longer for a retiring worker.
EXIT_WITHIN = 1.0

# How long a program has to answer an envelope. The first answer includes the
# language runtime starting, so this is generous; a looping program answers in
# milliseconds and only a program that never answers pays all of it.
DEFAULT_REPLY_TIMEOUT = 5.0


class Session:
    """One program, started as a slice starts a worker, and the pipes to it."""

    def __init__(self, argv):
        # Found on the controller's own PATH, because the program's is empty.
        exe = shutil.which(argv[0]) or argv[0]
        self.proc = subprocess.Popen(
            [exe] + argv[1:],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env={"DRIFT_RUNTIME": "1"},
            bufsize=0,
            start_new_session=True,
        )
        self.pid = self.proc.pid
        self._out = bytearray()
        self._eof = False
        self._err = bytearray()
        self._drain = threading.Thread(target=self._drain_stderr, args=(self.proc.stderr.fileno(),), daemon=True)
        self._drain.start()

    def _drain_stderr(self, fd):
        """Collect what the program writes to stderr, so a full pipe never blocks it."""
        try:
            while True:
                chunk = os.read(fd, 65536)
                if not chunk:
                    return
                self._err += chunk
        except OSError:
            return

    def send(self, data):
        """Write the bytes; False when the process is no longer reading."""
        try:
            view = memoryview(data)
            while view:
                view = view[os.write(self.proc.stdin.fileno(), view):]
            return True
        except OSError:
            return False

    def close_stdin(self):
        try:
            self.proc.stdin.close()
        except OSError:
            pass

    def _fill(self, deadline):
        """Read once, waiting until the deadline; False when nothing came."""
        left = deadline - time.monotonic()
        if left <= 0:
            return False
        ready, _, _ = select.select([self.proc.stdout.fileno()], [], [], left)
        if not ready:
            return False
        chunk = os.read(self.proc.stdout.fileno(), 65536)
        if chunk:
            self._out += chunk
        else:
            self._eof = True
        return True

    def read_line(self, timeout):
        """The next complete line: ("line", bytes), ("eof", leftover) or ("timeout", b"")."""
        deadline = time.monotonic() + timeout
        while True:
            end = self._out.find(b"\n")
            if end >= 0:
                line = bytes(self._out[: end + 1])
                del self._out[: end + 1]
                return "line", line
            if self._eof:
                leftover = bytes(self._out)
                self._out.clear()
                return "eof", leftover
            if not self._fill(deadline):
                return "timeout", b""

    def read_until_eof(self, timeout):
        """Everything the program writes until it closes its output, and whether it did."""
        deadline = time.monotonic() + timeout
        while not self._eof:
            if not self._fill(deadline):
                break
        data = bytes(self._out)
        self._out.clear()
        return data, self._eof

    def wait(self, timeout):
        """The exit status, or None when the process is still running."""
        try:
            return self.proc.wait(timeout)
        except subprocess.TimeoutExpired:
            return None

    def stderr_text(self):
        self._drain.join(1.0)
        return bytes(self._err).decode("utf-8", "replace")

    def stop(self):
        # Only a process that has not been reaped: a reaped pid may belong to
        # someone else by now.
        if self.proc.poll() is None:
            try:
                os.killpg(self.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
        self.proc.wait()
        self._drain.join(2.0)
        for pipe in (self.proc.stdin, self.proc.stdout, self.proc.stderr):
            try:
                pipe.close()
            except OSError:
                pass


def exit_text(status, waited):
    if status is None:
        return "still running after %gs" % waited
    if status < 0:
        return "killed by signal %d" % -status
    return "status %d" % status


def envelope(call, number):
    """The envelope a slice would send for this call: members it has nothing to say for are absent."""
    body = {}
    if "print" in call:
        body["print"] = call["print"]
    if call.get("fail"):
        body["fail"] = True
    out = {"path": "/loop/%d" % number, "headers": {}, "query": "", "body": body}
    for member in ("tmpdir", "secrets", "secrets_previous"):
        if member in call:
            out[member] = call[member]
    return json.dumps(out).encode()


def parse_reply(line):
    """The reply the line holds, or why it is not one."""
    text = line.decode("utf-8", "replace").strip()
    try:
        reply = json.loads(text)
    except ValueError:
        return None, "the line on the response stream is not JSON: %r" % text[:80]
    if not isinstance(reply, dict) or reply.get("status") != 200 or not isinstance(reply.get("payload"), dict):
        return None, "the line on the response stream is not the handler's reply: %r" % text[:80]
    return reply, None


def converse(session, calls, reply_timeout):
    """Send the envelopes one at a time, reading each reply first, as a slice does.

    Returns the replies and the first thing that went wrong, if anything did.
    """
    replies = []
    for number, call in enumerate(calls, 1):
        ended = "envelope %d: the process ended (%s) instead of answering it"
        if not session.send(envelope(call, number) + b"\n"):
            status = session.wait(EXIT_WITHIN)
            return replies, ended % (number, exit_text(status, EXIT_WITHIN))
        kind, data = session.read_line(reply_timeout)
        if kind == "timeout":
            return replies, "envelope %d: no reply line within %gs" % (number, reply_timeout)
        if kind == "eof":
            status = session.wait(EXIT_WITHIN)
            return replies, ended % (number, exit_text(status, EXIT_WITHIN))
        reply, problem = parse_reply(data)
        if problem:
            return replies, "envelope %d: %s" % (number, problem)
        replies.append(reply)
    return replies, None


# -- What a case observes in a conversation ---------------------------------

PRINTED = "loop-conformance-printed-line"


def payloads(replies):
    return [r["payload"] for r in replies]


def reports_rss(reply):
    rss = reply.get("rss_bytes")
    return isinstance(rss, int) and not isinstance(rss, bool) and rss > 0


# What a serve case reads out of its replies, by the name the case gives.
OBSERVERS = {
    "replies": lambda s, replies: len(replies),
    "own_pid": lambda s, replies: [p.get("pid") == s.pid for p in payloads(replies)],
    "tmpdir_and_home": lambda s, replies: [[p.get("tmpdir"), p.get("home")] for p in payloads(replies)],
    "secrets": lambda s, replies: [p.get("secrets") for p in payloads(replies)],
    "rss_bytes": lambda s, replies: [reports_rss(r) for r in replies],
}

# Those, and the two that read the process rather than its replies.
SERVE_OBSERVERS = set(OBSERVERS) | {"exit_after_eof", "printed_line"}


def serve(argv, case, reply_timeout):
    """Several calls to one process, then end of input."""
    session = Session(argv)
    try:
        replies, problem = converse(session, case["calls"], reply_timeout)
        if problem:
            return problem
        session.close_stdin()
        status = session.wait(EXIT_WITHIN)
        observe = case["observe"]
        if observe == "exit_after_eof":
            return exit_text(status, EXIT_WITHIN)
        if observe == "printed_line":
            return {"replies": len(replies), "on_stderr": PRINTED in session.stderr_text()}
        return OBSERVERS[observe](session, replies)
    finally:
        session.stop()


def one_shot(argv, case, reply_timeout):
    """A trigger's spawn: one envelope, no trailing newline, then stdin closed at once."""
    session = Session(argv)
    try:
        session.send(envelope(case["calls"][0], 1))
        session.close_stdin()
        data, closed = session.read_until_eof(reply_timeout)
        if not closed:
            return "no reply within %gs of the input closing" % reply_timeout
        lines = [line for line in data.splitlines() if line.strip()]
        for line in lines:
            _, problem = parse_reply(line)
            if problem:
                return problem
        return {"replies": len(lines), "exit": exit_text(session.wait(EXIT_WITHIN), EXIT_WITHIN)}
    finally:
        session.stop()


def failure(argv, case, reply_timeout):
    """Calls that are answered, then one whose handler fails."""
    calls = case["calls"]
    session = Session(argv)
    try:
        replies, problem = converse(session, calls[:-1], reply_timeout)
        if problem:
            return problem
        session.send(envelope(calls[-1], len(calls)) + b"\n")
        started = time.monotonic()
        written, _ = session.read_until_eof(EXIT_WITHIN)
        status = session.wait(max(0.0, EXIT_WITHIN - (time.monotonic() - started)))
        if status is None:
            ended = "still running after %gs" % EXIT_WITHIN
        else:
            ended = "status 0" if status == 0 else "non-zero status"
        return {
            "replies_before_the_failure": len(replies),
            "output_after_the_failure": written.decode("utf-8", "replace"),
            "exit": ended,
        }
    finally:
        session.stop()


SESSIONS = {"serve": serve, "one_shot": one_shot, "failure": failure}


def run_case(argv, case, reply_timeout):
    try:
        return SESSIONS[case.get("session", "serve")](argv, case, reply_timeout)
    except OSError as e:
        return "the program could not be run: %s" % e


def short(value, limit=110):
    text = json.dumps(value)
    return text if len(text) <= limit else text[: limit - 3] + "..."


def run_cases(argv, cases, reply_timeout):
    """Every case, each against its own process, all at once. Case name to what was got."""
    with ThreadPoolExecutor(max_workers=max(1, len(cases))) as pool:
        futures = {name: pool.submit(run_case, argv, case, reply_timeout) for name, case in cases.items()}
        return {name: future.result() for name, future in futures.items()}


def usage(message):
    print("  loop_check.py: %s" % message)
    print("  usage: loop_check.py <language> [--expected FILE] [--reply-timeout SECONDS] -- <program> [args...]")
    return 2


def main(argv):
    if "--" in argv:
        split = argv.index("--")
        options, program = argv[:split], argv[split + 1 :]
    else:
        options, program = argv, []
    expected_path = os.path.join(HERE, "expected.json")
    reply_timeout = DEFAULT_REPLY_TIMEOUT
    words = []
    it = iter(options)
    for word in it:
        if word == "--expected":
            expected_path = next(it, "")
        elif word == "--reply-timeout":
            try:
                reply_timeout = float(next(it, ""))
            except ValueError:
                return usage("--reply-timeout needs a number of seconds")
        else:
            words.append(word)
    if len(words) != 1:
        return usage("name one language")
    language = words[0]

    with open(expected_path) as f:
        expected = json.load(f)
    loop = expected.get("$loop")
    if not isinstance(loop, dict) or not loop.get("cases"):
        return usage("%s holds no $loop cases" % expected_path)
    not_applicable = loop.get("not_applicable", {})
    pending = loop.get("pending", {})
    if language in not_applicable and language in pending:
        return usage("%s is both not applicable and pending in $loop" % language)

    if language in not_applicable:
        print("  %s loop: not applicable: %s" % (language, not_applicable[language]))
        return 0
    if not program:
        return usage("%s has loop cases and no program was given after --" % language)

    for name, case in loop["cases"].items():
        if case.get("session", "serve") not in SESSIONS:
            return usage("%s names the session %r, which is not one of %s" % (name, case.get("session"), sorted(SESSIONS)))
        if case.get("session", "serve") == "serve" and case.get("observe") not in SERVE_OBSERVERS:
            return usage("%s observes %r, which is not one of %s" % (name, case.get("observe"), sorted(SERVE_OBSERVERS)))
    cases = {
        name: case
        for name, case in loop["cases"].items()
        if sys.platform.startswith(case.get("only_on", ""))
    }
    left_out = len(loop["cases"]) - len(cases)
    got = run_cases(program, cases, reply_timeout)
    failed = [name for name, case in cases.items() if got[name] != case["expect"]]
    passed = len(cases) - len(failed)
    note = " (%d asserted on another platform only)" % left_out if left_out else ""

    if language in pending:
        if not failed:
            print("  %s loop: %d/%d cases pass, and %s is on the pending list" % (language, passed, len(cases), language))
            print('    Remove "%s" from "$loop"."pending" in sdk/conformance/expected.json.' % language)
            print("    The list holds languages whose loop is not built yet; one whose loop passes is held to every case.")
            return 1
        print("  %s loop: PENDING, %d/%d cases pass%s. %s" % (language, passed, len(cases), note, pending[language]))
        for name in failed:
            print("    ✗ %s: %s" % (name, short(got[name])))
        return 0

    if not failed:
        print("  %s loop: %d/%d cases pass%s" % (language, passed, len(cases), note))
        return 0
    print("  %s loop: %d/%d pass, %d FAIL%s" % (language, passed, len(cases), len(failed), note))
    for name in failed:
        print("    ✗ %s" % name)
        print("        want: %s" % short(cases[name]["expect"]))
        print("        got:  %s" % short(got[name]))
        print("        why this matters: %s" % cases[name]["why"])
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
