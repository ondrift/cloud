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

  // ── Filtered NoSQL reads ──────────────────────────────────────────────────
  const filtered = drift.backbone.nosql.collection("conformance_filter");
  const wide = { _id: "wide" };
  for (let i = 0; i < 9; i++) wide[`f${i}`] = "v";
  for (const doc of [
    { _id: "x1", a: "1", b: "1" },
    { _id: "x2", a: "1", b: "2" },
    { _id: "x3", a: "2", b: "1" },
    { _id: "x4", a: "3", b: "2" },
    wide,
  ]) {
    try {
      await filtered.insert(doc);
    } catch (e) {
      fail(`Insert ${doc._id}: ${e.message}`);
    }
  }
  const idsOf = (rows) => rows.map((d) => d._id).sort();
  try {
    out.list_filter_two_fields = idsOf(await filtered.list({ a: "1", b: "2" }));
  } catch (e) {
    fail(`list with two entries: ${e.message}`);
  }
  try {
    out.list_filter_one_field = idsOf(await filtered.list({ a: "1" }));
  } catch (e) {
    fail(`list with one entry: ${e.message}`);
  }
  try {
    const nine = {};
    for (let i = 0; i < 9; i++) nine[`f${i}`] = "v";
    out.list_filter_over_cap = idsOf(await filtered.list(nine));
  } catch {
    out.list_filter_over_cap = "error";
  }

  // ── Edge links ────────────────────────────────────────────────────────────
  // A loopback peer that echoes the path it was asked for and the identity
  // header it received, null when the header is absent.
  const http = require("http");
  const peer = http.createServer((req, res) => {
    const slice = req.headers["x-drift-slice"];
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ path: new URL(req.url, "http://peer").pathname, slice: slice === undefined ? null : slice }));
  });
  await new Promise((resolve) => peer.listen(0, "127.0.0.1", resolve));
  delete process.env.DRIFT_SLICE;
  process.env.DRIFT_LINK_BILLING_API_URL = `http://127.0.0.1:${peer.address().port}/base/`;
  const echoed = (resp) => JSON.parse(resp.body.toString("utf8"));

  try {
    const e = echoed(await drift.edge.slice("billing-api").get("/api/events"));
    out.edge_slice_url = e.path;
    out.slice_identity_absent = e.slice;
  } catch (e) {
    fail(`edge.slice get: ${e.message}`);
  }
  try {
    out.slice_url = echoed(await drift.slice("billing-api").get("/api/events")).path;
  } catch (e) {
    fail(`slice get: ${e.message}`);
  }

  process.env.DRIFT_SLICE = "billing";
  try {
    const resp = await drift.edge.slice("billing-api").request("GET", "/api/events", { "X-Drift-Slice": "operator" }, null);
    out.slice_identity_kept = echoed(resp).slice;
  } catch (e) {
    fail(`edge.slice request with a caller identity: ${e.message}`);
  }
  delete process.env.DRIFT_SLICE;
  peer.closeAllConnections();
  peer.close();

  try {
    await drift.edge.slice("billing").get("/x");
    out.edge_slice_not_linked = null;
  } catch (e) {
    out.edge_slice_not_linked = e.message;
  }
  try {
    await drift.slice("billing").get("/x");
    out.slice_not_linked = null;
  } catch (e) {
    out.slice_not_linked = e.message;
  }

  emit();
}

main().catch((e) => {
  out.error = String((e && e.stack) || e);
  emit();
});
