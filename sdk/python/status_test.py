"""A refused Backbone call raises an error that carries the slice's own words.

    python3 python/status_test.py

The transport raised urllib's HTTPError with no body, so its message was
"HTTP Error 404: Not Found" and the slice's reply, the part that says WHICH
queue is missing, was gone. This stands up a loopback server that answers as a
slice does and asks what the SDK hands the caller.
"""

import json
import os
import sys
import threading
import urllib.error
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))


class Slice(BaseHTTPRequestHandler):
    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        if self.path.startswith("/queue/push"):
            reply, status = {"id": "0"}, 200
        else:
            reply, status = {"error": "queue jobs not found"}, 404
        raw = json.dumps(reply).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_):
        pass


def main():
    server = HTTPServer(("127.0.0.1", 0), Slice)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    os.environ["BACKBONE_URL"] = f"http://127.0.0.1:{server.server_port}"
    import drift  # noqa: E402  (BACKBONE_URL is read on first use)

    failures = []
    try:
        drift.backbone.queue("jobs").pop()
        failures.append("a 404 was returned as a value")
    except urllib.error.HTTPError as e:
        print("  ✓ a refusal is still an HTTPError, so existing handlers keep catching it")
        msg = str(e)
        for want in ("HTTP 404", "queue jobs not found", "queue/pop"):
            if want not in msg:
                failures.append(f"the error {msg!r} does not carry {want!r}")
        if b"queue jobs not found" not in e.read():
            failures.append("the slice's reply is not readable through .read()")
        else:
            print("  ✓ the error carries the status and the slice's own words")

    if drift.backbone.queue("jobs").push({"n": 1}) != "0":
        failures.append("push did not return the slice's id")
    else:
        print("  ✓ push returns the id the slice gave the message")

    server.shutdown()
    if failures:
        for f in failures:
            print(f"  ✗ {f}")
        sys.exit(1)
    print("\n✅ every case passed")


if __name__ == "__main__":
    main()
