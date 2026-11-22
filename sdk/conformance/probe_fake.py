#!/usr/bin/env python3
"""probe_fake.py - a fake language program for probe_check_test.py.

It answers the start check the way the programs under sdk/conformance do for each
language: with DRIFT_INVOKE=probe1 it writes the hello line to stdout and exits 0
without reading a request, and without the check it reads its request and, as its
handler, writes the marker to stderr and exits 97.

    python3 probe_fake.py [--defect NAME] <entry> <marker>

With no defect it is correct. Each defect breaks exactly one rule, so the test can
show that the controller fails that rule and nothing else:

    no-hello             exits 0 and says nothing, as an SDK that reads its request first
    decode-error         fails on the empty stdin with status 1
    calls-handler        reaches the handler, then says hello and exits 0
    spaced               says hello with spaces in it
    no-newline           says hello without the newline
    extra-line           follows the hello with another line
    wrong-version        says hello at version 2
    hello-on-stderr      says hello on stderr instead of stdout
    exit-nonzero         says hello and exits 3
    hangs                says hello and never exits
    only-run             answers the entry run, and nothing else
    answers-everything   answers the check even when DRIFT_INVOKE is not set
    never-reaches-handler  exits 0 without the check, whatever it is asked
"""

import argparse
import os
import sys
import time

DEFECTS = (
    "none",
    "no-hello",
    "decode-error",
    "calls-handler",
    "spaced",
    "no-newline",
    "extra-line",
    "wrong-version",
    "hello-on-stderr",
    "exit-nonzero",
    "hangs",
    "only-run",
    "answers-everything",
    "never-reaches-handler",
)

HELLO = '{"hello":{"proto":"drift-probe","v":1}}'


def say(stream, text):
    stream.write(text)
    stream.flush()


def answer(defect, marker):
    """The check is asked: say hello, the way the defect says to, and leave."""
    line = HELLO + "\n"
    if defect == "spaced":
        line = '{"hello": {"proto": "drift-probe", "v": 1}}\n'
    elif defect == "no-newline":
        line = HELLO
    elif defect == "wrong-version":
        line = HELLO.replace('"v":1', '"v":2') + "\n"
    elif defect == "extra-line":
        line = HELLO + "\n" + '{"status":200}\n'
    if defect == "calls-handler":
        say(sys.stderr, marker + "\n")
    say(sys.stderr if defect == "hello-on-stderr" else sys.stdout, line)
    if defect == "hangs":
        time.sleep(60)
    os._exit(3 if defect == "exit-nonzero" else 0)


def serve_a_request(defect, marker):
    """What an SDK does when it is not answering the check: it reads its request.

    A check leaves it nothing to read, and what it does with that is what the
    defects no-hello and decode-error name. A request reaches the handler, which
    writes the marker and exits 97, unless the defect is never-reaches-handler.
    """
    request = sys.stdin.read()
    if not request:
        if defect == "decode-error":
            say(sys.stderr, "decode error: Expecting value: line 1 column 1 (char 0)\n")
            os._exit(1)
        os._exit(0)
    if defect == "never-reaches-handler":
        os._exit(0)
    say(sys.stderr, marker + "\n")
    os._exit(97)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--defect", default="none", choices=DEFECTS)
    ap.add_argument("entry")
    ap.add_argument("marker")
    args = ap.parse_args()
    defect = args.defect

    asked = os.environ.get("DRIFT_INVOKE") == "probe1" or defect == "answers-everything"
    answers = defect not in ("no-hello", "decode-error") and not (defect == "only-run" and args.entry != "run")
    if asked and answers:
        answer(defect, args.marker)
    serve_a_request(defect, args.marker)


if __name__ == "__main__":
    main()
