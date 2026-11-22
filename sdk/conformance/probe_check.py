#!/usr/bin/env python3
"""probe_check.py - hold one language's program to the start check a slice runs before a deploy.

    python3 probe_check.py <language> [--expected FILE] [--deadline SECONDS] -- <program> [args...]

The program hands a handler that must not run to the SDK's own entry points (Go's
genmain.Serve, drift_sdk::run, Drift.run, \\Drift\\run, drift.run and the streaming
entries beside them). The controller starts it the way the slice starts a new
function version to check it: DRIFT_RUNTIME=1 and DRIFT_INVOKE=probe1 in an
otherwise empty environment, stdin closed, and the entry's name and a marker as
the program's last two arguments. The marker is what the handler writes to stderr,
before it exits 97, if it is ever reached. The cases are in expected.json under
"$probe": each names the entry, the environment and the stdin, and what a process
must show. check.py does the same for the primitives, from a driver's printed
output; this starts a process and reads what it did, so a driver cannot be made to
print what is expected.

The contract is protocol.rs in cloud/platform ("The start check"): one line on
stdout, {"hello":{"proto":"drift-probe","v":1}}, then exit status 0, inside
ten seconds of the spawn, with the handler never called. Every language must pass
every case. A language with no program is an error, not a skip.

Exit status: 0 every case passes, 1 one does not, 2 the controller could not be
run as asked.
"""

import json
import os
import shutil
import signal
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))

# How long a check has from spawn to exit: PROBE_DEADLINE in the platform's
# protocol.rs. Past it the slice kills the process and refuses the deploy.
DEFAULT_DEADLINE = 10.0

# What a program's handler writes to stderr if it is reached. The controller owns
# it and hands it to the program, so no program has a copy to get wrong.
HANDLER_MARK = "probe-conformance-handler-called"

# What a case with stdin "request" sends: one JSON object on one line, which every
# entry reads as its request (the first line of it, for run_ws).
REQUEST = b'{"path":"/probe","headers":{},"query":"","body":null}\n'

ENTRIES = ("run", "run_sse", "run_ws")
STDINS = ("closed", "request")


def start(argv, case, deadline):
    """Start the program for one case and say what it did.

    Returns the stdout it wrote in full, how it ended (its exit status, "killed by
    signal N", or "still running after Ns" when the deadline passed and it was
    killed) and whether its handler was reached.
    """
    exe = shutil.which(argv[0]) or argv[0]
    env = {"DRIFT_RUNTIME": "1"}
    if case.get("invoke") is not None:
        env["DRIFT_INVOKE"] = case["invoke"]
    sends_request = case.get("stdin", "closed") == "request"
    proc = subprocess.Popen(
        [exe] + argv[1:] + [case["entry"], HANDLER_MARK],
        stdin=subprocess.PIPE if sends_request else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        start_new_session=True,
    )
    timed_out = False
    try:
        out, err = proc.communicate(REQUEST if sends_request else None, timeout=deadline)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        out, err = proc.communicate()
    if timed_out:
        ended = "still running after %gs" % deadline
    elif proc.returncode < 0:
        ended = "killed by signal %d" % -proc.returncode
    else:
        ended = proc.returncode
    return {
        "stdout": out.decode("utf-8", "replace"),
        "exit": ended,
        "handler_called": HANDLER_MARK in err.decode("utf-8", "replace"),
    }


def run_case(argv, case, deadline):
    try:
        return start(argv, case, deadline)
    except OSError as e:
        return "the program could not be run: %s" % e


def run_cases(argv, cases, deadline):
    """Every case, each against its own process, all at once. Case name to what was got."""
    with ThreadPoolExecutor(max_workers=max(1, len(cases))) as pool:
        futures = {name: pool.submit(run_case, argv, case, deadline) for name, case in cases.items()}
        return {name: future.result() for name, future in futures.items()}


def short(value, limit=150):
    text = json.dumps(value)
    return text if len(text) <= limit else text[: limit - 3] + "..."


def usage(message):
    print("  probe_check.py: %s" % message)
    print("  usage: probe_check.py <language> [--expected FILE] [--deadline SECONDS] -- <program> [args...]")
    return 2


def main(argv):
    if "--" in argv:
        split = argv.index("--")
        options, program = argv[:split], argv[split + 1 :]
    else:
        options, program = argv, []
    expected_path = os.path.join(HERE, "expected.json")
    deadline = DEFAULT_DEADLINE
    words = []
    it = iter(options)
    for word in it:
        if word == "--expected":
            expected_path = next(it, "")
        elif word == "--deadline":
            try:
                deadline = float(next(it, ""))
            except ValueError:
                return usage("--deadline needs a number of seconds")
        else:
            words.append(word)
    if len(words) != 1:
        return usage("name one language")
    language = words[0]

    with open(expected_path) as f:
        expected = json.load(f)
    cases = (expected.get("$probe") or {}).get("cases")
    if not isinstance(cases, dict) or not cases:
        return usage("%s holds no $probe cases" % expected_path)
    if not program:
        return usage("%s has probe cases and no program was given after --" % language)
    for name, case in cases.items():
        if case.get("entry") not in ENTRIES:
            return usage("%s names the entry %r, which is not one of %s" % (name, case.get("entry"), list(ENTRIES)))
        if case.get("stdin", "closed") not in STDINS:
            return usage("%s names the stdin %r, which is not one of %s" % (name, case.get("stdin"), list(STDINS)))

    got = run_cases(program, cases, deadline)
    failed = [name for name, case in cases.items() if got[name] != case["expect"]]
    passed = len(cases) - len(failed)
    if not failed:
        print("  %s probe: %d/%d cases pass" % (language, passed, len(cases)))
        return 0
    print("  %s probe: %d/%d pass, %d FAIL" % (language, passed, len(cases), len(failed)))
    for name in failed:
        print("    ✗ %s" % name)
        print("        want: %s" % short(cases[name]["expect"]))
        print("        got:  %s" % short(got[name]))
        print("        why this matters: %s" % cases[name]["why"])
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
