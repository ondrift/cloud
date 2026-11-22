"""A process started to be checked says hello and exits, and never reaches the handler.

    python3 python/probe_test.py

Before a deploy replaces the function that serves, the slice starts the new
version once: DRIFT_INVOKE=probe1, stdin closed. It wants one line on stdout,
{"hello":{"proto":"drift-probe","v":1}}, and exit status 0, with the handler
never called, because a handler writes to databases and sends mail. The platform
owns that contract; this holds the SDK's side of it.

Answering means leaving the process, so each entry point is started as a process
of its own. The handler in that process fails the test if it is called: it
writes HANDLER_CALLED to stderr and exits 97. Each entry also runs without the
check, with a request on stdin, where the handler IS reached; without that the
absence of the marker could mean the guard cannot fire.
"""

import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

# The line the platform's start check reads, as the SDK repeats it.
HELLO = '{"hello":{"proto":"drift-probe","v":1}}\n'
CALLED = "HANDLER_CALLED"

# The slice allows a check ten seconds from spawn.
WITHIN = 10

# Every entry point the SDK has that reads a request.
ENTRIES = ("run", "run_sse", "run_ws")

# What an entry reads when it is not being checked: a request, on one line.
REQUEST = '{"path":"/","headers":{},"query":"","body":null}\n'

PROGRAM = """
import os, sys
sys.path.insert(0, %r)
import drift

def handler(*_):
    sys.stderr.write("%s\\n")
    sys.stderr.flush()
    os._exit(97)

getattr(drift, sys.argv[1])(handler)
""" % (HERE, CALLED)

failures = []


def check(what, ok, detail=""):
    if ok:
        print(f"  ✓ {what}")
        return
    print(f"  ✗ {what}" + (f": {detail}" if detail else ""))
    failures.append(what)


def start(entry, invoke, request):
    """The entry point as its own process, as the slice starts a function.

    A request of None is a closed stdin (the null device, as the slice gives a
    check); otherwise it is written to stdin, which is then closed. Returns the
    finished process, or None when it was still running after WITHIN seconds.
    """
    env = {"DRIFT_RUNTIME": "1"}
    if invoke is not None:
        env["DRIFT_INVOKE"] = invoke
    try:
        return subprocess.run(
            [sys.executable, "-c", PROGRAM, entry],
            stdin=subprocess.DEVNULL if request is None else None,
            input=request,
            capture_output=True,
            text=True,
            env=env,
            timeout=WITHIN,
        )
    except subprocess.TimeoutExpired:
        return None


def tail(text, limit=160):
    """The last line of what a process wrote to stderr: where a failure says why."""
    lines = text.strip().splitlines()
    last = lines[-1] if lines else ""
    return last if len(last) <= limit else "..." + last[-limit:]


def main():
    import drift

    print("Python SDK: the start check")

    check(
        "PROBE_PROTOCOL is 1",
        getattr(drift, "PROBE_PROTOCOL", None) == 1,
        repr(getattr(drift, "PROBE_PROTOCOL", "not defined")),
    )

    for entry in ENTRIES:
        done = start(entry, "probe1", None)
        if done is None:
            check(f"{entry} answers within {WITHIN}s", False, "it was still running")
            continue
        detail = f"stdout {done.stdout!r}, status {done.returncode}, stderr {tail(done.stderr)!r}"
        check(f"{entry} writes the hello line and nothing else", done.stdout == HELLO, detail)
        check(f"{entry} exits 0", done.returncode == 0, detail)
        check(f"{entry} does not reach the handler", CALLED not in done.stderr, detail)

    # THE CONTROLS. Without the check an entry reads its request and calls the
    # handler, so the marker above can appear, and an SDK that answered every
    # start as a check would show here.
    for invoke, what in ((None, "without the check"), ("probe2", "under another version of the check")):
        for entry in ENTRIES:
            done = start(entry, invoke, REQUEST)
            if done is None:
                check(f"{entry} {what} reaches the handler", False, f"it was still running after {WITHIN}s")
                continue
            check(
                f"{entry} {what} reaches the handler and says no hello",
                CALLED in done.stderr and done.returncode == 97 and "drift-probe" not in done.stdout,
                f"stdout {done.stdout!r}, status {done.returncode}, stderr {tail(done.stderr)!r}",
            )

    if failures:
        print(f"\n❌ {len(failures)} assertion(s) failed")
        sys.exit(1)
    print("\n✅ every case passed")


if __name__ == "__main__":
    main()
