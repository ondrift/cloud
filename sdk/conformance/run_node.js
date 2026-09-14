// Drives the Node SDK through the shared conformance cases and prints what it
// got, as JSON, for check.py to compare against expected.json.
//
// It asserts NOTHING itself. The assertions are in expected.json, once, for all
// six languages — six independently-worded suites can each pass while asserting
// subtly different things, and that is how five SDKs came to disagree with Go
// about local blobs without anybody noticing.

// No BACKBONE_URL: every case runs against the in-memory local store, which is
// the loop a developer meets first and the one where the six drifted. Set
// BEFORE the require, because the SDK caches the value on first read.
process.env.BACKBONE_URL = "";

const drift = require("../node/index.js");

const out = {};

function emit() {
  process.stdout.write(JSON.stringify(out, null, 2) + "\n");
}

// fail reports the first thing that went wrong and stops. A driver that carried
// on would report later cases against a store in a state nobody intended.
function fail(message) {
  out.error = message;
  emit();
  process.exit(0); // check.py turns the reported error into the failure
}

async function main() {
  const blob = drift.backbone.blob;

  // ── Blob ──────────────────────────────────────────────────────────────────
  try {
    await blob.put("uploads/greeting.txt", "hello, slice", "text/plain");
  } catch (e) {
    fail(`Blob.put with a bucket: ${e.message}`);
  }
  try {
    out.blob_with_bucket = (await blob.get("uploads/greeting.txt")).toString("utf8");
  } catch (e) {
    fail(`Blob.get with a bucket: ${e.message}`);
  }

  try {
    await blob.put("greeting.txt", "bare", "");
  } catch (e) {
    fail(`Blob.put without a bucket: ${e.message}`);
  }
  try {
    out.blob_without_bucket = (await blob.get("greeting.txt")).toString("utf8");
  } catch (e) {
    fail(`Blob.get without a bucket: ${e.message}`);
  }

  // A key never written must FAIL, and "error" is what a failure reports here.
  // An SDK that returns empty-with-no-error reports null instead and fails the
  // case — which is the whole point, because an implementation returning empty
  // for EVERYTHING would otherwise pass the two cases above by accident.
  try {
    const got = await blob.get("uploads/never-written.txt");
    out.blob_absent = got === null || got === undefined || got.length === 0 ? null : got.toString("utf8");
  } catch {
    out.blob_absent = "error";
  }

  // ── Ordered NoSQL reads ───────────────────────────────────────────────────
  const small = drift.backbone.nosql.collection("conformance_small");
  for (let i = 1; i <= 12; i++) {
    try {
      await small.insert({ n: i });
    } catch (e) {
      fail(`Insert ${i}: ${e.message}`);
    }
  }

  try {
    out.list_in_order = (await small.listInOrder(100)).map((d) => d.n);
  } catch (e) {
    fail(`listInOrder: ${e.message}`);
  }
  try {
    out.list_default_order = (await small.list(null)).map((d) => d._key);
  } catch (e) {
    fail(`list: ${e.message}`);
  }

  // Past the 1000-row page cap, which is where a cursor that advances in KEY
  // order rather than in the ordered sequence silently truncates.
  const big = drift.backbone.nosql.collection("conformance_big");
  for (let i = 1; i <= 1200; i++) {
    try {
      await big.insert({ n: i });
    } catch (e) {
      fail(`Insert big ${i}: ${e.message}`);
    }
  }
  try {
    const ns = (await big.listAllInOrder()).map((d) => d.n);
    out.list_all_in_order_count = ns.length;
    if (ns.length > 0) {
      out.list_all_in_order_first = ns[0];
      out.list_all_in_order_last = ns[ns.length - 1];
    }
    if (ns.length >= 1002) out.list_all_in_order_boundary = ns.slice(998, 1002);
  } catch (e) {
    fail(`listAllInOrder: ${e.message}`);
  }

  emit();
}

main().catch((e) => {
  out.error = String((e && e.stack) || e);
  emit();
});
