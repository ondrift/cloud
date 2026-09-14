#!/usr/bin/env python3
"""check.py — compare one SDK driver's output against the shared expectations.

    <driver> | python3 check.py <language>

The driver prints a JSON object of case name to the value it actually got. The
expectations live in expected.json and are the SAME for every language, which is
the whole point: six independently-worded suites can each pass while asserting
subtly different things, and that is how five SDKs came to disagree with Go
about local blobs without anybody noticing.

A case the driver does not report is a FAILURE, not a skip. An SDK that lacks a
method entirely is exactly what this is looking for, and silently passing over it
would make this harness agree with the thing it exists to catch.
"""

import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))


def main() -> int:
    language = sys.argv[1] if len(sys.argv) > 1 else "unknown"

    with open(os.path.join(HERE, "expected.json")) as f:
        expected = json.load(f)

    raw = sys.stdin.read().strip()
    if not raw:
        print(f"  {language}: the driver printed NOTHING — it failed before reporting anything")
        return 1
    try:
        actual = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"  {language}: the driver's output is not JSON ({e})")
        print("  ---- what it printed ----")
        for line in raw.splitlines()[:20]:
            print(f"  {line}")
        return 1

    if isinstance(actual, dict) and actual.get("error"):
        print(f"  {language}: the driver reported an error: {actual['error']}")
        return 1

    cases = [k for k in expected if not k.startswith("$")]
    failed = []
    for case in cases:
        want = expected[case]["expect"]
        if case not in actual:
            failed.append((case, "NOT REPORTED — the SDK has no such method, or the driver could not call it", want))
            continue
        got = actual[case]
        if got != want:
            failed.append((case, got, want))

    if not failed:
        print(f"  {language}: {len(cases)}/{len(cases)} cases pass")
        return 0

    print(f"  {language}: {len(cases) - len(failed)}/{len(cases)} pass, {len(failed)} FAIL")
    for case, got, want in failed:
        print(f"    ✗ {case}")
        print(f"        want: {short(want)}")
        print(f"        got:  {short(got)}")
        print(f"        why this matters: {expected[case]['why']}")
    return 1


def short(v, limit=110):
    """Render a value for a failure line without burying it in a page of numbers."""
    s = json.dumps(v)
    return s if len(s) <= limit else s[: limit - 3] + "..."


if __name__ == "__main__":
    sys.exit(main())
