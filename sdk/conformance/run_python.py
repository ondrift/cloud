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

# No BACKBONE_URL: every case runs against the in-memory local store, which is the
# loop a developer meets first and the one where the six drifted. Set BEFORE the
# import, because the SDK caches the value on first read.
os.environ["BACKBONE_URL"] = ""

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


def main():
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

    emit()


try:
    main()
except Exception as e:  # noqa: BLE001
    out["error"] = repr(e)
    emit()
