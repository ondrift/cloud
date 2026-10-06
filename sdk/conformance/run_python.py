"""Drives the Python SDK through the shared conformance cases and prints what it
got, as JSON, for check.py to compare against expected.json.

It asserts NOTHING itself. The assertions are in expected.json, once, for all six
languages — six independently-worded suites can each pass while asserting subtly
different things, and that is how five SDKs came to disagree with Go about local
blobs without anybody noticing.
"""

import json
import os
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, HTTPServer

# No BACKBONE_URL: every case runs against the in-memory local store, which is the
# loop a developer meets first and the one where the six drifted. Set BEFORE the
# import, because the SDK caches the value on first read.
#
# DRIFT_CONFORMANCE_BACKBONE_URL names a running slice's Backbone instead, and
# runs the queue cases against it alone: the slice is the authority on the queue
# wire, and the local store is held to what it answers.
SLICE = os.environ.get("DRIFT_CONFORMANCE_BACKBONE_URL", "")
os.environ["BACKBONE_URL"] = SLICE

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "python"))

import drift  # noqa: E402

out = {}


def emit():
    print(json.dumps(out, indent=2))


def fail(message):
    """Report the first thing that went wrong and stop. A driver that carried on
    would report later cases against a store in a state nobody intended."""
    out["error"] = message
    emit()
    sys.exit(0)  # check.py turns the reported error into the failure


def queue_cases():
    """Drive every queue call once. The names carry the time, so a run against a
    slice, whose queues outlive the run, never meets an earlier run's."""
    stamp = f"{time.time_ns()}{os.getpid()}"
    q = drift.backbone.queue(f"conformance-python-{stamp}")
    absent = drift.backbone.queue(f"conformance-absent-python-{stamp}")

    def attempt(fn, read):
        try:
            return read(fn())
        except Exception:  # noqa: BLE001
            return "error"

    def body(msg):
        return None if msg is None else msg["n"]

    def same(v):
        return v

    def refused(_):
        return None

    out["queue_pop_never_pushed"] = attempt(absent.pop, body)
    try:
        absent.pop()
        out["queue_error_carries_the_refusal"] = False
    except Exception as e:  # noqa: BLE001
        out["queue_error_carries_the_refusal"] = "HTTP 404" in str(e) and "not found" in str(e)
    out["queue_len_never_pushed"] = attempt(absent.len, same)
    out["queue_push_not_an_object"] = attempt(lambda: q.push("a bare string"), refused)
    out["queue_push_bad_name"] = attempt(lambda: drift.backbone.queue("-flag").push({"n": 0}), refused)
    out["queue_push_ids"] = []
    for n in range(1, 4):
        try:
            out["queue_push_ids"].append(q.push({"n": n}))
        except Exception as e:  # noqa: BLE001
            fail(f"queue.push {n}: {e}")
    out["queue_len_after_push"] = attempt(q.len, same)
    out["queue_peek"] = attempt(q.peek, body)
    out["queue_len_after_peek"] = attempt(q.len, same)
    out["queue_pop_in_order"] = [attempt(q.pop, body) for _ in range(3)]
    out["queue_pop_empty"] = attempt(q.pop, body)
    out["queue_peek_empty"] = attempt(q.peek, body)
    out["queue_len_after_pop"] = attempt(q.len, same)


def main():
    queue_cases()
    if SLICE:
        emit()
        return

    # A declared queue, named the way the CLI names it for a local run.
    os.environ["DRIFT_DECLARED_QUEUES"] = "conformance-declared"
    try:
        out["declared_queue_pop_empty"] = drift.backbone.queue("conformance-declared").pop()
    except Exception:  # noqa: BLE001
        out["declared_queue_pop_empty"] = "error"

    blob = drift.backbone.blob

    # -- Blob ---------------------------------------------------------------
    try:
        blob.put("uploads/greeting.txt", b"hello, slice", "text/plain")
    except Exception as e:  # noqa: BLE001
        fail(f"Blob.put with a bucket: {e}")
    try:
        out["blob_with_bucket"] = blob.get("uploads/greeting.txt").decode("utf-8")
    except Exception as e:  # noqa: BLE001
        fail(f"Blob.get with a bucket: {e}")

    try:
        blob.put("greeting.txt", b"bare", "")
    except Exception as e:  # noqa: BLE001
        fail(f"Blob.put without a bucket: {e}")
    try:
        out["blob_without_bucket"] = blob.get("greeting.txt").decode("utf-8")
    except Exception as e:  # noqa: BLE001
        fail(f"Blob.get without a bucket: {e}")

    # A key never written must FAIL, and "error" is what a failure reports here.
    # An SDK that returns empty-with-no-error reports null instead and fails the
    # case — which is the whole point, because an implementation returning empty
    # for EVERYTHING would otherwise pass the two cases above by accident.
    try:
        got = blob.get("uploads/never-written.txt")
        out["blob_absent"] = None if not got else got.decode("utf-8")
    except Exception:  # noqa: BLE001
        out["blob_absent"] = "error"

    # -- Ordered NoSQL reads ------------------------------------------------
    small = drift.backbone.nosql.collection("conformance_small")
    for i in range(1, 13):
        try:
            small.insert({"n": i})
        except Exception as e:  # noqa: BLE001
            fail(f"Insert {i}: {e}")

    try:
        out["list_in_order"] = [d["n"] for d in small.list_in_order(100)]
    except Exception as e:  # noqa: BLE001
        fail(f"list_in_order: {e}")
    try:
        out["list_default_order"] = [d["_key"] for d in small.list()]
    except Exception as e:  # noqa: BLE001
        fail(f"list: {e}")

    # Past the 1000-row page cap, which is where a cursor that advances in KEY
    # order rather than in the ordered sequence silently truncates.
    big = drift.backbone.nosql.collection("conformance_big")
    for i in range(1, 1201):
        try:
            big.insert({"n": i})
        except Exception as e:  # noqa: BLE001
            fail(f"Insert big {i}: {e}")
    try:
        ns = [d["n"] for d in big.list_all_in_order()]
        out["list_all_in_order_count"] = len(ns)
        if ns:
            out["list_all_in_order_first"] = ns[0]
            out["list_all_in_order_last"] = ns[-1]
        if len(ns) >= 1002:
            out["list_all_in_order_boundary"] = ns[998:1002]
    except Exception as e:  # noqa: BLE001
        fail(f"list_all_in_order: {e}")

    # -- Filtered NoSQL reads -----------------------------------------------
    filtered = drift.backbone.nosql.collection("conformance_filter")
    wide = {"_id": "wide"}
    for i in range(9):
        wide[f"f{i}"] = "v"
    for doc in [
        {"_id": "x1", "a": "1", "b": "1"},
        {"_id": "x2", "a": "1", "b": "2"},
        {"_id": "x3", "a": "2", "b": "1"},
        {"_id": "x4", "a": "3", "b": "2"},
        wide,
    ]:
        try:
            filtered.insert(doc)
        except Exception as e:  # noqa: BLE001
            fail(f"Insert {doc['_id']}: {e}")
    try:
        out["list_filter_two_fields"] = sorted(d["_id"] for d in filtered.list({"a": "1", "b": "2"}))
    except Exception as e:  # noqa: BLE001
        fail(f"list with two entries: {e}")
    try:
        out["list_filter_one_field"] = sorted(d["_id"] for d in filtered.list({"a": "1"}))
    except Exception as e:  # noqa: BLE001
        fail(f"list with one entry: {e}")
    try:
        out["list_filter_over_cap"] = sorted(d["_id"] for d in filtered.list({f"f{i}": "v" for i in range(9)}))
    except Exception:  # noqa: BLE001
        out["list_filter_over_cap"] = "error"

    # -- Edge links ---------------------------------------------------------
    # A loopback peer that echoes the path it was asked for and the identity
    # header it received, null when the header is absent.
    class Peer(BaseHTTPRequestHandler):
        def do_GET(self):
            slices = self.headers.get_all("X-Drift-Slice")
            body = json.dumps({
                "path": urllib.parse.urlsplit(self.path).path,
                "slice": ",".join(slices) if slices else None,
            }).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, fmt, *args):
            pass

    peer = HTTPServer(("127.0.0.1", 0), Peer)
    threading.Thread(target=peer.serve_forever, daemon=True).start()
    os.environ.pop("DRIFT_SLICE", None)
    os.environ["DRIFT_LINK_BILLING_API_URL"] = f"http://127.0.0.1:{peer.server_port}/base/"

    def echoed(resp):
        _status, body = resp
        return json.loads(body)

    try:
        e = echoed(drift.edge.slice("billing-api").get("/api/events"))
        out["edge_slice_url"] = e["path"]
        out["slice_identity_absent"] = e["slice"]
    except Exception as e:  # noqa: BLE001
        fail(f"edge.slice get: {e}")
    try:
        out["slice_url"] = echoed(drift.slice("billing-api").get("/api/events"))["path"]
    except Exception as e:  # noqa: BLE001
        fail(f"slice get: {e}")

    os.environ["DRIFT_SLICE"] = "billing"
    try:
        resp = drift.edge.slice("billing-api").request("GET", "/api/events", headers={"X-Drift-Slice": "operator"})
        out["slice_identity_kept"] = echoed(resp)["slice"]
    except Exception as e:  # noqa: BLE001
        fail(f"edge.slice request with a caller identity: {e}")
    os.environ.pop("DRIFT_SLICE", None)
    peer.shutdown()

    try:
        drift.edge.slice("billing").get("/x")
        out["edge_slice_not_linked"] = None
    except Exception as e:  # noqa: BLE001
        out["edge_slice_not_linked"] = str(e)
    try:
        drift.slice("billing").get("/x")
        out["slice_not_linked"] = None
    except Exception as e:  # noqa: BLE001
        out["slice_not_linked"] = str(e)

    emit()


try:
    main()
except Exception as e:  # noqa: BLE001
    out["error"] = repr(e)
    emit()
