#!/usr/bin/env python3
"""loop_fake.py - a fake language program for loop_check_test.py.

It serves the loop a slice's warm worker speaks, the way the handler programs
under sdk/conformance do for each language: one newline-delimited envelope in,
exactly one line out, TMPDIR and HOME pointed at the envelope's tmpdir, the
envelope's secrets replacing the previous call's, a handler's printed line on
stderr, the process ending at end of input with status 0, and a handler that
raises ending the process without a reply.

    python3 loop_fake.py [--defect NAME]

With no defect it is a correct loop. Each defect breaks exactly one rule, so the
test can show that the controller fails the rule and nothing else:

    one-shot            answers one envelope, then exits 0
    one-reply           answers the first envelope and swallows every later one
    needs-newline       ignores a final envelope that has no trailing newline
    tmpdir-once         points TMPDIR and HOME at the first envelope's tmpdir only
    home-untouched      points TMPDIR at the tmpdir and leaves HOME alone
    secret-not-replaced keeps the first value a secret was given
    secret-stays        never removes a secret an envelope stopped carrying
    previous-stays      never removes a _PREVIOUS variable the envelope dropped
    print-to-stdout     lets a handler's printed line onto the response stream
    no-rss              leaves rss_bytes out of every reply
    eof-hangs           keeps running after end of input
    eof-fails           exits 3 at end of input
    panic-survives      logs a handler's failure and carries on, silently
    panic-answers       answers a handler's failure with a 500 line and carries on
"""

import argparse
import contextlib
import json
import os
import resource
import sys
import time

DEFECTS = (
    "none",
    "one-shot",
    "one-reply",
    "needs-newline",
    "tmpdir-once",
    "home-untouched",
    "secret-not-replaced",
    "secret-stays",
    "previous-stays",
    "print-to-stdout",
    "no-rss",
    "eof-hangs",
    "eof-fails",
    "panic-survives",
    "panic-answers",
)


class Loop:
    def __init__(self, defect):
        self.defect = defect
        # Secret names this process set, so a later envelope can remove them.
        self.injected = set()
        self.tmpdir_applied = False

    def apply(self, envelope):
        tmpdir = envelope.get("tmpdir")
        if tmpdir and os.path.isabs(tmpdir):
            once_done = self.defect == "tmpdir-once" and self.tmpdir_applied
            if not once_done:
                os.environ["TMPDIR"] = tmpdir
                if self.defect != "home-untouched":
                    os.environ["HOME"] = tmpdir
                self.tmpdir_applied = True

        secrets = envelope.get("secrets") or {}
        previous = envelope.get("secrets_previous") or {}
        for name in list(self.injected):
            var = "DRIFT_SECRET_" + name.upper()
            if name not in secrets and self.defect != "secret-stays":
                os.environ.pop(var, None)
                self.injected.discard(name)
            if name not in previous and self.defect != "previous-stays":
                os.environ.pop(var + "_PREVIOUS", None)
        for name, value in secrets.items():
            var = "DRIFT_SECRET_" + name.upper()
            if not (self.defect == "secret-not-replaced" and var in os.environ):
                os.environ[var] = value
            self.injected.add(name)
        for name, value in previous.items():
            os.environ["DRIFT_SECRET_" + name.upper() + "_PREVIOUS"] = value
            self.injected.add(name)

    def handle(self, envelope):
        body = envelope.get("body") or {}
        if body.get("print") is not None:
            print(body["print"])
        if body.get("fail"):
            raise RuntimeError("the handler was asked to fail")
        return {
            "status": 200,
            "message": "ok",
            "payload": {
                "pid": os.getpid(),
                "tmpdir": os.environ.get("TMPDIR"),
                "home": os.environ.get("HOME"),
                "secrets": {k: v for k, v in os.environ.items() if k.startswith("DRIFT_SECRET_")},
            },
        }

    def reply(self, line):
        envelope = json.loads(line)
        self.apply(envelope)
        try:
            out = self.handle(envelope)
        except RuntimeError as e:
            if self.defect == "panic-survives":
                print("handler failed: %s" % e, file=sys.stderr)
                return None
            if self.defect == "panic-answers":
                print("handler failed: %s" % e, file=sys.stderr)
                return {"status": 500, "message": "the handler failed", "payload": None}
            raise
        if self.defect != "no-rss":
            out["rss_bytes"] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024
        return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--defect", default="none", choices=DEFECTS)
    defect = ap.parse_args().defect

    # The response stream is the real stdout, kept apart from what a handler
    # prints: while a handler runs, print reaches stderr.
    protocol = sys.stdout
    loop = Loop(defect)
    answered = 0
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        if defect == "needs-newline" and not line.endswith("\n"):
            continue
        if defect == "one-reply" and answered >= 1:
            continue
        printed_to = protocol if defect == "print-to-stdout" else sys.stderr
        with contextlib.redirect_stdout(printed_to):
            out = loop.reply(line)
        if out is not None:
            protocol.write(json.dumps(out) + "\n")
            protocol.flush()
        answered += 1
        if defect == "one-shot":
            return 0
    if defect == "eof-hangs":
        time.sleep(60)
    if defect == "eof-fails":
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main())
