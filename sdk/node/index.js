/**
 * Drift SDK for Node.js Atomic functions.
 *
 * Provides:
 *   - Backbone helpers: drift.backbone.{secret,cache,nosql,queue,blob,lock,sql,realtime}
 *   - Deed helpers:     drift.deed.{keyauth,jwt,vault,link,pocket}
 *   - Edge:             drift.edge.slice(name) calls a slice linked under
 *                       edge.links in the Driftfile.
 *   - log(msg): Writes to stderr (captured by runner).
 *   - httpRequest(): Outbound HTTP from within a function.
 *   - run(handler): the entry point the CLI's generated wrapper calls. You do
 *     NOT call this yourself — see the contract below.
 *
 * Uses only built-in APIs (process, fetch, http). Zero dependencies.
 *
 * ── HOW A DEPLOYED FUNCTION IS CALLED ──────────────────────────────────────
 *
 * `drift atomic deploy` generates a wrapper (app.js) that requires your file
 * and calls your function BY NAME. So a deployed function must:
 *
 *   1. be a NAMED function — `function myHandler(...)`. An arrow function
 *      assigned to a const cannot be found by name, and the deploy says so
 *      rather than shipping a function that never binds.
 *   2. be EXPORTED — `module.exports = { myHandler }`.
 *   3. return the 3-tuple `[status, message, payload]` — NOT an object.
 *
 * The signature differs by method, because the wrapper unwraps the body for
 * you on writes:
 *
 *   GET                        function myHandler(req)
 *   POST/PUT/DELETE/PATCH      function myHandler(body, req)
 *   queue trigger              function myHandler(body, req)
 *
 * A complete GET function:
 *
 *   const drift = require('@ondrift/sdk');
 *
 *   // Driftfile: name get:expenses, handler getExpenses
 *   async function getExpenses(req) {
 *     const rows = await drift.backbone.sql('ledger').query('SELECT * FROM expenses');
 *     return [200, 'OK', { count: rows.length, expenses: rows }];
 *   }
 *
 *   module.exports = { getExpenses };
 *
 * And a POST, which receives the decoded body first:
 *
 *   // Driftfile: name post:expense, handler postExpense
 *   async function postExpense(body, req) {
 *     if (!body.payer) return [400, 'Bad Request', { error: 'payer is required' }];
 *     return [200, 'OK', { ok: true }];
 *   }
 *
 *   module.exports = { postExpense };
 *
 * `drift atomic new` scaffolds exactly this shape — start from it rather than
 * from memory.
 */

"use strict";

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

function run(handler) {
  if (process.env.DRIFT_RUNTIME) {
    _runDeployed(handler);
  } else {
    _runLocal(handler);
  }
}

function _runDeployed(handler) {
  let data = "";
  process.stdin.on("data", (chunk) => (data += chunk));
  process.stdin.on("end", async () => {
    try {
      const req = JSON.parse(data);
      if (typeof req.query === "string") {
        const parsed = {};
        for (const pair of req.query.split("&")) {
          if (!pair) continue;
          const eq = pair.indexOf("=");
          const k = eq < 0 ? pair : pair.slice(0, eq);
          const v = eq < 0 ? "" : pair.slice(eq + 1);
          parsed[decodeURIComponent(k)] = decodeURIComponent(v);
        }
        req.query = parsed;
      }
      const resp = await handler(req);
      process.stdout.write(JSON.stringify(resp));
    } catch (err) {
      process.stdout.write(
        JSON.stringify({ status: 500, message: String(err), payload: null })
      );
    }
  });
}

// _renderResponse turns a wrapper's response object into the bytes and headers
// the local server writes, using the same rule the slice applies to a deployed
// function (`Atomic::build_func_response`): a Content-Type that is not JSON
// means `payload` is a base64 string to be written raw, and anything else gets
// the {status, message, payload} envelope.
//
// It exists so `drift atomic run` and a deployed function disagree about
// nothing. A local server that always enveloped would let someone build a
// webhook or an OAuth endpoint locally, watch it work, and find the envelope
// back the first time it ran on a slice.
function _renderResponse(resp) {
  const headers = Object.assign({}, resp.headers || {});
  const ct = headers["Content-Type"] || headers["content-type"] || "";
  const isJSON =
    ct === "" || ct === "application/json" || ct.startsWith("application/json;");

  if (!isJSON) {
    // The payload travels as base64 for the same reason it does on the wire:
    // it may be bytes that are not text at all.
    return [Buffer.from(String(resp.payload), "base64"), headers];
  }
  headers["Content-Type"] = "application/json";
  return [
    JSON.stringify({
      status: resp.status,
      message: resp.message,
      payload: resp.payload,
    }),
    headers,
  ];
}

function _runLocal(handler) {
  const http = require("http");
  const port = parseInt(process.env.PORT || "8080", 10);

  const server = http.createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;

    let parsed = null;
    if (body) {
      try {
        parsed = JSON.parse(body);
      } catch {
        parsed = body;
      }
    }

    const url = new URL(req.url, `http://localhost:${port}`);
    const headers = {};
    for (const [k, v] of Object.entries(req.headers)) {
      headers[k] = Array.isArray(v) ? v[0] : v;
    }

    const funcReq = {
      path:url.pathname,
      headers,
      query: url.search ? url.search.slice(1) : "",
      body: parsed,
    };

    try {
      const resp = await handler(funcReq);
      const [body, headers] = _renderResponse(resp);
      res.writeHead(resp.status || 200, headers);
      res.end(body);
    } catch (err) {
      res.writeHead(500, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ status: 500, message: String(err) }));
    }
  });

  server.listen(port, () => {
    process.stderr.write(`drift-sdk: local server starting on :${port}\n`);
  });
}

// ---------------------------------------------------------------------------
// Backbone transport
// ---------------------------------------------------------------------------

let _backboneUrl = null;
let _backboneAgent = null;
let _backboneRequestOpts = null;

function _getBackboneUrl() {
  if (_backboneUrl === null) {
    _backboneUrl = process.env.BACKBONE_URL || "";
  }
  return _backboneUrl;
}

// _initBackboneTransport parses BACKBONE_URL once and caches a
// node http.Agent + the per-request options that route to it.
// BACKBONE_URL is an http://host:port TCP URL.
function _initBackboneTransport(rawUrl) {
  const http = require("http");
  const u = new URL(rawUrl);
  _backboneAgent = new http.Agent({ keepAlive: true, maxSockets: 10 });
  _backboneRequestOpts = {
    host: u.hostname,
    port: u.port || 80,
    agent: _backboneAgent,
  };
}

// extraHeaders is an optional plain object merged in ahead of Content-Type —
// today its one caller is Deed.Pocket, whose routes are JWT-gated and need
// an Authorization: Bearer header (see _callAuth below).
function _backboneRequest(method, path, body, contentType, extraHeaders) {
  const http = require("http");
  if (_backboneRequestOpts === null) _initBackboneTransport(_getBackboneUrl());

  return new Promise((resolve, reject) => {
    const headers = Object.assign({}, extraHeaders);
    if (body) headers["Content-Type"] = contentType || "application/json";
    const opts = Object.assign({}, _backboneRequestOpts, {
      method,
      path: "/" + path,
      headers,
    });
    const req = http.request(opts, (resp) => {
      const chunks = [];
      resp.on("data", (c) => chunks.push(c));
      resp.on("end", () => {
        resolve({ status: resp.statusCode, body: Buffer.concat(chunks) });
      });
    });
    req.on("error", reject);
    if (body) req.write(body);
    req.end();
  });
}

async function _call(method, path, body) {
  const base = _getBackboneUrl();
  if (!base) return _callLocal(method, path, body);

  const payload = body !== undefined && body !== null ? JSON.stringify(body) : null;
  const { status, body: respBody } = await _backboneRequest(method, path, payload);
  // A non-2xx response body is an ERROR, not data — matches Go's
  // callBackboneHTTP and Deed's _callChecked below. Without this, a
  // 404/409/etc body (e.g. Secret.get on an undeclared name, a lock
  // conflict) was returned to the caller as if it were the value.
  if (status >= 400) {
    const text = respBody.toString("utf8").trim();
    throw new Error(`drift: backbone ${method} ${path}: HTTP ${status}${text ? `: ${text}` : ""}`);
  }
  if (status === 204 || respBody.length === 0) return null;
  const text = respBody.toString("utf8");
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function _callRaw(method, path, dataBytes, contentType) {
  const base = _getBackboneUrl();
  // Local dev goes through the SAME dispatch every other primitive uses, so a
  // blob has one local address rather than two. This used to write the bytes
  // into _store.blobs under the request path itself — a second, parallel store
  // that only Blob used, and one blob.get never read.
  if (!base) return _callLocal(method, path, dataBytes);
  const { status, body: respBody } = await _backboneRequest(
    method,
    path,
    dataBytes,
    contentType || "application/octet-stream",
  );
  if (status >= 400) {
    throw new Error(`blob op ${method} ${path}: HTTP ${status} ${respBody.toString("utf8")}`);
  }
  // Preserve the previous fetch-based ArrayBuffer return shape.
  return respBody.buffer.slice(respBody.byteOffset, respBody.byteOffset + respBody.byteLength);
}

// ---------------------------------------------------------------------------
// Deed transport
// ---------------------------------------------------------------------------
//
// Deed has its own listener/port now (DEED_URL) — a separate connection
// pool from Backbone's, mirroring _backboneUrl/_backboneAgent/
// _backboneRequest exactly (sharing one Agent between the two would pool
// sockets to the wrong port on every call that alternates between Backbone
// and Deed). Like the generic `_call` above (which now applies the same
// status >= 400 check), these treat a non-2xx response as an error rather
// than handing the caller whatever body came back — the "a get on a
// missing item is an error, not a silent null" convention every Backbone/
// Deed primitive shares (mirrors Go's callBackboneHTTP, applied uniformly).

let _deedUrl = null;
let _deedAgent = null;
let _deedRequestOpts = null;

function _getDeedUrl() {
  if (_deedUrl === null) {
    _deedUrl = process.env.DEED_URL || "";
  }
  return _deedUrl;
}

function _initDeedTransport(rawUrl) {
  const http = require("http");
  const u = new URL(rawUrl);
  _deedAgent = new http.Agent({ keepAlive: true, maxSockets: 10 });
  _deedRequestOpts = {
    host: u.hostname,
    port: u.port || 80,
    agent: _deedAgent,
  };
}

function _deedRequest(method, path, body, contentType, extraHeaders) {
  const http = require("http");
  if (_deedRequestOpts === null) _initDeedTransport(_getDeedUrl());

  return new Promise((resolve, reject) => {
    const headers = Object.assign({}, extraHeaders);
    if (body) headers["Content-Type"] = contentType || "application/json";
    const opts = Object.assign({}, _deedRequestOpts, {
      method,
      path: "/" + path,
      headers,
    });
    const req = http.request(opts, (resp) => {
      const chunks = [];
      resp.on("data", (c) => chunks.push(c));
      resp.on("end", () => {
        resolve({ status: resp.statusCode, body: Buffer.concat(chunks) });
      });
    });
    req.on("error", reject);
    if (body) req.write(body);
    req.end();
  });
}

async function _callChecked(method, path, body) {
  const base = _getDeedUrl();
  if (!base) {
    throw new Error("drift: deed requires a running slice (DEED_URL) — not available in local dev");
  }
  const payload = body !== undefined && body !== null ? JSON.stringify(body) : null;
  const { status, body: respBody } = await _deedRequest(method, path, payload);
  if (status >= 400) {
    throw new Error(`deed ${method} ${path}: HTTP ${status} ${respBody.toString("utf8")}`);
  }
  if (status === 204 || respBody.length === 0) return null;
  const text = respBody.toString("utf8");
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

// _callAuth is _callChecked with a bearer token attached — used only by
// Deed.Pocket, whose routes are JWT-gated (unlike every other loopback-open
// Backbone/Deed primitive). Local dev (no DEED_URL) has no JWT
// verification to check a token against, so — same as KeyAuth/JWT already
// — this call isn't available without a running slice.
async function _callAuth(method, path, token, body) {
  const base = _getDeedUrl();
  if (!base) {
    throw new Error("drift: deed pocket requires a running slice (DEED_URL) — not available in local dev");
  }
  const payload = body !== undefined && body !== null ? JSON.stringify(body) : null;
  const { status, body: respBody } = await _deedRequest(
    method, path, payload, "application/json", { Authorization: `Bearer ${token}` },
  );
  if (status >= 400) {
    throw new Error(`deed ${method} ${path}: HTTP ${status} ${respBody.toString("utf8")}`);
  }
  if (status === 204 || respBody.length === 0) return null;
  const text = respBody.toString("utf8");
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

// The page sizes the platform applies to a list read. Mirrored by the local store
// so a local run truncates where a slice would, and used as the page size listAll
// walks with.
const LIST_DEFAULT = 100;
const LIST_MAX = 1000;

// In-memory backbone for local dev.
const _store = {
  nosql: {},
  cache: {},
  queues: {},
  blobs: {},
  locks: {},
  nextId: 0,
};

// The position a local document key encodes. The local store mints keys from
// its own counter, so ordering by that value IS insertion order. A key that is
// not a number sorts as 0 and falls back to the string compare beside it.
function _localKeyIndex(key) {
  const n = parseInt(key, 10);
  return Number.isInteger(n) ? n : 0;
}

// The most `field=…&value=…` pairs one filtered read may combine on a slice.
const MAX_FILTER_PAIRS = 8;

// _localFilterPairs returns every field and value a list query carries, in
// order, refused where the slice refuses them: unequal numbers of each, more
// than MAX_FILTER_PAIRS pairs, or an empty field or value among several. One
// pair or none is not checked, and reads as it always has.
function _localFilterPairs(qs) {
  const fields = [];
  const values = [];
  for (const pair of (qs || "").split("&")) {
    if (!pair) continue;
    const eq = pair.indexOf("=");
    const k = decodeURIComponent(eq < 0 ? pair : pair.slice(0, eq));
    const v = decodeURIComponent(eq < 0 ? "" : pair.slice(eq + 1));
    if (k === "field") fields.push(v);
    if (k === "value") values.push(v);
  }
  if (fields.length <= 1 && values.length <= 1) return [fields, values];
  if (fields.length !== values.length) {
    throw new Error(
      `drift: backbone nosql/list: HTTP 400: a filter pairs each field with a value by position: got ${fields.length} field(s) and ${values.length} value(s)`,
    );
  }
  if (fields.length > MAX_FILTER_PAIRS) {
    throw new Error(
      `drift: backbone nosql/list: HTTP 400: a filter combines at most ${MAX_FILTER_PAIRS} fields, got ${fields.length}`,
    );
  }
  if (fields.concat(values).some((s) => !s)) {
    throw new Error(
      "drift: backbone nosql/list: HTTP 400: every field and value in a filter must be non-empty",
    );
  }
  return [fields, values];
}

// _localMatchesEvery reports whether doc's top-level fields equal EVERY pair.
// No pair, or a single pair with an empty field, filters nothing.
function _localMatchesEvery(doc, fields, values) {
  if (fields.length === 0 || (fields.length === 1 && !fields[0])) return true;
  for (let i = 0; i < fields.length; i++) {
    const want = i < values.length ? values[i] : "";
    if (String(doc[fields[i]]) !== want) return false;
  }
  return true;
}

// The slice's rule for a single-segment resource name such as a queue: an
// alphanumeric first byte, then alphanumerics and `. _ -`, at most 255 bytes.
function _localValidName(s) {
  return typeof s === "string" && Buffer.byteLength(s) <= 255 && /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(s);
}

// DRIFT_DECLARED_QUEUES names the queues the project's Driftfile declares,
// comma-separated. The CLI sets it for a local run; the local store creates
// each one, empty, before answering a queue call, as `drift file apply` creates
// them on a slice before anything runs. A queue it does not name still has to
// be pushed to before it exists.
function _ensureDeclaredQueues() {
  for (const raw of (process.env.DRIFT_DECLARED_QUEUES || "").split(",")) {
    const name = raw.trim();
    if (_localValidName(name) && !_store.queues[name]) _store.queues[name] = { msgs: [], nextId: 0 };
  }
}

// The slice's message timestamp: RFC 3339 in UTC with nine fractional digits.
// A Date holds milliseconds, so the last six are zero.
function _queueTimestamp() {
  return new Date().toISOString().replace(/\.(\d{3})Z$/, ".$1000000Z");
}

function _callLocal(method, path, body) {
  const [basePath, qs] = path.split("?", 2);
  const query = {};
  if (qs) {
    for (const pair of qs.split("&")) {
      const [k, v] = pair.split("=", 2);
      query[decodeURIComponent(k)] = decodeURIComponent(v || "");
    }
  }

  // NoSQL
  if (basePath === "write" && method === "POST") {
    const col = (body && body.collection) || "default";
    if (!_store.nosql[col]) _store.nosql[col] = {};
    _store.nextId++;
    const key = String(_store.nextId);
    // The platform injects the storage key into the stored document, and paging
    // reads it back off the last row. A local document without one reads as
    // un-pageable.
    _store.nosql[col][key] = { ...body, _key: key };
    return { key };
  }
  if (basePath === "read" && method === "GET") {
    const col = query.collection || "default";
    return (_store.nosql[col] || {})[query.key] || null;
  }
  if (basePath === "nosql/list" && method === "GET") {
    const col = query.collection || "default";
    const order = query.order || "";
    // Every `field=…&value=…` pair, paired by position. Several are ANDed, and
    // refused on the same grounds the slice refuses them.
    const [fields, values] = _localFilterPairs(qs);

    // The slice refuses an order it does not recognise rather than serving an
    // unordered read under the caller's name for one, and refuses the
    // combination with a filter because the field index carries no order.
    // Accepting either here would hide the failure until deployment.
    if (order && order !== "insertion") {
      throw new Error(`drift: backbone nosql/list: HTTP 400: unknown order "${order}"`);
    }
    if (order === "insertion" && fields.length > 0 && fields[0]) {
      throw new Error(
        "drift: backbone nosql/list: HTTP 400: order=insertion cannot be combined with a field filter",
      );
    }

    const docs = _store.nosql[col] || {};
    // Walk in a DEFINED order, like the platform's ordered walk, and apply the
    // same page cap. An object with integer-like keys enumerates numerically, so
    // the sort is explicit: the platform orders bytewise, where "1000" precedes
    // "999". `less` is also what the cursor compares against, so paging resumes
    // in whichever order was asked for.
    let less = (a, b) => a < b;
    if (order === "insertion") {
      // The local store mints keys from its own counter, so ordering by that
      // NUMBER is insertion order — the same guarantee a slice gets from its
      // order index, reached differently because the two mint keys differently.
      less = (a, b) => {
        const ia = _localKeyIndex(a);
        const ib = _localKeyIndex(b);
        if (ia !== ib) return ia < ib;
        return a < b;
      };
    }
    const keys = Object.keys(docs).sort((a, b) => (less(a, b) ? -1 : less(b, a) ? 1 : 0));
    const after = query.after || "";
    let limit = parseInt(query.limit, 10);
    if (!Number.isInteger(limit) || limit <= 0) limit = LIST_DEFAULT;
    if (limit > LIST_MAX) limit = LIST_MAX;
    const results = [];
    for (const k of keys) {
      if (after && !less(after, k)) continue;
      const doc = docs[k];
      if (!_localMatchesEvery(doc, fields, values)) continue;
      results.push(doc);
      if (results.length >= limit) break;
    }
    return results;
  }
  if (basePath === "nosql/drop" && method === "POST") {
    delete _store.nosql[query.collection];
    return null;
  }

  // Cache
  if (basePath === "cache/set" && method === "POST") {
    _store.cache[(body && body.key) || ""] = body && body.value;
    return null;
  }
  if (basePath === "cache/get" && method === "GET") {
    return _store.cache[query.key] !== undefined ? _store.cache[query.key] : null;
  }
  if (basePath === "cache/del") {
    delete _store.cache[query.key];
    return null;
  }

  // Queue
  //
  // Answered as a slice answers, refusals included: a push names its queue in
  // the body and must carry an object; a pop, a peek and a length name it in the
  // query string and nowhere else; a queue nothing was ever pushed to is a 404.
  if (basePath.startsWith("queue/")) _ensureDeclaredQueues();
  if (basePath === "queue/push" && method === "POST") {
    const name = body && body.queue;
    if (name !== undefined && typeof name !== "string") {
      throw new Error("drift: backbone queue/push: HTTP 400: invalid JSON");
    }
    const msg = body && body.body;
    if (!name || msg === null || typeof msg !== "object" || Array.isArray(msg)) {
      throw new Error("drift: backbone queue/push: HTTP 400: queue and body required");
    }
    if (!_localValidName(name)) throw new Error("drift: backbone queue/push: HTTP 400: invalid queue name");
    if (!_store.queues[name]) _store.queues[name] = { msgs: [], nextId: 0 };
    const q = _store.queues[name];
    const id = String(q.nextId++);
    q.msgs.push({ id, body: JSON.parse(JSON.stringify(msg)), timestamp: _queueTimestamp() });
    return { id };
  }
  const queueRead =
    (basePath === "queue/pop" && method === "POST") ||
    (basePath === "queue/peek" && method === "GET") ||
    (basePath === "queue/len" && method === "GET");
  if (queueRead) {
    const name = query.queue || "";
    if (!name) throw new Error(`drift: backbone ${basePath}: HTTP 400: queue query param required`);
    if (!_localValidName(name)) throw new Error(`drift: backbone ${basePath}: HTTP 400: invalid queue name`);
    const q = _store.queues[name];
    if (!q) throw new Error(`drift: backbone ${basePath}: HTTP 404: queue ${name} not found`);
    if (basePath === "queue/len") return { length: q.msgs.length };
    if (q.msgs.length === 0) return null;
    return basePath === "queue/pop" ? q.msgs.shift() : q.msgs[0];
  }

  // Blob
  //
  // Addressed by BUCKET AND KEY, which is what the wire format sends. These arms
  // used to read `name` — the shape from before the bucket/key split — and
  // _callRaw never reached them at all: it wrote the bytes into _store.blobs
  // under the full request path `blob/put?bucket=…&key=…`, while blob.get read
  // `blob/get?bucket=…&key=…`. Two code paths that were supposed to agree on a
  // key format and did not, so every local Put reported success and every local
  // Get returned null — indistinguishable from "the key never existed".
  //
  // Both now route through here, so there is ONE local address for a blob.
  if (basePath === "blob/put" && method === "POST") {
    if (!query.bucket || !query.key) {
      throw new Error("drift: backbone blob/put: HTTP 400: bucket and key are required");
    }
    _store.blobs[_blobAddress(query.bucket, query.key)] = body;
    return null;
  }
  if (basePath === "blob/get" && method === "GET") {
    const addr = _blobAddress(query.bucket, query.key);
    // ABSENT IS AN ERROR, matching the slice's 404 and Go's SDK. Returning null
    // would make "never stored" and "stored an empty value" the same answer,
    // which is the defect this whole path is being fixed for.
    if (!(addr in _store.blobs)) {
      throw new Error(
        `drift: backbone blob/get: HTTP 404: no object "${query.key}" in bucket "${query.bucket || "default"}"`,
      );
    }
    return _store.blobs[addr];
  }

  // Secret — in local dev, read from environment variables (loaded from .env by the CLI)
  if (basePath === "secret/get" && method === "GET") {
    return process.env[query.name] || null;
  }

  // Lock
  if (basePath === "lock/acquire" && method === "POST") {
    const name = (body && body.name) || "";
    if (_store.locks[name]) return null;
    _store.nextId++;
    const token = `local-lock-${_store.nextId}`;
    _store.locks[name] = token;
    return { token };
  }
  if (basePath === "lock/release" && method === "POST") {
    delete _store.locks[(body && body.name) || ""];
    return null;
  }

  return null;
}

// ---------------------------------------------------------------------------
// Secret
// ---------------------------------------------------------------------------

// Read order:
//   1. Persistent-worker per-request store (globalThis.__driftSecrets) —
//      populated by the lang server's AsyncLocalStorage from req.secrets.
//   2. DRIFT_SECRET_<NAME> env var — set by the runner for native subprocess
//      invocations.
//   3. Backbone HTTP — local dev only; production /secret/get is SAT-guarded.
const secret = {
  get: async (name) => {
    const store = globalThis.__driftSecrets && globalThis.__driftSecrets.getStore();
    if (store && Object.prototype.hasOwnProperty.call(store, name)) {
      return store[name];
    }
    const envVal = process.env["DRIFT_SECRET_" + name.toUpperCase()];
    if (envVal !== undefined) return envVal;
    const resp = await _call("GET", `secret/get?name=${encodeURIComponent(name)}`);
    return typeof resp === "string" ? resp : resp ? JSON.stringify(resp) : "";
  },
  // The value this secret held before it was last replaced, while that is
  // still inside its grace window; "" once it closes.
  //
  // For a credential the handler VERIFIES rather than presents — a webhook
  // signing secret whose sender is still using the old value while they catch
  // up. Same read order as get: the per-request store, then the environment.
  //
  // "" rather than a throw when there is none, because that is the normal
  // state and the caller's branch is the same whether the secret was never
  // replaced, was replaced long ago, or does not exist.
  previous: async (name) => {
    const store =
      globalThis.__driftSecretsPrevious && globalThis.__driftSecretsPrevious.getStore();
    if (store && Object.prototype.hasOwnProperty.call(store, name)) {
      return store[name];
    }
    const envVal = process.env["DRIFT_SECRET_" + name.toUpperCase() + "_PREVIOUS"];
    return envVal === undefined ? "" : envVal;
  },
  set: (name, value) => _call("POST", "secret/set", { name, value }),
  delete: (name) => _call("DELETE", `secret/delete?name=${encodeURIComponent(name)}`),
};

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

const cache = {
  get: (key) => _call("GET", `cache/get?key=${encodeURIComponent(key)}`),
  set: (key, value, ttl) => {
    const payload = { key, value };
    if (ttl > 0) payload.ttl = ttl;
    return _call("POST", "cache/set", payload);
  },
  delete: (key) => _call("DELETE", `cache/del?key=${encodeURIComponent(key)}`),
};

// ---------------------------------------------------------------------------
// NoSQL
// ---------------------------------------------------------------------------

const nosql = {
  collection: (name) => ({
    insert: async (doc) => {
      const payload = { collection: name, ...(typeof doc === "object" ? doc : { data: doc }) };
      const resp = await _call("POST", "write", payload);
      return (resp && resp.key) || "";
    },
    read: (key) =>
      _call("GET", `read?collection=${encodeURIComponent(name)}&key=${encodeURIComponent(key)}`),
    get: async (id) => {
      const path = `nosql/list?collection=${encodeURIComponent(name)}&field=_id&value=${encodeURIComponent(id)}`;
      const rows = await _call("GET", path);
      const arr = Array.isArray(rows) ? rows : [];
      return arr[0] || null;
    },
    delete: (key) =>
      _call("POST", `nosql/delete?collection=${encodeURIComponent(name)}&key=${encodeURIComponent(key)}`),
    // Returns ONE page, optionally filtered. Every entry in `filter` must match:
    // { group: "g1", status: "open" } returns the documents whose `group` is "g1"
    // AND whose `status` is "open". A filter combines at most 8 fields, each
    // compared against a top-level field of the document, and every field and
    // value in a filter of two or more must be non-empty.
    //
    // Pass a limit for any collection that grows: omitting it
    // does not mean "everything" — the platform applies its own default of 100
    // documents and returns a short list with nothing to mark it as truncated, so
    // a caller cannot tell "all of them" from "the first hundred". Rows come back
    // in key order rather than newest-first, so a capped read of an append-only
    // log is missing its most recent entries. The platform clamps the limit to
    // 1000. `after` resumes from a document's `_key`; use listAll for a collection
    // that can outgrow one page.
    list: (filter, limit, after) => {
      let path = `nosql/list?collection=${encodeURIComponent(name)}`;
      if (filter) {
        for (const [k, v] of Object.entries(filter)) {
          path += `&field=${encodeURIComponent(k)}&value=${encodeURIComponent(v)}`;
        }
      }
      if (limit > 0) path += `&limit=${encodeURIComponent(limit)}`;
      if (after) path += `&after=${encodeURIComponent(after)}`;
      return _call("GET", path).then((r) => (Array.isArray(r) ? r : []));
    },
    // Returns EVERY document matching the filter, paging until exhausted.
    //
    // One page is capped at 1000 and its response carries nothing marking it
    // short, so a bigger collection cannot be read correctly with list() alone —
    // and because rows arrive in key order rather than newest-first, what a capped
    // read omits is arbitrary rather than merely recent. An append-only log is the
    // case that breaks: a Merkle root over a subset of the log is not a proof of
    // the log.
    //
    // Every matching document is held in memory at once — the point for a ledger,
    // and a cost worth knowing elsewhere, since a function runs under a limit.
    listAll: async (filter) => {
      const page = LIST_MAX;
      const all = [];
      let after = "";
      for (;;) {
        const rows = await nosql.collection(name).list(filter, page, after);
        all.push(...rows);
        if (rows.length < page) return all;
        const next = rows[rows.length - 1] && rows[rows.length - 1]._key;
        if (!next) {
          throw new Error("drift: list row carries no _key, so the collection cannot be paged");
        }
        // A full page that does not move the cursor would loop forever. Report it
        // rather than returning a short read, which is the failure this exists to
        // prevent.
        if (next === after) {
          throw new Error(`drift: listing ${name} did not advance past _key ${after}`);
        }
        after = next;
      }
    },
    // Returns ONE page of documents in the order they were WRITTEN.
    //
    // list() and listAll() return rows in storage-key order, which is not the
    // order anything was appended: the key's counter is not zero-padded, so a
    // collection written 1…12 comes back 10, 11, 12, 1, 2 … 9. That is stable
    // and complete and it is not chronological, and it LOOKS chronological for
    // the first nine documents — long enough to pass a manual test.
    //
    // This is the read for anything shaped like a LOG: an event store, a
    // ledger, an activity feed, an outbox, a chat history. The platform keeps a
    // separate order index for it, so no document key changes and a cursor you
    // already hold keeps working.
    //
    // NO FILTER. The field index carries the same unpadded key the scan does, so
    // an ordered filtered read needs a second index rather than a flag — the
    // platform refuses the combination rather than quietly ignoring it. Filter
    // in the caller, or keep the partition in its own collection.
    //
    // Same limit rules as list(): pass one for any collection that grows, and
    // the platform clamps to 1000. Use listAllInOrder for a log past a page.
    listInOrder: (limit, after) => {
      let path = `nosql/list?order=insertion&collection=${encodeURIComponent(name)}`;
      if (limit > 0) path += `&limit=${encodeURIComponent(limit)}`;
      if (after) path += `&after=${encodeURIComponent(after)}`;
      return _call("GET", path).then((r) => (Array.isArray(r) ? r : []));
    },
    // Returns EVERY document in the order it was written, paging until the
    // collection is exhausted.
    //
    // This is what makes a hash-chained log verifiable: walking it in order is
    // the whole point, and a Merkle root over a subset is not a proof of the
    // set. Every document is held in memory at once — the point for a ledger,
    // and a cost worth knowing elsewhere, since a function runs under a limit.
    listAllInOrder: async () => {
      const page = LIST_MAX;
      const all = [];
      let after = "";
      for (;;) {
        const rows = await nosql.collection(name).listInOrder(page, after);
        all.push(...rows);
        if (rows.length < page) return all;
        const next = rows[rows.length - 1] && rows[rows.length - 1]._key;
        if (!next) {
          throw new Error("drift: list row carries no _key, so the collection cannot be paged");
        }
        // A full page that does not move the cursor would loop forever. Same
        // guard, same reason, as listAll: a short read is the failure this
        // method exists to prevent.
        if (next === after) {
          throw new Error(`drift: listing ${name} did not advance past _key ${after}`);
        }
        after = next;
      }
    },
    drop: () => _call("POST", `nosql/drop?collection=${encodeURIComponent(name)}`),
  }),
};

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

// The slice takes a push's queue name in the JSON body, and a pop's, a peek's
// and a length's in the query string and nowhere else. A pop or a peek answers
// with the message's envelope ({id, body, timestamp}); the caller gets the body
// it pushed, or null when the queue is empty. A queue nothing was ever pushed to
// does not exist, and every read of it throws.
function queue(name) {
  const at = (path) => `${path}?queue=${encodeURIComponent(name)}`;
  const head = async (method, path) => {
    const msg = await _call(method, at(path));
    if (msg === null) return null;
    if (typeof msg !== "object" || !("body" in msg)) {
      throw new Error(`drift: queue ${name}: the ${path} reply is not a message: ${JSON.stringify(msg)}`);
    }
    return msg.body;
  };
  return {
    // body must be a plain object; the slice refuses anything else. Resolves to
    // the id the slice gave the message.
    push: async (body) => {
      const reply = await _call("POST", "queue/push", { queue: name, body });
      if (!reply || typeof reply.id !== "string" || reply.id === "") {
        throw new Error(
          `drift: queue ${name}: the push may have succeeded, but its reply carries no id: ${JSON.stringify(reply)}`,
        );
      }
      return reply.id;
    },
    pop: () => head("POST", "queue/pop"),
    peek: () => head("GET", "queue/peek"),
    len: async () => {
      const reply = await _call("GET", at("queue/len"));
      if (!reply || typeof reply.length !== "number") {
        throw new Error(`drift: queue ${name}: the length reply is unreadable: ${JSON.stringify(reply)}`);
      }
      return reply.length;
    },
  };
}

// ---------------------------------------------------------------------------
// Blob
// ---------------------------------------------------------------------------

function _splitBucketKey(name) {
  const i = name.indexOf("/");
  if (i < 0) return ["default", name];
  return [name.slice(0, i), name.slice(i + 1)];
}

// The ONE address a blob has in the local store. Put and Get must derive it the
// same way or they address different things while both reporting success, which
// is precisely the defect this replaced: one wrote under the full request path
// `blob/put?bucket=…&key=…` and the other read `blob/get?bucket=…&key=…`.
function _blobAddress(bucket, key) {
  return `${bucket || ""}/${key || ""}`;
}

const blob = {
  put: async (name, data, contentType) => {
    const [bucket, key] = _splitBucketKey(name);
    const path = `blob/put?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(key)}`;
    const bytes = Buffer.isBuffer(data) || data instanceof Uint8Array
      ? data
      : Buffer.from(typeof data === "string" ? data : JSON.stringify(data));
    return _callRaw("POST", path, bytes, contentType);
  },
  get: async (name) => {
    const [bucket, key] = _splitBucketKey(name);
    const path = `blob/get?bucket=${encodeURIComponent(bucket)}&key=${encodeURIComponent(key)}`;
    const base = _getBackboneUrl();
    // Same dispatch as the put, so the two agree on where a blob lives. An
    // absent key THROWS here, matching the slice's 404 — returning null made
    // "never stored" and "stored an empty value" the same answer.
    if (!base) return _callLocal("GET", path, null);
    const resp = await fetch(`${base}/${path}`);
    if (!resp.ok) {
      const text = (await resp.text()).trim();
      throw new Error(`drift: backbone GET ${path}: HTTP ${resp.status}${text ? `: ${text}` : ""}`);
    }
    return Buffer.from(await resp.arrayBuffer());
  },
};

// ---------------------------------------------------------------------------
// Lock
// ---------------------------------------------------------------------------

const lock = {
  acquire: async (name, ttl) => {
    const resp = await _call("POST", "lock/acquire", { name, ttl });
    return (resp && resp.token) || "";
  },
  release: (name, token) => _call("POST", "lock/release", { name, token }),
};

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

function log(msg) {
  process.stderr.write(String(msg) + "\n");
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

// Default 30-second timeout on outbound calls. A function calling a hung
// remote shouldn't hold an Atomic invocation open longer than this; the
// runner's per-invocation deadline is the absolute ceiling. Pass
// `{timeoutMs: N}` in opts to override.
async function httpRequest(method, url, headers, body, opts) {
  const reqOpts = { method, headers: headers || {} };
  if (body !== undefined && body !== null) {
    reqOpts.body = typeof body === "string" ? body : JSON.stringify(body);
  }
  const timeoutMs = (opts && opts.timeoutMs) || 30000;
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), timeoutMs);
  reqOpts.signal = ctrl.signal;
  try {
    const resp = await fetch(url, reqOpts);
    const data = await resp.arrayBuffer();
    return { status: resp.status, body: Buffer.from(data) };
  } finally {
    clearTimeout(t);
  }
}

// Exports collected at the bottom of the file (single canonical
// `module.exports = {...}` block) once every symbol is declared.

// ─── SSE (Server-Sent Events) ────────────────────────────────────────────────

/**
 * Entry point for SSE streaming functions.
 *
 * Usage:
 *   // Driftfile: name get:events, handler getEvents, stream sse
 *   const drift = require("@ondrift/sdk");
 *   drift.runSSE(async (req, emit) => {
 *     for (let i = 0; i < 10; i++) {
 *       emit("counter", { value: i });
 *       await new Promise(r => setTimeout(r, 1000));
 *     }
 *   });
 */
function runSSE(handler) {
  if (process.env.DRIFT_RUNTIME) {
    let data = "";
    process.stdin.on("data", (chunk) => (data += chunk));
    process.stdin.on("end", async () => {
      const req = JSON.parse(data || "{}");
      const emit = (event, payload) => {
        if (event) process.stdout.write(`event: ${event}\n`);
        process.stdout.write(`data: ${JSON.stringify(payload)}\n\n`);
      };
      await handler(req, emit);
    });
    return;
  }
  _runLocalSSE(handler);
}

function _runLocalSSE(handler) {
  const http = require("http");
  const port = parseInt(process.env.PORT || "8080", 10);

  const server = http.createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;

    let parsed = null;
    if (body) {
      try {
        parsed = JSON.parse(body);
      } catch {
        parsed = body;
      }
    }

    const url = new URL(req.url, `http://localhost:${port}`);
    const headers = {};
    for (const [k, v] of Object.entries(req.headers)) {
      headers[k] = Array.isArray(v) ? v[0] : v;
    }

    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    });

    const funcReq = {
      path:url.pathname,
      headers,
      query: url.search ? url.search.slice(1) : "",
      body: parsed,
    };

    const emit = (event, payload) => {
      if (event) res.write(`event: ${event}\n`);
      res.write(`data: ${JSON.stringify(payload)}\n\n`);
    };

    let closed = false;
    req.on("close", () => {
      closed = true;
    });

    try {
      await handler(funcReq, emit);
    } catch (err) {
      if (!closed) {
        res.write(`event: error\ndata: ${JSON.stringify({ error: String(err) })}\n\n`);
      }
    }
    if (!closed) res.end();
  });

  server.listen(port, () => {
    process.stderr.write(`drift-sdk: local SSE server starting on :${port}\n`);
  });
}

// ─── WebSocket ───────────────────────────────────────────────────────────────

/**
 * Entry point for WebSocket functions.
 *
 * Usage:
 *   // Driftfile: name get:chat, handler getChat, stream ws
 *   const drift = require("@ondrift/sdk");
 *   drift.runWS(async (req, conn) => {
 *     while (true) {
 *       const msg = await conn.read();
 *       if (msg === null) break;
 *       conn.write({ echo: msg });
 *     }
 *   });
 */
function runWS(handler) {
  if (process.env.DRIFT_RUNTIME) {
    const readline = require("readline");
    const rl = readline.createInterface({ input: process.stdin });
    const lines = [];
    let resolve = null;

    rl.on("line", (line) => {
      if (resolve) {
        const r = resolve;
        resolve = null;
        r(line);
      } else {
        lines.push(line);
      }
    });
    rl.on("close", () => {
      if (resolve) {
        const r = resolve;
        resolve = null;
        r(null);
      }
    });

    const readLine = () =>
      new Promise((r) => {
        if (lines.length > 0) r(lines.shift());
        else resolve = r;
      });

    // First line is the initial request.
    readLine().then(async (firstLine) => {
      const req = firstLine ? JSON.parse(firstLine) : {};
      const conn = {
        read: async () => {
          const line = await readLine();
          if (line === null) return null;
          try {
            return JSON.parse(line);
          } catch {
            return line;
          }
        },
        write: (data) => {
          process.stdout.write(JSON.stringify(data) + "\n");
        },
        writeRaw: (msg) => {
          process.stdout.write(msg + "\n");
        },
      };
      await handler(req, conn);
    });
  }
}

// ---------------------------------------------------------------------------
// Exports — single canonical assignment. Every public symbol declared
// above lives in this block. If you add a new public symbol, add it
// here too.
// ---------------------------------------------------------------------------

// ─── SQL ────────────────────────────────────────────────────────────────────
//
// Per-slice SQLite databases addressed by name. Wire shape: one JSON envelope
// per call ({db, sql, args, tx?}). See docs/memos/backbone-sql.md.
//
//   const db = drift.backbone.sql('clinic');
//   const rows = await db.query('SELECT * FROM appointments WHERE slot >= ?', [from]);
//   await db.execute('INSERT INTO appointments(...) VALUES(?, ?)', ['alice', '10:00']);
//   await db.transaction(async (tx) => {
//     await tx.execute('UPDATE appointments SET status=? WHERE id=?', ['confirmed', 7]);
//   });
//

// _sqlArgs is the `args` every statement sends: always a JSON array. A default
// parameter covers an omitted argument and not an explicit null, which would
// go out as `"args":null`, and a slice that reads args as a list refuses that
// whole body as invalid JSON.
function _sqlArgs(args) {
  return args == null ? [] : args;
}

function _sqlRows(resp) {
  const cols = (resp && resp.columns) || [];
  const rows = (resp && resp.rows) || [];
  return rows.map(r => {
    const o = {};
    for (let i = 0; i < cols.length; i++) o[cols[i]] = r[i];
    return o;
  });
}

// Every method goes through `_call`, the same helper the other seven
// primitives use. It was the only primitive calling `_backboneRequest`
// directly, and got three things wrong by doing so:
//
//   1. `_backboneRequest` resolves `{status, body}`, not a Buffer — so
//      `resp.toString('utf8')` produced the literal string "[object Object]"
//      and every call died on `JSON.parse` with "Unexpected token o in JSON at
//      position 1". The whole Node SQL API was unusable.
//   2. Paths were passed with a leading slash while `_backboneRequest` prepends
//      one itself, producing `//sql/query`.
//   3. No status check, so a 4xx body would have been parsed as data — the
//      exact bug `_call`'s own comment was added to fix for everyone else.
function sql(name) {
  return {
    async query(sqlText, args) {
      return _sqlRows(await _call('POST', 'sql/query', { db: name, sql: sqlText, args: _sqlArgs(args) }));
    },
    async execute(sqlText, args) {
      return (await _call('POST', 'sql/execute', { db: name, sql: sqlText, args: _sqlArgs(args) })) || {};
    },
    async begin() {
      const { tx } = (await _call('POST', 'sql/begin', { db: name })) || {};
      return _sqlTx(name, tx);
    },
    async transaction(fn) {
      const tx = await this.begin();
      try {
        const out = await fn(tx);
        await tx.commit();
        return out;
      } catch (e) {
        try { await tx.rollback(); } catch (_) { /* ignore */ }
        throw e;
      }
    },
  };
}

function _sqlTx(db, token) {
  return {
    async query(sqlText, args) {
      return _sqlRows(await _call('POST', 'sql/query', { db, sql: sqlText, args: _sqlArgs(args), tx: token }));
    },
    async execute(sqlText, args) {
      return (await _call('POST', 'sql/execute', { db, sql: sqlText, args: _sqlArgs(args), tx: token })) || {};
    },
    async commit() {
      await _call('POST', 'sql/commit', { tx: token });
    },
    async rollback() {
      await _call('POST', 'sql/rollback', { tx: token });
    },
  };
}

// ─── Realtime (backbone.realtime) ─────────────────────────────────────────────
//
// Server-originated pub/sub. Subscribers connect over WebSocket at the Canvas
// route /realtime/<name>; a function fans a message out to all of them with
// backbone.realtime.channel(name).publish(msg).
const realtime = {
  channel: (name) => ({
    publish: async (msg) => {
      const resp = await _call("POST", "realtime/publish", { channel: name, message: msg });
      return (resp && resp.recipients) || 0;
    },
    presence: async () => {
      const resp = await _call("GET", `realtime/presence?channel=${encodeURIComponent(name)}`);
      return (resp && resp.present) || 0;
    },
  }),
};

// ================================================================
// Deed — identity, verified. The fourth pillar alongside Atomic /
// Backbone / Canvas — a peer subsystem, not a Backbone primitive, with its
// own loopback listener (DEED_URL) separate from Backbone's
// (BACKBONE_URL).
//
//   deed.keyauth / jwt / vault / link / pocket
//
// KeyAuth: passwordless Ed25519 device-key auth. JWT: general-purpose
// HS256 sign/verify (KeyAuth mints its own tokens through it). Vault: an
// account-key-wrapped keyring. Link: multi-device attestation /
// enrollment / revocation. Pocket: E2EE per-identity app data, JWT-gated.
//
// Not to be confused with cross-slice calling (edge.slice(name) /
// callerSlice, further below) — that's inter-slice networking, declared
// under edge.links in the Driftfile. deed.link enrolls another DEVICE for
// the same identity; it has nothing to do with calling another SLICE.
// ================================================================

// ─── KeyAuth (Deed.keyauth) ────────────────────────────────────────────────────
//
// Passwordless Ed25519 device-key auth. uid = the public key. challenge mints a
// one-time nonce; the client signs the canonical {domain,nonce,pubkey}; verify
// checks the signature and returns THIS slice's session JWT (sub = pubkey). The
// client half (keygen + recovery phrase) is the @ondrift/keyauth library.
const keyauth = {
  challenge: async (pubkey) => {
    const resp = await _callChecked("POST", "keyauth/challenge", { pubkey });
    return (resp && resp.nonce) || "";
  },
  verify: async (pubkey, sig, domain) => {
    const resp = await _callChecked("POST", "keyauth/verify", { pubkey, sig, domain });
    return (resp && resp.token) || "";
  },
};

// ─── JWT (Deed.jwt) ─────────────────────────────────────────────────────────────
//
// HS256 minting + verification, signed with the slice's per-slice JKey. The
// signing key never leaves the slice's backbone process; all operations flow
// through loopback HTTP to backbone /jwt/{sign,verify,slice-id}.
//
// Design: internal/todo/slice-jwt-primitive.md.

class JWTError extends Error {
  constructor(reason) {
    super(`jwt verify: ${reason}`);
    this.name = "JWTError";
    this.reason = reason;
  }
}

const jwt = {
  /**
   * Sign a JWT with the slice's HS256 JKey.
   *
   * `claims.exp` is required. `iat`, `iss`, and `jti` are auto-set when
   * unset. `claims.custom` is a plain object of app-specific claims that
   * the platform never inspects.
   */
  async issue(claims = {}) {
    const body = {};
    for (const k of ["sub", "iat", "exp", "nbf", "iss", "aud", "jti", "custom"]) {
      if (claims[k] !== undefined && claims[k] !== null) body[k] = claims[k];
    }
    const resp = await _callChecked("POST", "jwt/sign", body);
    return resp && resp.token ? resp.token : null;
  },

  /**
   * Validate a token. Resolves with the parsed claims object on success;
   * throws JWTError on validation failure with a stable wire `reason`.
   */
  async verify(token, opts = {}) {
    const body = { token };
    if (opts.audience) body.audience = opts.audience;
    if (opts.allowedIssuer) body.allowed_issuer = opts.allowedIssuer;
    const resp = await _callChecked("POST", "jwt/verify", body);
    if (!resp || typeof resp !== "object") throw new JWTError("internal_error");
    if (!resp.valid) throw new JWTError(resp.reason || "internal_error");
    return resp.claims || {};
  },

  /** The slice's auto-set issuer string ("drift-slice-<user>-<slice>"). */
  async sliceId() {
    const resp = await _callChecked("GET", "jwt/slice-id");
    return resp && resp.slice_id ? resp.slice_id : "";
  },
};

// ─── Vault (Deed.vault) ─────────────────────────────────────────────────────────
//
// Zero-knowledge recovery store: opaque, user-scoped, append-only. The client
// encrypts the blob under a key derived from its recovery phrase (which the
// slice NEVER sees), so Drift stores the backup but cannot read it. Backed by
// Deed's own dedicated routes (deed/vault/*) — replaces the old generic-NoSQL-
// collection implementation entirely; no Driftfile declaration needed.
const vault = {
  /** Appends an opaque encrypted backup blob for uid (append-only — a new version each call). */
  put: (uid, blob) => _callChecked("POST", "deed/vault/put", { uid, blob }),

  /**
   * Returns the most recent backup blob for uid. Throws if uid has never
   * written one — same "missing is an error, not a silent null" convention
   * as Pocket.get.
   */
  get: async (uid) => {
    const resp = await _callChecked("GET", `deed/vault/get?uid=${encodeURIComponent(uid)}`);
    if (!resp || typeof resp.blob === "undefined") {
      throw new Error("drift: vault.get: no vault entry for uid");
    }
    return resp.blob;
  },
};

// ─── Link (Deed.link) ───────────────────────────────────────────────────────────
//
// Multi-device continuity: generalizes the enroll/attest/revoke pattern so an
// identity's KeyAuth session can move to a second, third, ... device. The
// signature parameters below (sig, attestingPubkey, ...) are produced entirely
// client-side — this SDK only forwards them, the same way keyauth.verify
// forwards a signature it never computes itself. The one rule the whole design
// rests on: Deed verifies, it never decides — a device is only ever added on
// the strength of a signature from a device already active in the identity's
// registry.
//
// Not to be confused with cross-slice calling (edge.slice(name)/callerSlice,
// further below) — this Link enrolls a DEVICE for one identity, it does not
// call another slice.
const link = {
  /**
   * Starts a device-linking session for a not-yet-enrolled device's pubkey
   * (usually carried in a QR code alongside the pubkey). Returns a session ID
   * for an already-active device to present to attest.
   *
   * opts.metadata is an optional opaque string an attesting device can read
   * back via link.sessionInfo — e.g. an ephemeral key it should seal a
   * payload for. Deed never interprets it.
   */
  begin: async (pubkey, opts = {}) => {
    const body = { pubkey };
    if (opts.metadata) body.metadata = opts.metadata;
    const resp = await _callChecked("POST", "deed/link/begin", body);
    return (resp && resp.session_id) || "";
  },

  /**
   * A read-only, repeatable peek at a pending session — what an attesting
   * device (which only ever learns sessionId, from a scanned/typed code)
   * uses to learn new_pubkey (attest's message is verified server-side
   * against the session's own stored value, never the request body, so the
   * attester has to reconstruct it exactly) and whatever opaque metadata
   * the joining device passed to begin.
   */
  sessionInfo: async (sessionId) =>
    (await _callChecked("POST", "deed/link/session", { session_id: sessionId })) || {},

  /**
   * Renders text (in practice, a Link session ID) as a scannable QR code,
   * returning inline SVG markup. Pure rendering — no session or identity
   * involvement, so it works for any short string.
   */
  qr: async (text) => {
    const resp = await _callChecked("POST", "deed/link/qr", { text });
    return (resp && resp.svg) || "";
  },

  /**
   * Has an already-active device vouch for the session's pending device. sig
   * is the client's signature over the canonical {domain,identity,new_pubkey}
   * message — computed client-side, never by this SDK.
   *
   * opts.sealed is an optional opaque string relayed back once complete
   * reports "attested" — e.g. a payload end-to-end-encrypted for whatever
   * key the joiner published as begin's metadata. Deed only relays it.
   */
  attest: (identity, sessionId, attestingPubkey, sig, opts = {}) => {
    const body = {
      identity, session_id: sessionId, attesting_pubkey: attestingPubkey, sig,
    };
    if (opts.sealed) body.sealed = opts.sealed;
    return _callChecked("POST", "deed/link/attest", body);
  },

  /**
   * Polls a session the new device started with begin, returning whether an
   * active device has attested it yet: `{status, identity?, sealed?}` —
   * identity/sealed are only set once status === "attested" (sealed only if
   * attest supplied one).
   */
  complete: async (sessionId) =>
    (await _callChecked("POST", "deed/link/complete", { session_id: sessionId })) || {},

  /**
   * Deactivates targetPubkey in identity's device registry. Any currently-
   * active device may revoke another (or itself); revokingPubkey is the
   * device doing the revoking, sig its signature over the canonical
   * {domain,identity,target_pubkey} message.
   */
  revoke: (identity, targetPubkey, revokingPubkey, sig) =>
    _callChecked("POST", "deed/link/revoke", {
      identity, target_pubkey: targetPubkey, revoking_pubkey: revokingPubkey, sig,
    }),
};

// ─── Pocket (Deed.pocket) ────────────────────────────────────────────────────────
//
// An app's actual data — E2EE, content-keyed, following an identity across
// every device Link has enrolled. The crypto work happens entirely
// client-side before anything reaches this primitive; Pocket never encrypts or
// decrypts the payload itself. Every call takes `token` explicitly (the JWT
// keyauth.verify returned) rather than holding hidden session state —
// matching the rest of this SDK's stateless posture inside an Atomic function
// invocation. The token's sub is the only identity a call can read or write
// under; there is no way to name a different one.
const pocket = {
  /** Stores blob under key for whichever identity token resolves to. */
  set: (token, key, blob) => _callAuth("POST", "deed/pocket/set", token, { key, blob }),

  /** Returns the blob stored under key for token's identity. Throws if no such key exists. */
  get: async (token, key) => {
    const resp = await _callAuth("GET", `deed/pocket/get?key=${encodeURIComponent(key)}`, token);
    if (!resp || typeof resp.blob === "undefined") {
      throw new Error("drift: pocket.get: no such key");
    }
    return resp.blob;
  },

  /** Removes key for token's identity. Throws if no such key exists. */
  delete: (token, key) => _callAuth("POST", "deed/pocket/delete", token, { key }),

  /** Returns every key stored under token's identity — never another identity's, even by guessing. */
  list: async (token) => {
    const resp = await _callAuth("GET", "deed/pocket/list", token);
    return Array.isArray(resp) ? resp : [];
  },
};

// ─── Deed — the fourth pillar, a peer of Backbone ───────────────────────────────
//
// The single entrypoint for every identity primitive. See the module header
// comment above for the full rationale and the edge.slice(name)
// disambiguation.
const deed = { keyauth, jwt, vault, link, pocket };

// ─── Backbone — the B of the sacred A·B·C triad ───────────────────────────────
//
// The single entrypoint for every stateful primitive. Nothing stateful lives at
// the top level; the triad is the namespace for everything under it. (Identity
// — keyauth/jwt/vault/link/pocket — lives under `deed`, above, a peer of
// Backbone rather than one of its primitives.)
const backbone = { secret, cache, nosql, queue, blob, lock, sql, realtime };

// ─── Edge: slice-to-slice links (inter-slice networking, NOT the same as
// deed.link device enrollment above) ─────────────────────────────────────────
//
// A link is an Edge fact, declared under edge.links in the Driftfile:
//
//   edge:
//     links: [billing, docs]

function _linkEnvName(name) {
  return "DRIFT_LINK_" + name.toUpperCase().replace(/[^A-Z0-9]/g, "_") + "_URL";
}

// _sliceClient returns a client for another slice of the same owner that this
// slice links to. The call travels in-cluster and carries this slice's identity
// (X-Drift-Slice) as a label when the runtime supplies one.
function _sliceClient(name) {
  const resolveURL = (path) => {
    const baseUrl = process.env[_linkEnvName(name)];
    if (!baseUrl) {
      throw new Error(
        `drift: slice "${name}" is not linked: declare it under edge.links in the Driftfile`,
      );
    }
    return baseUrl.replace(/\/+$/, "") + "/" + String(path).replace(/^\/+/, "");
  };
  // The identity header is attached last, and a caller's own X-Drift-Slice is
  // dropped in any letter case, so a caller cannot relabel the call. It is
  // omitted when the runtime supplies no identity: an empty header would assert
  // an identity of "" rather than none. See callerSlice for why the header is a
  // label and never evidence.
  const request = (method, path, headers, body) => {
    const url = resolveURL(path);
    const self = process.env.DRIFT_SLICE || "";
    const h = {};
    for (const [k, v] of Object.entries(headers || {})) {
      if (self && k.toLowerCase() === "x-drift-slice") continue;
      h[k] = v;
    }
    if (self) h["X-Drift-Slice"] = self;
    return httpRequest(method, url, h, body);
  };
  return {
    request,
    get: (path) => request("GET", path, null, null),
    post: (path, body) => request("POST", path, { "Content-Type": "application/json" }, body),
  };
}

// edge is the entrypoint for calling another slice. edge.slice(name) returns a
// client for a slice linked under edge.links.
const edge = {
  slice: _sliceClient,
};

/**
 * Returns exactly what drift.edge.slice(name) returns.
 *
 * @deprecated Use drift.edge.slice(name). This spelling keeps working, with the
 * same behaviour.
 */
function slice(name) {
  return _sliceClient(name);
}

// callerSlice returns the linked slice that called this request, or "" if the
// request didn't arrive over a slice-to-slice link (case-insensitive header).
// It reads a header the caller writes, so it is a label for logging and
// routing and authorises nothing.
function callerSlice(req) {
  const h = (req && req.headers) || {};
  for (const k of Object.keys(h)) {
    if (k.toLowerCase() === "x-drift-slice") return h[k];
  }
  return "";
}

// env returns an environment variable value ("" if unset).
function env(key) {
  return process.env[key] || "";
}

// ---------------------------------------------------------------------------
// Exports — single canonical assignment. State primitives live under
// `backbone` (the sacred triad); identity primitives live under `deed` (the
// fourth pillar); calls to linked slices live under `edge`; Atomic
// entrypoints and utilities stay top-level, beside the deprecated top-level
// `slice` alias.
// ---------------------------------------------------------------------------

module.exports = {
  run, runSSE, runWS,
  log, httpRequest, env,
  backbone,
  deed,
  edge,
  slice, callerSlice,
  JWTError,
};
