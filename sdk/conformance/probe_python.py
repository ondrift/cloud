"""The Python program the start check cases run (probe_check.py).

It hands a handler that must not run to the SDK's own entry points, drift.run
for the entry "run" and drift.run_sse and drift.run_ws for the streaming ones.
Its two arguments are the entry's name and the marker the handler writes to
stderr, before it exits 97, if it is ever reached.
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "python"))
import drift  # noqa: E402


def main():
    if len(sys.argv) != 3 or sys.argv[1] not in ("run", "run_sse", "run_ws"):
        sys.stderr.write("usage: probe_python.py <run|run_sse|run_ws> <marker>\n")
        sys.exit(2)
    entry, marker = sys.argv[1], sys.argv[2]

    def handler(*_):
        sys.stderr.write(marker + "\n")
        sys.stderr.flush()
        os._exit(97)

    getattr(drift, entry)(handler)


main()
