//! Drift SDK for Rust Atomic functions.
//!
//! Provides:
//!   - run(handler): Entry point (deployed or local mode).
//!   - `backbone` — the B of the sacred A·B·C triad; the SOLE entrypoint for
//!     every STATE primitive: `backbone::secret`, `backbone::cache`,
//!     `backbone::nosql`, `backbone::queue`, `backbone::blob`, `backbone::lock`,
//!     `backbone::sql`, `backbone::realtime`. (There is no top-level
//!     `drift_sdk::secret` etc. — go through `drift_sdk::backbone`.)
//!   - `deed` — Drift's 4th architectural pillar (identity, verified), a peer
//!     of Backbone/Atomic/Canvas: `deed::keyauth` (passwordless Ed25519
//!     device-key auth), `deed::jwt` (general-purpose HS256 sign/verify —
//!     KeyAuth mints its own tokens through it), `deed::vault` (an
//!     account-key-wrapped keyring), `deed::link` (multi-device attestation /
//!     enrollment / revocation), `deed::pocket` (E2EE per-identity app data,
//!     JWT-gated). Not to be confused with `edge::slice`/`caller_slice` below
//!     — that's inter-slice networking; `deed::link` enrolls another DEVICE
//!     for the same identity, it has nothing to do with calling another SLICE.
//!   - log(msg): Writes to stderr (captured by runner).
//!   - http_request(): Outbound HTTP from within a function.
//!   - edge::slice(name) / caller_slice(req): slice-to-slice links, declared
//!     under `edge.links` in the Driftfile. The crate-root slice(name) is the
//!     deprecated spelling of edge::slice(name).

use std::cell::RefCell;
use std::collections::{BTreeMap, HashMap};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::TcpListener;

pub use serde_json::Value;

// ---------------------------------------------------------------------------
// Response headers
// ---------------------------------------------------------------------------

thread_local! {
    static RESPONSE_HEADERS: RefCell<BTreeMap<String, String>> = const {
        RefCell::new(BTreeMap::new())
    };
}

/// Set a response header for the invocation in progress.
///
/// This is how a Rust handler reaches the raw-response escape hatch: the slice
/// writes `payload` as raw bytes whenever the function's Content-Type is not
/// JSON, and wraps it in `{status, message, payload}` otherwise. A body a third
/// party has to parse — a webhook receiver, an OAuth or OIDC endpoint, an
/// OpenID4VP request object — needs the former.
///
/// ```no_run
/// drift_sdk::set_response_header("Content-Type", "application/oauth-authz-req+jwt");
/// ```
///
/// The other five languages carry headers as an optional fourth element of the
/// handler's return. Rust cannot: its wrapper destructures a fixed-arity tuple,
/// so growing one would fail to compile against every handler already written.
/// A sink keeps the change additive.
pub fn set_response_header(name: &str, value: &str) {
    RESPONSE_HEADERS.with(|h| {
        h.borrow_mut().insert(name.to_string(), value.to_string());
    });
}

/// Take the headers set during this invocation, leaving none behind.
///
/// Called by the generated wrapper, not by user code. Draining rather than
/// reading matters if a process ever serves more than one call: a header left
/// in place would leak onto the next response.
pub fn take_response_headers() -> Option<Value> {
    RESPONSE_HEADERS.with(|h| {
        let taken = std::mem::take(&mut *h.borrow_mut());
        if taken.is_empty() {
            return None;
        }
        Some(Value::Object(
            taken
                .into_iter()
                .map(|(k, v)| (k, Value::String(v)))
                .collect(),
        ))
    })
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

pub fn run<F>(handler: F)
where
    F: Fn(Value) -> Value + 'static,
{
    if std::env::var("DRIFT_RUNTIME").is_ok() {
        run_deployed(&handler);
    } else {
        run_local(handler);
    }
}

fn run_deployed<F: Fn(Value) -> Value>(handler: &F) {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let mut req: Value = serde_json::from_str(&input).unwrap_or(Value::Null);
    if let Some(q) = req.get("query").and_then(|v| v.as_str()).map(str::to_string) {
        let mut parsed = serde_json::Map::new();
        for pair in q.split('&').filter(|p| !p.is_empty()) {
            let (k, v) = match pair.split_once('=') {
                Some((k, v)) => (k, v),
                None => (pair, ""),
            };
            parsed.insert(percent_decode(k), Value::String(percent_decode(v)));
        }
        if let Some(obj) = req.as_object_mut() {
            obj.insert("query".to_string(), Value::Object(parsed));
        }
    }
    let resp = handler(req);
    let out = serde_json::to_string(&resp).unwrap();
    io::stdout().write_all(out.as_bytes()).unwrap();
    io::stdout().flush().unwrap();
}

fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(b) = u8::from_str_radix(std::str::from_utf8(&bytes[i + 1..i + 3]).unwrap_or("00"), 16) {
                out.push(b);
                i += 3;
                continue;
            }
        }
        if bytes[i] == b'+' { out.push(b' '); } else { out.push(bytes[i]); }
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// Turn a wrapper's response into the bytes and headers the local server writes,
/// using the same rule the slice applies to a deployed function
/// (`Atomic::build_func_response`): a Content-Type that is not JSON means
/// `payload` is a base64 string to be written raw, and anything else gets the
/// `{status, message, payload}` envelope.
///
/// It exists so `drift atomic run` and a deployed function disagree about
/// nothing. A local server that always enveloped would let someone build a
/// webhook or an OAuth endpoint locally, watch it work, and find the envelope
/// back the first time it ran on a slice.
fn render_response(resp: &Value) -> (Vec<u8>, Vec<(String, String)>) {
    let mut headers: Vec<(String, String)> = resp
        .get("headers")
        .and_then(|h| h.as_object())
        .map(|o| {
            o.iter()
                .filter_map(|(k, v)| v.as_str().map(|s| (k.clone(), s.to_string())))
                .collect()
        })
        .unwrap_or_default();

    let ct = headers
        .iter()
        .find(|(k, _)| k.eq_ignore_ascii_case("content-type"))
        .map(|(_, v)| v.as_str())
        .unwrap_or("");
    let is_json =
        ct.is_empty() || ct == "application/json" || ct.starts_with("application/json;");

    if !is_json {
        // The payload travels as base64 for the same reason it does on the
        // wire: it may be bytes that are not text at all.
        let raw = resp.get("payload").and_then(|p| p.as_str()).unwrap_or("");
        return (b64_decode(raw), headers);
    }

    headers.retain(|(k, _)| !k.eq_ignore_ascii_case("content-type"));
    headers.push(("Content-Type".to_string(), "application/json".to_string()));

    let body = serde_json::json!({
        "status": resp.get("status"),
        "message": resp.get("message"),
        "payload": resp.get("payload"),
    });
    (
        serde_json::to_string(&body).unwrap_or_default().into_bytes(),
        headers,
    )
}

/// Standard base64 decode, for the raw-response path above. Anything that is not
/// valid base64 yields empty rather than panicking in a dev server.
fn b64_decode(s: &str) -> Vec<u8> {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut rev = [255u8; 256];
    for (i, c) in TABLE.iter().enumerate() {
        rev[*c as usize] = i as u8;
    }

    let mut out = Vec::new();
    let mut acc: u32 = 0;
    let mut bits = 0u32;
    for &c in s.as_bytes() {
        if c == b'=' || c.is_ascii_whitespace() {
            continue;
        }
        let v = rev[c as usize];
        if v == 255 {
            return Vec::new();
        }
        acc = (acc << 6) | v as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    out
}

fn run_local<F: Fn(Value) -> Value + 'static>(handler: F) {
    let port: u16 = std::env::var("PORT")
        .unwrap_or_else(|_| "8080".into())
        .parse()
        .unwrap_or(8080);

    let listener = TcpListener::bind(format!("0.0.0.0:{}", port)).unwrap();
    eprintln!("drift-sdk: local server starting on :{}", port);

    for stream in listener.incoming() {
        let mut stream = match stream {
            Ok(s) => s,
            Err(_) => continue,
        };

        let mut reader = BufReader::new(match stream.try_clone() {
            Ok(s) => s,
            Err(_) => continue,
        });

        // Read request line.
        let mut request_line = String::new();
        if reader.read_line(&mut request_line).is_err() {
            continue;
        }
        let parts: Vec<&str> = request_line.trim().splitn(3, ' ').collect();
        if parts.len() < 2 {
            continue;
        }
        let _ = parts[0]; // method is no longer surfaced to handlers
        let path_str = parts[1];

        // Read headers.
        let mut headers = serde_json::Map::new();
        let mut content_length: usize = 0;
        loop {
            let mut line = String::new();
            if reader.read_line(&mut line).is_err() {
                break;
            }
            let trimmed = line.trim().to_string();
            if trimmed.is_empty() {
                break;
            }
            if let Some((k, v)) = trimmed.split_once(": ") {
                if k.eq_ignore_ascii_case("content-length") {
                    content_length = v.parse().unwrap_or(0);
                }
                headers.insert(k.to_lowercase(), Value::String(v.to_string()));
            }
        }

        // Read body.
        let body = if content_length > 0 {
            let mut buf = vec![0u8; content_length];
            let _ = reader.read_exact(&mut buf);
            let raw = String::from_utf8_lossy(&buf).to_string();
            match serde_json::from_str::<Value>(&raw) {
                Ok(v) => v,
                Err(_) => Value::String(raw),
            }
        } else {
            Value::Null
        };

        // Parse path and query.
        let (path, query) = match path_str.split_once('?') {
            Some((p, q)) => (p, q),
            None => (path_str, ""),
        };

        let req = serde_json::json!({
            "path": path,
            "headers": Value::Object(headers),
            "query": query,
            "body": body,
        });

        let resp = handler(req);
        let status = resp.get("status").and_then(|s| s.as_u64()).unwrap_or(200);
        let (out, out_headers) = render_response(&resp);

        let mut response = format!("HTTP/1.1 {} OK\r\n", status);
        for (k, v) in &out_headers {
            response.push_str(&format!("{}: {}\r\n", k, v));
        }
        response.push_str(&format!("Content-Length: {}\r\n\r\n", out.len()));

        let mut bytes = response.into_bytes();
        bytes.extend_from_slice(&out);
        let _ = stream.write_all(&bytes);
    }
}

// ---------------------------------------------------------------------------
// Backbone transport
// ---------------------------------------------------------------------------

fn get_backbone_url() -> String {
    std::env::var("BACKBONE_URL").unwrap_or_default()
}

// Deed lives on its own listener/port now — a separate env var, separate
// base URL from Backbone's. See the `deed` module below.
fn get_deed_url() -> String {
    std::env::var("DEED_URL").unwrap_or_default()
}

fn percent_encode(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char);
            }
            _ => {
                out.push_str(&format!("%{:02X}", b));
            }
        }
    }
    out
}

fn call(method: &str, path: &str, body: Option<Value>) -> Option<Value> {
    let base = get_backbone_url();
    if base.is_empty() {
        return call_local(method, path, body);
    }
    call_at(&base, method, path, body)
}

// Same wire logic as `call`, against an explicit base URL rather than
// Backbone's. Used by `deed_call` (Deed's own listener/port) so `jwt` — the
// one Deed primitive that keeps this softer, non-`Result` calling
// convention — can move off Backbone's base URL without duplicating the
// whole HTTP dance.
fn call_at(base: &str, method: &str, path: &str, body: Option<Value>) -> Option<Value> {
    let url = format!("{}/{}", base, path);

    let result = if let Some(b) = body {
        ureq::request(method, &url)
            .set("Content-Type", "application/json")
            .send_string(&serde_json::to_string(&b).unwrap_or_default())
    } else {
        ureq::request(method, &url).call()
    };

    match result {
        Ok(resp) => {
            if resp.status() == 204 {
                return None;
            }
            let text = resp.into_string().unwrap_or_default();
            if text.is_empty() {
                return None;
            }
            serde_json::from_str(&text)
                .ok()
                .or_else(|| Some(Value::String(text)))
        }
        Err(ureq::Error::Status(204, _)) => None,
        Err(ureq::Error::Status(_, resp)) => {
            let text = resp.into_string().unwrap_or_default();
            if text.is_empty() {
                return None;
            }
            serde_json::from_str(&text)
                .ok()
                .or_else(|| Some(Value::String(text)))
        }
        Err(_) => None,
    }
}

// The queue's transport, which keeps what `call` drops: whether the slice
// refused. Ok(None) is a 204 or an empty body, Ok(Some) a 2xx body, and Err a
// refusal or a call that never arrived. Local dev answers from `local_queue`,
// which answers as a slice does, refusals included.
fn queue_call(method: &str, path: &str, body: Option<Value>) -> Result<Option<Value>, String> {
    let base = get_backbone_url();
    if base.is_empty() {
        return local_queue(method, path, body);
    }
    let url = format!("{}/{}", base, path);
    let result = match body {
        Some(b) => ureq::request(method, &url)
            .set("Content-Type", "application/json")
            .send_string(&serde_json::to_string(&b).unwrap_or_default()),
        None => ureq::request(method, &url).call(),
    };
    let base_path = path.split('?').next().unwrap_or(path);
    match result {
        Ok(resp) => {
            if resp.status() == 204 {
                return Ok(None);
            }
            let text = resp.into_string().unwrap_or_default();
            if text.is_empty() {
                return Ok(None);
            }
            Ok(Some(serde_json::from_str(&text).unwrap_or(Value::String(text))))
        }
        Err(ureq::Error::Status(code, resp)) => {
            let text = resp.into_string().unwrap_or_default();
            Err(format!("drift: backbone {base_path}: HTTP {code}: {}", text.trim()))
        }
        Err(e) => Err(format!("drift: backbone {base_path}: {e}")),
    }
}

// `jwt`'s transport: same forgiving None-on-miss convention as `call`, but
// against Deed's own base URL (DEED_URL) since /jwt/* moved off Backbone's
// listener along with the rest of Deed. Falls back to `call_local` in local
// dev, same as `call` — `call_local` has no jwt/* arm, so this preserves
// today's local-dev behavior (None) unchanged.
fn deed_call(method: &str, path: &str, body: Option<Value>) -> Option<Value> {
    let base = get_deed_url();
    if base.is_empty() {
        return call_local(method, path, body);
    }
    call_at(&base, method, path, body)
}

// call_raw posts raw bytes (used by blob.put). The platform's /blob/put
// expects ?bucket=&key= query params and a binary body, not JSON.
//
// LOCAL DEV IS NOT HANDLED HERE, deliberately. Every other primitive falls back
// to call_local, which speaks JSON; a blob is bytes, and routing bytes through a
// JSON dispatch is what produced the bug this note replaces — the local arms
// keyed on a `name` query parameter that the bucket/key wire format stopped
// sending, so a put stored under one string and a get looked up another. Put
// reported success, get returned nothing, and the two were indistinguishable
// from "that key was never written".
//
// blob::put and blob::get now own their local branch, against a byte store. See
// them for the key format.
fn call_raw(method: &str, path: &str, data_bytes: &[u8], content_type: &str) -> Option<Vec<u8>> {
    let base = get_backbone_url();
    if base.is_empty() {
        return None;
    }
    let url = format!("{}/{}", base, path);
    let resp = ureq::request(method, &url)
        .set("Content-Type", content_type)
        .send_bytes(data_bytes);
    match resp {
        Ok(r) => {
            let mut buf = Vec::new();
            let _ = r.into_reader().read_to_end(&mut buf);
            Some(buf)
        }
        Err(_) => None,
    }
}

// ---------------------------------------------------------------------------
// Local in-memory backbone (used when BACKBONE_URL is unset, i.e. local dev)
// ---------------------------------------------------------------------------

use std::sync::{Mutex, OnceLock};

/// The page sizes the platform applies to a list read. Mirrored by the local store
/// so a local run truncates where a slice would, and used as the page size
/// `list_all` walks with.
pub(crate) const LIST_DEFAULT: usize = 100;
pub(crate) const LIST_MAX: usize = 1000;

struct LocalStore {
    secrets: HashMap<String, String>,
    cache: HashMap<String, Value>,
    nosql: HashMap<String, Vec<Value>>,    // collection -> docs
    queues: HashMap<String, LocalQueue>,
    // bucket/key -> the bytes themselves. BYTES, not Value: a blob is binary,
    // and holding it as JSON meant every round trip through this store had to
    // agree on an encoding as well as on a key. The address is the same pair the
    // wire format uses, built by blob::local_address.
    blobs: HashMap<String, Vec<u8>>,
    locks: HashMap<String, String>,        // name -> token
}

fn local_store() -> &'static Mutex<LocalStore> {
    static STORE: OnceLock<Mutex<LocalStore>> = OnceLock::new();
    STORE.get_or_init(|| {
        Mutex::new(LocalStore {
            secrets: HashMap::new(),
            cache: HashMap::new(),
            nosql: HashMap::new(),
            queues: HashMap::new(),
            blobs: HashMap::new(),
            locks: HashMap::new(),
        })
    })
}

/// One of a slice's queues, mirrored: its live messages in order, each the
/// envelope a slice answers with, and the counter their ids come from. A slice
/// numbers each queue's messages from "0" and never reuses an id.
#[derive(Default)]
struct LocalQueue {
    msgs: Vec<Value>,
    next_id: u64,
}

/// The slice's rule for a single-segment resource name such as a queue: an
/// alphanumeric first byte, then alphanumerics and `. _ -`, at most 255 bytes.
fn local_valid_name(s: &str) -> bool {
    let b = s.as_bytes();
    !b.is_empty()
        && b.len() <= 255
        && b[0].is_ascii_alphanumeric()
        && b.iter().all(|&c| c.is_ascii_alphanumeric() || matches!(c, b'.' | b'_' | b'-'))
}

/// The slice's message timestamp: RFC 3339 in UTC with all nine fractional
/// digits.
fn rfc3339_nanos(t: std::time::SystemTime) -> String {
    let d = t.duration_since(std::time::UNIX_EPOCH).unwrap_or_default();
    let secs = d.as_secs() as i64;
    let (days, rem) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    // The civil date from a count of days since the Unix epoch, by Howard
    // Hinnant's days-to-civil algorithm.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + if month <= 2 { 1 } else { 0 };
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:09}Z",
        rem / 3600,
        rem % 3600 / 60,
        rem % 60,
        d.subsec_nanos()
    )
}

/// The queue routes, answered as a slice answers them: a push names its queue in
/// the body and must carry an object; a pop, a peek and a length name it in the
/// query string and nowhere else; a missing or malformed name is a 400, and a
/// queue nothing was ever pushed to is a 404.
fn local_queue(method: &str, path: &str, body: Option<Value>) -> Result<Option<Value>, String> {
    let base_path = path.split('?').next().unwrap_or(path);
    let refuse = |code: u16, msg: &str| Err(format!("drift: backbone {base_path}: HTTP {code}: {msg}"));
    let mut store = local_store().lock().map_err(|_| "drift: the local store is poisoned".to_string())?;

    // DRIFT_DECLARED_QUEUES names the queues the project's Driftfile declares,
    // comma-separated. The CLI sets it for a local run, and each one exists,
    // empty, before any queue call is answered, as `drift file apply` creates
    // them on a slice before anything runs. A queue it does not name still has
    // to be pushed to before it exists.
    for name in std::env::var("DRIFT_DECLARED_QUEUES").unwrap_or_default().split(',') {
        let name = name.trim();
        if local_valid_name(name) {
            store.queues.entry(name.to_string()).or_default();
        }
    }

    if (method, base_path) == ("POST", "queue/push") {
        let b = body.unwrap_or(Value::Null);
        let name = match b.get("queue") {
            None => "",
            Some(Value::String(s)) => s.as_str(),
            Some(_) => return refuse(400, "invalid JSON"),
        };
        let msg = b.get("body").cloned().unwrap_or(Value::Null);
        if name.is_empty() || !msg.is_object() {
            return refuse(400, "queue and body required");
        }
        if !local_valid_name(name) {
            return refuse(400, "invalid queue name");
        }
        let q = store.queues.entry(name.to_string()).or_default();
        let id = q.next_id.to_string();
        q.next_id += 1;
        q.msgs.push(serde_json::json!({
            "id": id,
            "body": msg,
            "timestamp": rfc3339_nanos(std::time::SystemTime::now()),
        }));
        return Ok(Some(serde_json::json!({ "id": id })));
    }

    let read = matches!(
        (method, base_path),
        ("POST", "queue/pop") | ("GET", "queue/peek") | ("GET", "queue/len")
    );
    if !read {
        return refuse(404, "not found");
    }
    let name = parse_query(path).remove("queue").unwrap_or_default();
    if name.is_empty() {
        return refuse(400, "queue query param required");
    }
    if !local_valid_name(&name) {
        return refuse(400, "invalid queue name");
    }
    let Some(q) = store.queues.get_mut(&name) else {
        return refuse(404, &format!("queue {name} not found"));
    };
    Ok(match base_path {
        "queue/len" => Some(serde_json::json!({ "length": q.msgs.len() })),
        _ if q.msgs.is_empty() => None,
        "queue/pop" => Some(q.msgs.remove(0)),
        _ => Some(q.msgs[0].clone()),
    })
}

/// Every `key=value` pair of a path's query, percent-decoded, in order.
fn query_pairs(path: &str) -> Vec<(String, String)> {
    let mut pairs = Vec::new();
    if let Some(q) = path.split_once('?').map(|(_, q)| q) {
        for pair in q.split('&') {
            if let Some((k, v)) = pair.split_once('=') {
                pairs.push((percent_decode(k), percent_decode(v)));
            }
        }
    }
    pairs
}

fn parse_query(path: &str) -> HashMap<String, String> {
    query_pairs(path).into_iter().collect()
}

/// The most `field=…&value=…` pairs one filtered read may combine on a slice.
const MAX_FILTER_PAIRS: usize = 8;

/// Every field and value a list path carries, in order, or the refusal a slice
/// answers for them: unequal numbers of each, more than MAX_FILTER_PAIRS pairs,
/// or an empty field or value among several. One pair or none is not checked,
/// and reads as it always has.
fn local_filter_pairs(path: &str) -> Result<(Vec<String>, Vec<String>), String> {
    let pairs = query_pairs(path);
    let fields: Vec<String> = pairs.iter().filter(|(k, _)| k == "field").map(|(_, v)| v.clone()).collect();
    let values: Vec<String> = pairs.iter().filter(|(k, _)| k == "value").map(|(_, v)| v.clone()).collect();
    if fields.len() <= 1 && values.len() <= 1 {
        return Ok((fields, values));
    }
    if fields.len() != values.len() {
        return Err(format!(
            "a filter pairs each field with a value by position: got {} field(s) and {} value(s)",
            fields.len(),
            values.len()
        ));
    }
    if fields.len() > MAX_FILTER_PAIRS {
        return Err(format!(
            "a filter combines at most {MAX_FILTER_PAIRS} fields, got {}",
            fields.len()
        ));
    }
    if fields.iter().chain(values.iter()).any(|s| s.is_empty()) {
        return Err("every field and value in a filter must be non-empty".to_string());
    }
    Ok((fields, values))
}

/// Whether a document's top-level fields equal EVERY pair. No pair, or a single
/// pair with an empty field, filters nothing.
fn local_matches_every(doc: &Value, fields: &[String], values: &[String]) -> bool {
    if fields.is_empty() || (fields.len() == 1 && fields[0].is_empty()) {
        return true;
    }
    fields.iter().enumerate().all(|(i, f)| {
        let got = match doc.get(f) {
            Some(Value::String(s)) => s.clone(),
            Some(other) => other.to_string(),
            None => String::new(),
        };
        got == values.get(i).map(String::as_str).unwrap_or("")
    })
}

fn call_local(_method: &str, path: &str, body: Option<Value>) -> Option<Value> {
    let q = parse_query(path);
    let base_path = path.split('?').next().unwrap_or(path);
    let mut store = local_store().lock().ok()?;

    match base_path {
        // Secrets — read from env vars in local dev (matching other SDKs)
        "secret/get" => {
            let name = q.get("name")?;
            store.secrets.get(name)
                .map(|v| Value::String(v.clone()))
                .or_else(|| std::env::var(name).ok().map(Value::String))
        }
        "secret/set" => {
            let b = body?;
            let name = b.get("name")?.as_str()?.to_string();
            let value = b.get("value")?.as_str()?.to_string();
            store.secrets.insert(name, value);
            None
        }
        "secret/delete" => {
            let name = q.get("name")?;
            store.secrets.remove(name);
            None
        }
        "secret/list" => {
            let names: Vec<Value> = store.secrets.keys().map(|k| Value::String(k.clone())).collect();
            Some(Value::Array(names))
        }
        // Cache
        "cache/get" => {
            let key = q.get("key")?;
            store.cache.get(key).cloned()
        }
        "cache/set" => {
            let b = body?;
            let key = b.get("key")?.as_str()?.to_string();
            let value = b.get("value")?.clone();
            store.cache.insert(key, value);
            None
        }
        "cache/del" => {
            let key = q.get("key")?;
            store.cache.remove(key);
            None
        }
        // NoSQL — use 1-indexed string keys matching other SDKs
        "write" => {
            let b = body?;
            let coll = b.get("collection")?.as_str()?.to_string();
            let docs = store.nosql.entry(coll).or_default();
            let next_id = docs.len() + 1;
            let key = format!("{}", next_id);
            let mut doc = b.clone();
            doc["_key"] = Value::String(key.clone());
            docs.push(doc);
            Some(serde_json::json!({"key": key}))
        }
        "read" => {
            let coll = q.get("collection")?;
            let key = q.get("key")?;
            let docs = store.nosql.get(coll)?;
            docs.iter().find(|d| d.get("_key").and_then(|k| k.as_str()) == Some(key)).cloned()
        }
        "nosql/list" => {
            let coll = q.get("collection")?;
            let order = q.get("order").map(|s| s.as_str()).unwrap_or("");
            let mut docs = store.nosql.get(coll).cloned().unwrap_or_default();
            // Order by storage key, like the platform's ordered walk. Documents are
            // held in insertion order here, which is numeric — the platform orders
            // bytewise, where "1000" precedes "999".
            //
            // `order=insertion` asks for the order they were WRITTEN instead. The
            // local store mints keys from a per-collection counter, so ordering by
            // that NUMBER is insertion order — the same guarantee a slice gets from
            // its order index, reached differently because the two mint keys
            // differently. This comparator is also what the cursor is compared
            // with below, so paging resumes in whichever order was asked for.
            //
            // THE TWO REFUSALS THE OTHER FIVE SDKs RAISE ARE ABSENT HERE, and that
            // is a limit of this dispatch rather than a decision: `call_local`
            // returns Option<Value> and has no error channel at all, so an unknown
            // `order` or an `order=insertion` combined with a field filter cannot
            // be reported as the 400 a slice answers. Neither is reachable from
            // this SDK's public API — `list_in_order` is the only caller that sends
            // an order, it hardcodes `insertion`, and it takes no filter.
            let key_index = |v: &Value| -> i64 {
                v.get("_key")
                    .and_then(|k| k.as_str())
                    .and_then(|s| s.parse::<i64>().ok())
                    .unwrap_or(0)
            };
            if order == "insertion" {
                docs.sort_by(|a, b| {
                    key_index(a).cmp(&key_index(b)).then_with(|| {
                        let ka = a.get("_key").and_then(|k| k.as_str()).unwrap_or("");
                        let kb = b.get("_key").and_then(|k| k.as_str()).unwrap_or("");
                        ka.cmp(kb)
                    })
                });
            } else {
                docs.sort_by(|a, b| {
                    let ka = a.get("_key").and_then(|k| k.as_str()).unwrap_or("");
                    let kb = b.get("_key").and_then(|k| k.as_str()).unwrap_or("");
                    ka.cmp(kb)
                });
            }
            let after = q.get("after").map(|s| s.as_str()).unwrap_or("");
            // The cursor is compared in the SAME order the rows are sorted in. A
            // bytewise `key <= after` under an insertion-ordered walk both skips
            // and repeats rows the moment the counter passes 9.
            let after_index = after.parse::<i64>().unwrap_or(0);
            let limit = q
                .get("limit")
                .and_then(|s| s.parse::<usize>().ok())
                .filter(|&n| n > 0)
                .unwrap_or(LIST_DEFAULT)
                .min(LIST_MAX);
            // The field filter is applied here too, so a local run narrows a read
            // the way a slice does instead of answering every filter identically.
            // Several pairs are ANDed. A refusal comes back as the `{"error"}`
            // body a slice's 400 carries, which is what `call_at` hands back for
            // one, so `list` reads it here exactly as it reads it there.
            let (fields, values) = match local_filter_pairs(path) {
                Ok(p) => p,
                Err(e) => {
                    return Some(serde_json::json!({
                        "error": format!("drift: backbone nosql/list: HTTP 400: {e}")
                    }))
                }
            };
            let mut out: Vec<Value> = Vec::new();
            for d in docs {
                let key = d.get("_key").and_then(|k| k.as_str()).unwrap_or("");
                if !after.is_empty() {
                    let past = if order == "insertion" {
                        let ki = key.parse::<i64>().unwrap_or(0);
                        (ki, key) > (after_index, after)
                    } else {
                        key > after
                    };
                    if !past {
                        continue;
                    }
                }
                if !local_matches_every(&d, &fields, &values) {
                    continue;
                }
                out.push(d);
                if out.len() >= limit {
                    break;
                }
            }
            Some(Value::Array(out))
        }
        "nosql/drop" => {
            let coll = q.get("collection")?;
            store.nosql.remove(coll);
            None
        }
        // Queues are NOT dispatched here: `queue_call` sends them to
        // `local_queue`, which reports a refusal, as this dispatch cannot.
        //
        // Blobs are NOT dispatched here either. They carry bytes rather than JSON and
        // are served by blob::put / blob::get against store.blobs directly.
        //
        // The arms that used to sit here keyed on a `name` query parameter, which
        // the bucket/key wire format has not sent for a long time: a put stored
        // under one string, a get looked up another, and neither reported
        // anything wrong.
        // Locks — check for existing owner before acquiring
        "lock/acquire" => {
            let b = body?;
            let name = b.get("name")?.as_str()?.to_string();
            if store.locks.contains_key(&name) {
                return None; // lock already held
            }
            let token = format!("local-{}", store.locks.len());
            store.locks.insert(name, token.clone());
            Some(serde_json::json!({"token": token}))
        }
        "lock/release" => {
            let b = body?;
            let name = b.get("name")?.as_str()?.to_string();
            store.locks.remove(&name);
            None
        }
        _ => None,
    }
}

// ===========================================================================
// Backbone — the B of the sacred A·B·C triad. The SOLE entrypoint for every
// state primitive. Reach these as `backbone::secret::get(...)`,
// `backbone::queue(...)`, `backbone::realtime::channel(...)`, etc. Nothing
// stateful lives at the crate root.
//
// Sub-modules glob-import the crate root (`use crate::*`) for the private
// transport helpers (`call`, `percent_encode`, …) and `Value` — exactly the
// target the modules used before they were nested under `backbone`.
// ===========================================================================

pub mod backbone {
    use crate::*;

    // -----------------------------------------------------------------------
    // Secret
    // -----------------------------------------------------------------------

    pub mod secret {
        use crate::*;

        /// Read order:
        ///   1. `DRIFT_SECRET_<NAME>` env var — set by the runner from the
        ///      function's Driftfile `secrets:` list. Only path that works
        ///      in production: backbone `/secret/get` is SAT-guarded and the
        ///      subprocess does not hold the SAT.
        ///   2. HTTP fallback — local-dev only. In production, returns 401.
        pub fn get(name: &str) -> String {
            let env_key = format!("DRIFT_SECRET_{}", name.to_uppercase());
            if let Ok(v) = std::env::var(&env_key) {
                return v;
            }
            match call("GET", &format!("secret/get?name={}", percent_encode(name)), None) {
                Some(Value::String(s)) => s,
                Some(v) => serde_json::to_string(&v).unwrap_or_default(),
                None => String::new(),
            }
        }

        /// The value this secret held before it was last replaced, while that
        /// is still inside its grace window; empty once it closes.
        ///
        /// For a credential the handler VERIFIES rather than presents — a
        /// webhook signing secret whose sender is still using the old value
        /// while they catch up.
        ///
        /// Empty rather than an error when there is none: that is the normal
        /// state, and the caller's branch is the same whether the secret was
        /// never replaced, was replaced long ago, or does not exist.
        pub fn previous(name: &str) -> String {
            std::env::var(format!("DRIFT_SECRET_{}_PREVIOUS", name.to_uppercase()))
                .unwrap_or_default()
        }

        pub fn set(name: &str, value: &str) {
            call("POST", "secret/set", Some(serde_json::json!({"name": name, "value": value})));
        }

        pub fn delete(name: &str) {
            call("DELETE", &format!("secret/delete?name={}", percent_encode(name)), None);
        }
    }

    // -----------------------------------------------------------------------
    // Cache
    // -----------------------------------------------------------------------

    pub mod cache {
        use crate::*;

        pub fn get(key: &str) -> Option<Value> {
            call("GET", &format!("cache/get?key={}", percent_encode(key)), None)
        }

        pub fn set(key: &str, value: Value, ttl: u64) {
            let mut payload = serde_json::json!({"key": key, "value": value});
            if ttl > 0 {
                payload["ttl"] = Value::from(ttl);
            }
            call("POST", "cache/set", Some(payload));
        }

        pub fn delete(key: &str) {
            call("DELETE", &format!("cache/del?key={}", percent_encode(key)), None);
        }
    }

    // -----------------------------------------------------------------------
    // NoSQL
    // -----------------------------------------------------------------------

    pub mod nosql {
        use crate::*;

        pub struct Collection {
            name: String,
        }

        pub fn collection(name: &str) -> Collection {
            Collection { name: name.to_string() }
        }

        impl Collection {
            pub fn insert(&self, doc: Value) -> String {
                let mut payload = serde_json::json!({"collection": self.name});
                if let Value::Object(map) = doc {
                    for (k, v) in map {
                        payload[&k] = v;
                    }
                } else {
                    payload["data"] = doc;
                }
                match call("POST", "write", Some(payload)) {
                    Some(Value::Object(m)) => {
                        m.get("key").and_then(|v| v.as_str()).unwrap_or("").to_string()
                    }
                    _ => String::new(),
                }
            }

            pub fn read(&self, key: &str) -> Option<Value> {
                call("GET", &format!("read?collection={}&key={}", percent_encode(&self.name), percent_encode(key)), None)
            }

            pub fn get(&self, id: &str) -> Option<Value> {
                let path = format!(
                    "nosql/list?collection={}&field=_id&value={}",
                    percent_encode(&self.name),
                    percent_encode(id),
                );
                match call("GET", &path, None) {
                    Some(Value::Array(arr)) if !arr.is_empty() => Some(arr[0].clone()),
                    _ => None,
                }
            }

            pub fn delete(&self, key: &str) {
                call("POST", &format!(
                    "nosql/delete?collection={}&key={}",
                    percent_encode(&self.name),
                    percent_encode(key),
                ), None);
            }

            /// Documents from the collection, optionally filtered.
            ///
            /// Every entry in `filter` must match: `{"group": "g1", "status":
            /// "open"}` returns the documents whose `group` is "g1" AND whose
            /// `status` is "open". A filter combines at most 8 fields, each
            /// compared against a top-level field of the document, and every
            /// field and value in a filter of two or more must be non-empty. A
            /// filter the platform refuses reads as no documents.
            ///
            /// Reads at the platform's own default of 100 documents. For any
            /// collection that grows, use [`list_limited`] instead: a capped read
            /// is returned with nothing marking it truncated, so a caller cannot
            /// tell "all of them" from "the first hundred".
            ///
            /// [`list_limited`]: Self::list_limited
            pub fn list(&self, filter: Option<HashMap<String, String>>) -> Vec<Value> {
                self.list_limited(filter, None)
            }

            /// Documents from the collection, with an explicit row limit.
            ///
            /// Rows come back in key order rather than newest-first, so a capped
            /// read of an append-only log is missing its most recent entries — not
            /// its oldest. The platform clamps the limit to 1000; a collection
            /// expected to outgrow that needs its reads narrowed by filter rather
            /// than widened by limit.
            ///
            /// A separate method rather than a parameter on `list`, so adding it
            /// does not break every existing caller.
            pub fn list_limited(
                &self,
                filter: Option<HashMap<String, String>>,
                limit: Option<usize>,
            ) -> Vec<Value> {
                self.list_page(filter, limit, None)
            }

            /// EVERY document matching the filter, paging until the collection is
            /// exhausted.
            ///
            /// One page is capped at 1000 and its response carries nothing marking
            /// it short, so a bigger collection cannot be read correctly with
            /// `list` alone — and because rows arrive in key order rather than
            /// newest-first, what a capped read omits is arbitrary rather than
            /// merely recent. An append-only log is the case that breaks: a Merkle
            /// root over a subset of the log is not a proof of the log.
            ///
            /// Every matching document is held in memory at once — the point for a
            /// ledger, and a cost worth knowing elsewhere, since a function runs
            /// under a memory limit.
            ///
            /// `Err` carries the storage key a page failed to advance past. A short
            /// read is the failure this method exists to prevent, so it reports
            /// rather than returning what it managed to collect.
            pub fn list_all(
                &self,
                filter: Option<HashMap<String, String>>,
            ) -> Result<Vec<Value>, String> {
                let mut all: Vec<Value> = Vec::new();
                let mut after: Option<String> = None;
                loop {
                    let rows =
                        self.list_page(filter.clone(), Some(LIST_MAX), after.as_deref());
                    let got = rows.len();
                    let next = rows
                        .last()
                        .and_then(|d| d.get("_key"))
                        .and_then(|k| k.as_str())
                        .map(|s| s.to_string());
                    all.extend(rows);
                    if got < LIST_MAX {
                        return Ok(all);
                    }
                    let next = next.ok_or_else(|| {
                        "drift: list row carries no _key, so the collection cannot be paged"
                            .to_string()
                    })?;
                    if Some(&next) == after.as_ref() {
                        return Err(format!(
                            "drift: listing {} did not advance past _key {next}",
                            self.name
                        ));
                    }
                    after = Some(next);
                }
            }

            /// ONE page of documents in the order they were WRITTEN.
            ///
            /// `list` and `list_all` return rows in storage-key order, which is
            /// not the order anything was appended: the key's counter is not
            /// zero-padded, so a collection written 1…12 comes back 10, 11, 12,
            /// 1, 2 … 9. That is stable and complete and it is not chronological,
            /// and it LOOKS chronological for the first nine documents — long
            /// enough to pass a manual test.
            ///
            /// This is the read for anything shaped like a LOG: an event store, a
            /// ledger, an activity feed, an outbox, a chat history. The platform
            /// keeps a separate order index for it, so no document key changes and
            /// a cursor you already hold keeps working.
            ///
            /// NO FILTER. The field index carries the same unpadded key the scan
            /// does, so an ordered filtered read needs a second index rather than
            /// a flag — the platform refuses the combination rather than quietly
            /// ignoring it. Filter in the caller, or keep the partition in its own
            /// collection.
            ///
            /// The platform clamps the limit to 1000. Use [`list_all_in_order`]
            /// for a log that outgrows one page.
            ///
            /// [`list_all_in_order`]: Self::list_all_in_order
            pub fn list_in_order(&self, limit: Option<usize>) -> Vec<Value> {
                self.list_page_ordered(limit, None)
            }

            /// EVERY document in the order it was written, paging until the
            /// collection is exhausted.
            ///
            /// This is what makes a hash-chained log verifiable: walking it in
            /// order is the whole point, and a Merkle root over a subset is not a
            /// proof of the set. Every document is held in memory at once — the
            /// point for a ledger, and a cost worth knowing elsewhere, since a
            /// function runs under a memory limit.
            ///
            /// `Err` carries the storage key a page failed to advance past, for
            /// the same reason `list_all` does: a short read is the failure this
            /// method exists to prevent, so it reports rather than returning what
            /// it managed to collect.
            pub fn list_all_in_order(&self) -> Result<Vec<Value>, String> {
                let mut all: Vec<Value> = Vec::new();
                let mut after: Option<String> = None;
                loop {
                    let rows = self.list_page_ordered(Some(LIST_MAX), after.as_deref());
                    let got = rows.len();
                    let next = rows
                        .last()
                        .and_then(|d| d.get("_key"))
                        .and_then(|k| k.as_str())
                        .map(|s| s.to_string());
                    all.extend(rows);
                    if got < LIST_MAX {
                        return Ok(all);
                    }
                    let next = next.ok_or_else(|| {
                        "drift: list row carries no _key, so the collection cannot be paged"
                            .to_string()
                    })?;
                    if Some(&next) == after.as_ref() {
                        return Err(format!(
                            "drift: listing {} did not advance past _key {next}",
                            self.name
                        ));
                    }
                    after = Some(next);
                }
            }

            /// One ordered list request. The cursor is still the document's own
            /// `_key`, so the wire contract does not change with the ordering.
            fn list_page_ordered(&self, limit: Option<usize>, after: Option<&str>) -> Vec<Value> {
                let mut path = format!(
                    "nosql/list?order=insertion&collection={}",
                    percent_encode(&self.name)
                );
                if let Some(n) = limit.filter(|&n| n > 0) {
                    path.push_str(&format!("&limit={n}"));
                }
                if let Some(a) = after.filter(|a| !a.is_empty()) {
                    path.push_str(&format!("&after={}", percent_encode(a)));
                }
                match call("GET", &path, None) {
                    Some(Value::Array(arr)) => arr,
                    _ => vec![],
                }
            }

            /// One list request, resuming after a storage key when given one.
            fn list_page(
                &self,
                filter: Option<HashMap<String, String>>,
                limit: Option<usize>,
                after: Option<&str>,
            ) -> Vec<Value> {
                let mut path = format!("nosql/list?collection={}", percent_encode(&self.name));
                if let Some(f) = filter {
                    for (k, v) in f {
                        path.push_str(&format!("&field={}&value={}", percent_encode(&k), percent_encode(&v)));
                    }
                }
                if let Some(n) = limit.filter(|&n| n > 0) {
                    path.push_str(&format!("&limit={n}"));
                }
                if let Some(a) = after.filter(|a| !a.is_empty()) {
                    path.push_str(&format!("&after={}", percent_encode(a)));
                }
                match call("GET", &path, None) {
                    Some(Value::Array(arr)) => arr,
                    _ => vec![],
                }
            }

            pub fn drop(&self) {
                call("POST", &format!("nosql/drop?collection={}", percent_encode(&self.name)), None);
            }
        }
    }

    // -----------------------------------------------------------------------
    // Queue
    // -----------------------------------------------------------------------

    pub struct QueueHandle {
        name: String,
    }

    pub fn queue(name: &str) -> QueueHandle {
        QueueHandle { name: name.to_string() }
    }

    // The slice takes a push's queue name in the JSON body, and a pop's, a
    // peek's and a length's in the query string and nowhere else. The `try_`
    // methods, `peek` and `len` report a refusal as an error; `push` and `pop`
    // keep the forgiving shape they have always had.
    impl QueueHandle {
        /// Append `body`, which must be a JSON object, creating the queue on
        /// first use, and return the id the slice gave the message. `None`
        /// when the push was refused; `try_push` says why.
        pub fn push(&self, body: Value) -> Option<String> {
            self.try_push(body).ok()
        }

        /// Append `body`, which must be a JSON object, creating the queue on
        /// first use, and return the id the slice gave the message.
        pub fn try_push(&self, body: Value) -> Result<String, String> {
            let reply = queue_call("POST", "queue/push", Some(serde_json::json!({"queue": self.name, "body": body})))?;
            match reply.as_ref().and_then(|r| r.get("id")).and_then(|id| id.as_str()) {
                Some(id) if !id.is_empty() => Ok(id.to_string()),
                // The message may well have been queued; an empty id would
                // invite a retry that queues it twice.
                _ => Err(format!(
                    "drift: queue {:?}: the push may have succeeded, but its reply carries no id: {reply:?}",
                    self.name
                )),
            }
        }

        /// The body of the message at the head of the queue, removed. `None`
        /// when the queue is empty and also when the call failed; `try_pop`
        /// tells the two apart.
        pub fn pop(&self) -> Option<Value> {
            self.try_pop().ok().flatten()
        }

        /// The body of the message at the head of the queue, removed. `Ok(None)`
        /// when the queue is empty; an error when nothing was ever pushed to it,
        /// because on a slice it does not exist.
        pub fn try_pop(&self) -> Result<Option<Value>, String> {
            self.head("POST", "queue/pop")
        }

        /// The body of the message at the head of the queue, left in place, with
        /// the same answers as `try_pop`.
        pub fn peek(&self) -> Result<Option<Value>, String> {
            self.head("GET", "queue/peek")
        }

        /// How many messages are waiting. An error when nothing was ever pushed
        /// to the queue, as for `try_pop`.
        pub fn len(&self) -> Result<usize, String> {
            let path = format!("queue/len?queue={}", percent_encode(&self.name));
            match queue_call("GET", &path, None)? {
                Some(v) => v
                    .get("length")
                    .and_then(|n| n.as_u64())
                    .map(|n| n as usize)
                    .ok_or_else(|| format!("drift: queue {:?}: the length reply is unreadable: {v}", self.name)),
                None => Err(format!("drift: queue {:?}: the length reply is empty", self.name)),
            }
        }

        /// Whether no message is waiting, with the same errors as `len`.
        pub fn is_empty(&self) -> Result<bool, String> {
            self.len().map(|n| n == 0)
        }

        /// A pop or a peek: the slice answers with the message's envelope
        /// ({id, body, timestamp}), of which the caller wants the body.
        fn head(&self, method: &str, path: &str) -> Result<Option<Value>, String> {
            let path = format!("{path}?queue={}", percent_encode(&self.name));
            match queue_call(method, &path, None)? {
                None => Ok(None),
                Some(msg) => match msg.get("body") {
                    Some(body) => Ok(Some(body.clone())),
                    None => Err(format!("drift: queue {:?}: the reply is not a message: {msg}", self.name)),
                },
            }
        }
    }

    // -----------------------------------------------------------------------
    // Blob
    // -----------------------------------------------------------------------

    pub mod blob {
        use crate::*;

        fn split_bucket_key(name: &str) -> (&str, &str) {
            match name.split_once('/') {
                Some((b, k)) => (b, k),
                None => ("default", name),
            }
        }

        /// The address a blob is stored under in local dev: the same bucket and
        /// key the wire format carries, joined.
        ///
        /// ONE function, called by both put and get, because the bug it replaces
        /// was exactly two call sites deriving a key separately and drifting
        /// apart. A put that stores under an address a get does not build is
        /// silent in both directions.
        fn local_address(name: &str) -> String {
            let (bucket, key) = split_bucket_key(name);
            format!("{}/{}", bucket, key)
        }

        pub fn put(name: &str, data: &[u8], content_type: Option<&str>) {
            if get_backbone_url().is_empty() {
                local_store()
                    .lock()
                    .unwrap()
                    .blobs
                    .insert(local_address(name), data.to_vec());
                return;
            }
            let (bucket, key) = split_bucket_key(name);
            let path = format!("blob/put?bucket={}&key={}", percent_encode(bucket), percent_encode(key));
            call_raw("POST", &path, data, content_type.unwrap_or("application/octet-stream"));
        }

        pub fn get(name: &str) -> Option<Vec<u8>> {
            if get_backbone_url().is_empty() {
                return local_store().lock().unwrap().blobs.get(&local_address(name)).cloned();
            }
            let (bucket, key) = split_bucket_key(name);
            let path = format!("blob/get?bucket={}&key={}", percent_encode(bucket), percent_encode(key));
            let base = get_backbone_url();
            let url = format!("{}/{}", base, path);
            match ureq::get(&url).call() {
                Ok(r) => {
                    let mut buf = Vec::new();
                    let _ = r.into_reader().read_to_end(&mut buf);
                    Some(buf)
                }
                Err(_) => None,
            }
        }
    }

    // -----------------------------------------------------------------------
    // Lock
    // -----------------------------------------------------------------------

    pub mod lock {
        use crate::*;

        pub fn acquire(name: &str, ttl: u64) -> String {
            match call("POST", "lock/acquire", Some(serde_json::json!({"name": name, "ttl": ttl}))) {
                Some(Value::Object(m)) => {
                    m.get("token").and_then(|v| v.as_str()).unwrap_or("").to_string()
                }
                _ => String::new(),
            }
        }

        pub fn release(name: &str, token: &str) {
            call("POST", "lock/release", Some(serde_json::json!({"name": name, "token": token})));
        }
    }

    // -----------------------------------------------------------------------
    // Realtime — pub/sub fan-out over the slice's Canvas WebSocket hub.
    // -----------------------------------------------------------------------
    //
    // Subscribers connect over WebSocket at the Canvas route /realtime/<name>;
    // publish fans a message out to every connected subscriber.

    pub mod realtime {
        use crate::*;

        pub struct Channel {
            name: String,
        }

        pub fn channel(name: &str) -> Channel {
            Channel { name: name.to_string() }
        }

        impl Channel {
            /// Publish a message to every subscriber. Returns the recipient count.
            pub fn publish(&self, message: Value) -> u64 {
                match call("POST", "realtime/publish", Some(serde_json::json!({"channel": self.name, "message": message}))) {
                    Some(Value::Object(m)) => m.get("recipients").and_then(|v| v.as_u64()).unwrap_or(0),
                    _ => 0,
                }
            }

            /// The number of subscribers currently connected to this channel.
            pub fn presence(&self) -> u64 {
                match call("GET", &format!("realtime/presence?channel={}", percent_encode(&self.name)), None) {
                    Some(Value::Object(m)) => m.get("present").and_then(|v| v.as_u64()).unwrap_or(0),
                    _ => 0,
                }
            }
        }
    }

    // ─── SQL ──────────────────────────────────────────────────────────────
    //
    // Per-slice SQLite databases addressed by name. Wire shape: one JSON
    // envelope per call ({db, sql, args, tx?}). See docs/memos/backbone-sql.md.
    //
    //   let db = drift_sdk::backbone::sql("clinic");
    //   let rows = db.query("SELECT * FROM appointments WHERE slot >= ?", &[json!("2026-05-01")]);
    //   let res = db.execute("INSERT INTO appointments(...) VALUES(?, ?)", &[json!("alice"), json!("10:00")]);

    pub struct SqlDb {
        name: String,
    }

    pub struct SqlTx {
        db: String,
        token: String,
    }

    #[derive(Default, Debug, Clone)]
    pub struct SqlExecResult {
        pub rows_affected: i64,
        pub last_insert_id: i64,
    }

    fn sql_exec_from_value(v: Option<Value>) -> SqlExecResult {
        let v = match v {
            Some(v) => v,
            None => return SqlExecResult::default(),
        };
        SqlExecResult {
            rows_affected: v.get("rows_affected").and_then(|x| x.as_i64()).unwrap_or(0),
            last_insert_id: v.get("last_insert_id").and_then(|x| x.as_i64()).unwrap_or(0),
        }
    }

    pub fn sql<S: Into<String>>(name: S) -> SqlDb {
        SqlDb { name: name.into() }
    }

    fn sql_rows(resp: Option<Value>) -> Vec<serde_json::Map<String, Value>> {
        let v = match resp {
            Some(v) => v,
            None => return vec![],
        };
        let cols: Vec<String> = v
            .get("columns")
            .and_then(|c| c.as_array())
            .map(|a| {
                a.iter()
                    .filter_map(|e| e.as_str().map(|s| s.to_string()))
                    .collect()
            })
            .unwrap_or_default();
        let rows = v.get("rows").and_then(|r| r.as_array()).cloned().unwrap_or_default();
        rows.into_iter()
            .map(|row| {
                let arr = row.as_array().cloned().unwrap_or_default();
                let mut map = serde_json::Map::new();
                for (i, col) in cols.iter().enumerate() {
                    map.insert(col.clone(), arr.get(i).cloned().unwrap_or(Value::Null));
                }
                map
            })
            .collect()
    }

    impl SqlDb {
        pub fn query(&self, sql: &str, args: &[Value]) -> Vec<serde_json::Map<String, Value>> {
            let body = serde_json::json!({"db": self.name, "sql": sql, "args": args});
            sql_rows(call("POST", "sql/query", Some(body)))
        }

        pub fn execute(&self, sql: &str, args: &[Value]) -> SqlExecResult {
            let body = serde_json::json!({"db": self.name, "sql": sql, "args": args});
            sql_exec_from_value(call("POST", "sql/execute", Some(body)))
        }

        pub fn begin(&self) -> Option<SqlTx> {
            let body = serde_json::json!({"db": self.name});
            let raw = call("POST", "sql/begin", Some(body))?;
            let token = raw.get("tx")?.as_str()?.to_string();
            Some(SqlTx { db: self.name.clone(), token })
        }
    }

    impl SqlTx {
        pub fn query(&self, sql: &str, args: &[Value]) -> Vec<serde_json::Map<String, Value>> {
            let body = serde_json::json!({"db": self.db, "sql": sql, "args": args, "tx": self.token});
            sql_rows(call("POST", "sql/query", Some(body)))
        }

        pub fn execute(&self, sql: &str, args: &[Value]) -> SqlExecResult {
            let body = serde_json::json!({"db": self.db, "sql": sql, "args": args, "tx": self.token});
            sql_exec_from_value(call("POST", "sql/execute", Some(body)))
        }

        pub fn commit(&self) {
            let _ = call("POST", "sql/commit", Some(serde_json::json!({"tx": self.token})));
        }

        pub fn rollback(&self) {
            let _ = call("POST", "sql/rollback", Some(serde_json::json!({"tx": self.token})));
        }
    }
}

// ===========================================================================
// Deed — Drift's 4th architectural pillar (identity, verified), a peer of
// Backbone/Atomic/Canvas. Five primitives: `deed::keyauth`, `deed::jwt`,
// `deed::vault`, `deed::link`, `deed::pocket`.
//
// KeyAuth: passwordless Ed25519 device-key auth. JWT: general-purpose HS256
// sign/verify (KeyAuth mints its own tokens through it). Vault: an
// account-key-wrapped keyring. Link: multi-device attestation / enrollment /
// revocation. Pocket: E2EE per-identity app data, JWT-gated.
//
// Not to be confused with slice-to-slice linking (`edge::slice(name)` /
// `caller_slice`, further below) — that's inter-slice networking, declared
// under edge.links in the Driftfile. `deed::link` enrolls another DEVICE for
// the same identity; it has nothing to do with calling another SLICE.
// ===========================================================================

pub mod deed {
    use crate::*;

    /// Error returned by any Deed call: a transport failure, a non-2xx HTTP
    /// status, or a malformed response body. Deed calls always fail loudly —
    /// in particular, a "not found" Get (e.g. `vault::get` on a uid that
    /// never wrote a backup, `pocket::get` on an unknown key) comes back as
    /// `Err`, never a silent `Ok(None)`/default.
    #[derive(Debug, Clone)]
    pub struct DeedError {
        pub message: String,
    }

    impl std::fmt::Display for DeedError {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(f, "{}", self.message)
        }
    }
    impl std::error::Error for DeedError {}

    fn unavailable() -> DeedError {
        DeedError {
            message: "drift deed: requires a running slice (DEED_URL) — not available in local dev".to_string(),
        }
    }

    fn malformed(path: &str) -> DeedError {
        DeedError { message: format!("drift deed: malformed {} response", path) }
    }

    // Shared low-level transport for every Deed primitive except `jwt` (which
    // keeps using the crate-root `deed_call()`, a softer None-on-miss
    // convention — see its doc comment — since it's a straight relocation,
    // not a rewrite). Unlike `call()`/`deed_call()`, this propagates transport
    // failures and HTTP >= 400 as `Err` rather than collapsing them into
    // `None` — the stricter convention Deed's wire contract calls for. `token`
    // is `Some(...)` only for `pocket`, whose routes are JWT-gated; every
    // other Deed primitive passes `None`. Deed has its own listener/port
    // (DEED_URL), separate from Backbone's.
    fn request(method: &str, path: &str, token: Option<&str>, body: Option<&Value>) -> Result<Value, DeedError> {
        let base = get_deed_url();
        if base.is_empty() {
            return Err(unavailable());
        }
        let url = format!("{}/{}", base, path);

        let mut req = ureq::request(method, &url);
        if let Some(t) = token {
            req = req.set("Authorization", &format!("Bearer {}", t));
        }

        let result = if let Some(b) = body {
            req.set("Content-Type", "application/json")
                .send_string(&serde_json::to_string(b).unwrap_or_default())
        } else {
            req.call()
        };

        match result {
            Ok(resp) => {
                if resp.status() == 204 {
                    return Ok(Value::Null);
                }
                let text = resp.into_string().unwrap_or_default();
                if text.is_empty() {
                    return Ok(Value::Null);
                }
                serde_json::from_str(&text)
                    .map_err(|e| DeedError { message: format!("drift deed: parse {} response: {}", path, e) })
            }
            Err(ureq::Error::Status(code, resp)) => {
                let text = resp.into_string().unwrap_or_default();
                let text = text.trim();
                let msg = if text.is_empty() {
                    format!("drift deed: {} HTTP {}", path, code)
                } else {
                    format!("drift deed: {} HTTP {}: {}", path, code, text)
                };
                Err(DeedError { message: msg })
            }
            Err(ureq::Error::Transport(t)) => {
                Err(DeedError { message: format!("drift deed: {} request failed: {}", path, t) })
            }
        }
    }

    // -----------------------------------------------------------------------
    // KeyAuth — passwordless Ed25519 device-key auth.
    // -----------------------------------------------------------------------
    //
    // The client holds a keypair; the pubkey IS its identity — no accounts,
    // no passwords, no email. `challenge` mints a one-time nonce; the client
    // signs the canonical {domain,nonce,pubkey}; `verify` checks the
    // signature and issues this slice's session JWT (sub = pubkey). The
    // slice stores nothing about the user — it just verifies a signature.
    // The client half (keygen + signing + recovery-phrase derivation) is a
    // small browser library; see the @ondrift/keyauth memo.

    pub mod keyauth {
        use super::{request, malformed, DeedError};

        /// Returns a one-time login nonce for the given Ed25519 public key
        /// (32-byte hex). Single-use, short-TTL, cache-backed in the slice.
        pub fn challenge(pubkey: &str) -> Result<String, DeedError> {
            let resp = request("POST", "keyauth/challenge", None, Some(&serde_json::json!({"pubkey": pubkey})))?;
            resp.get("nonce")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .ok_or_else(|| malformed("keyauth/challenge"))
        }

        /// Checks the client's signature over the canonical
        /// {domain,nonce,pubkey} and, on success, returns this slice's
        /// session JWT (sub = the pubkey). `domain` namespaces the signature
        /// to your app (e.g. "myapp-auth-v1") so a signature for one
        /// app/slice can't be replayed at another — the client must sign the
        /// same domain. A bad/expired/absent challenge or a bad signature
        /// returns `Err`.
        pub fn verify(pubkey: &str, sig: &str, domain: &str) -> Result<String, DeedError> {
            let resp = request(
                "POST",
                "keyauth/verify",
                None,
                Some(&serde_json::json!({"pubkey": pubkey, "sig": sig, "domain": domain})),
            )?;
            resp.get("token")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .ok_or_else(|| malformed("keyauth/verify"))
        }
    }

    // -----------------------------------------------------------------------
    // JWT primitive
    // -----------------------------------------------------------------------
    //
    // HS256 minting + verification, signed with the slice's per-slice JKey. The
    // signing key never leaves the slice's backbone process; all operations flow
    // through loopback HTTP to backbone /jwt/{sign,verify,slice-id}.
    //
    // Design: internal/todo/slice-jwt-primitive.md.

    pub mod jwt {
        use crate::*;

        /// Returned by [`verify`] when the token fails validation. `reason`
        /// is one of the stable wire strings: `malformed`, `bad_signature`,
        /// `expired`, `not_yet_valid`, `wrong_algorithm`, `wrong_issuer`,
        /// `wrong_audience`, `invalid_claims`, `missing_exp`, `internal_error`.
        #[derive(Debug, Clone)]
        pub struct JWTError {
            pub reason: String,
        }

        impl std::fmt::Display for JWTError {
            fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                write!(f, "jwt verify: {}", self.reason)
            }
        }
        impl std::error::Error for JWTError {}

        /// Claims payload. Matches the wire shape of the platform JWT
        /// primitive — standard fields plus an open `custom` map.
        #[derive(Debug, Clone, Default)]
        pub struct Claims {
            pub sub: Option<String>,
            pub iat: Option<i64>,
            pub exp: Option<i64>,
            pub nbf: Option<i64>,
            pub iss: Option<String>,
            pub aud: Option<Vec<String>>,
            pub jti: Option<String>,
            pub custom: Option<Value>,
        }

        /// Sign a JWT with the slice's HS256 JKey. `exp` is required;
        /// `iat`, `iss`, and `jti` are auto-set when `None`.
        pub fn issue(claims: Claims) -> String {
            let mut body = serde_json::Map::new();
            if let Some(v) = claims.sub    { body.insert("sub".to_string(),    Value::String(v)); }
            if let Some(v) = claims.iat    { body.insert("iat".to_string(),    Value::from(v)); }
            if let Some(v) = claims.exp    { body.insert("exp".to_string(),    Value::from(v)); }
            if let Some(v) = claims.nbf    { body.insert("nbf".to_string(),    Value::from(v)); }
            if let Some(v) = claims.iss    { body.insert("iss".to_string(),    Value::String(v)); }
            if let Some(v) = claims.aud    { body.insert("aud".to_string(),    serde_json::to_value(v).unwrap_or(Value::Null)); }
            if let Some(v) = claims.jti    { body.insert("jti".to_string(),    Value::String(v)); }
            if let Some(v) = claims.custom { body.insert("custom".to_string(), v); }

            match deed_call("POST", "jwt/sign", Some(Value::Object(body))) {
                Some(Value::Object(m)) => {
                    m.get("token").and_then(|v| v.as_str()).unwrap_or_default().to_string()
                }
                _ => String::new(),
            }
        }

        /// Validate a token. Returns parsed claims on success; `JWTError`
        /// on validation failure.
        pub fn verify(token: &str, audience: Option<&str>, allowed_issuer: Option<&str>) -> Result<Value, JWTError> {
            let mut body = serde_json::Map::new();
            body.insert("token".to_string(), Value::String(token.to_string()));
            if let Some(a) = audience { body.insert("audience".to_string(), Value::String(a.to_string())); }
            if let Some(i) = allowed_issuer { body.insert("allowed_issuer".to_string(), Value::String(i.to_string())); }

            let resp = match deed_call("POST", "jwt/verify", Some(Value::Object(body))) {
                Some(Value::Object(m)) => m,
                _ => return Err(JWTError { reason: "internal_error".to_string() }),
            };
            let valid = resp.get("valid").and_then(|v| v.as_bool()).unwrap_or(false);
            if !valid {
                let reason = resp.get("reason").and_then(|v| v.as_str()).unwrap_or("internal_error").to_string();
                return Err(JWTError { reason });
            }
            Ok(resp.get("claims").cloned().unwrap_or(Value::Null))
        }

        /// The slice's auto-set issuer string ("drift-slice-<user>-<slice>").
        pub fn slice_id() -> String {
            match deed_call("GET", "jwt/slice-id", None) {
                Some(Value::Object(m)) => {
                    m.get("slice_id").and_then(|v| v.as_str()).unwrap_or_default().to_string()
                }
                _ => String::new(),
            }
        }
    }

    // -----------------------------------------------------------------------
    // Vault — zero-knowledge recovery store.
    // -----------------------------------------------------------------------
    //
    // Opaque, user-scoped, append-only. The client encrypts the blob under a
    // key derived from its recovery phrase (which the slice NEVER sees), so
    // Drift stores the backup but cannot read it. Scoped to a uid the caller
    // supplies — typically the authenticated KeyAuth pubkey. Backed by
    // Deed's own dedicated routes: AES-256-GCM at rest (defense in depth
    // only — the blob must already be ciphertext before it arrives, since
    // that's the actual source of Vault's confidentiality guarantee) and a
    // per-item size quota. No Driftfile declaration needed.

    pub mod vault {
        use crate::*;
        use super::{request, malformed, DeedError};

        /// Appends an opaque encrypted backup blob for uid. Append-only (a
        /// new version each call); `get` returns the newest.
        pub fn put(uid: &str, blob: Value) -> Result<(), DeedError> {
            request("POST", "deed/vault/put", None, Some(&serde_json::json!({"uid": uid, "blob": blob})))?;
            Ok(())
        }

        /// Returns the most recent backup blob for uid. Returns `Err` if uid
        /// has never written one (same "not found is an error" convention as
        /// `backbone::secret::get`/`backbone::blob::get`).
        pub fn get(uid: &str) -> Result<Value, DeedError> {
            let resp = request("GET", &format!("deed/vault/get?uid={}", percent_encode(uid)), None, None)?;
            resp.get("blob").cloned().ok_or_else(|| malformed("deed/vault/get"))
        }
    }

    // -----------------------------------------------------------------------
    // Link — multi-device continuity.
    // -----------------------------------------------------------------------
    //
    // Generalizes the enroll/attest/revoke pattern so an identity's KeyAuth
    // session can move to a second, third, ... device. The signature
    // parameters below (sig, attesting_pubkey, etc.) are produced entirely
    // client-side — this SDK only forwards them, the same way
    // `keyauth::verify` forwards a signature it never computes itself. The
    // one rule the whole design rests on: Deed verifies, it never decides —
    // a device is only ever added on the strength of a signature from a
    // device already active in the identity's registry.
    //
    // Not to be confused with slice-to-slice linking (`edge::slice(name)` /
    // `caller_slice`, at the crate root) — this Link enrolls a DEVICE for
    // one identity, it does not call another slice.

    pub mod link {
        use super::{request, malformed, DeedError};

        /// Returned by [`complete`]. `identity`/`sealed` are set only once
        /// `status == "attested"` (`sealed` only if `attest_with_seal`
        /// supplied one).
        #[derive(Debug, Clone, Default)]
        pub struct LinkStatus {
            pub status: String,
            pub identity: Option<String>,
            pub sealed: Option<String>,
        }

        /// Returned by [`session_info`].
        #[derive(Debug, Clone, Default)]
        pub struct LinkSessionInfo {
            pub new_pubkey: String,
            pub metadata: Option<String>,
        }

        /// Starts a device-linking session for a not-yet-enrolled device's
        /// pubkey (usually carried in a QR code alongside the pubkey).
        /// Returns a session ID for an already-active device to present to
        /// `attest`.
        pub fn begin(pubkey: &str) -> Result<String, DeedError> {
            let resp = request("POST", "deed/link/begin", None, Some(&serde_json::json!({"pubkey": pubkey})))?;
            resp.get("session_id")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .ok_or_else(|| malformed("deed/link/begin"))
        }

        /// Same as [`begin`], but also registers an opaque `metadata`
        /// string an attesting device can read back via `session_info` —
        /// e.g. an ephemeral key it should seal a payload for. Deed never
        /// interprets it. A separate function rather than an added
        /// parameter to `begin`, since Rust has no default arguments and
        /// changing `begin`'s arity would break every existing caller.
        pub fn begin_with_metadata(pubkey: &str, metadata: &str) -> Result<String, DeedError> {
            let resp = request(
                "POST",
                "deed/link/begin",
                None,
                Some(&serde_json::json!({"pubkey": pubkey, "metadata": metadata})),
            )?;
            resp.get("session_id")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .ok_or_else(|| malformed("deed/link/begin"))
        }

        /// A read-only, repeatable peek at a pending session — what an
        /// attesting device (which only ever learns `session_id`, from a
        /// scanned/typed code) uses to learn `new_pubkey` (`attest`'s
        /// message is verified server-side against the session's own
        /// stored value, never the request body, so the attester has to
        /// reconstruct it exactly) and whatever opaque metadata the
        /// joining device passed to `begin`.
        pub fn session_info(session_id: &str) -> Result<LinkSessionInfo, DeedError> {
            let resp = request(
                "POST",
                "deed/link/session",
                None,
                Some(&serde_json::json!({"session_id": session_id})),
            )?;
            Ok(LinkSessionInfo {
                new_pubkey: resp.get("new_pubkey").and_then(|v| v.as_str()).unwrap_or_default().to_string(),
                metadata: resp.get("metadata").and_then(|v| v.as_str()).map(|s| s.to_string()),
            })
        }

        /// Renders `text` (in practice, a Link session ID) as a scannable
        /// QR code, returning inline SVG markup. Pure rendering — no
        /// session or identity involvement, so it works for any short
        /// string.
        pub fn qr(text: &str) -> Result<String, DeedError> {
            let resp = request("POST", "deed/link/qr", None, Some(&serde_json::json!({"text": text})))?;
            resp.get("svg")
                .and_then(|v| v.as_str())
                .map(|s| s.to_string())
                .ok_or_else(|| malformed("deed/link/qr"))
        }

        /// Has an already-active device vouch for the session's pending
        /// device. `sig` is the client's signature over the canonical
        /// {domain,identity,new_pubkey} message — computed client-side,
        /// never by this SDK.
        pub fn attest(identity: &str, session_id: &str, attesting_pubkey: &str, sig: &str) -> Result<(), DeedError> {
            request(
                "POST",
                "deed/link/attest",
                None,
                Some(&serde_json::json!({
                    "identity": identity,
                    "session_id": session_id,
                    "attesting_pubkey": attesting_pubkey,
                    "sig": sig,
                })),
            )?;
            Ok(())
        }

        /// Same as [`attest`], but also carries an opaque `sealed` string
        /// relayed back once [`complete`] reports "attested" — e.g. a
        /// payload end-to-end-encrypted for whatever key the joiner
        /// published as `begin_with_metadata`'s metadata. Deed only relays
        /// it, never opens it. A separate function for the same arity
        /// reason as `begin_with_metadata`.
        pub fn attest_with_seal(identity: &str, session_id: &str, attesting_pubkey: &str, sig: &str, sealed: &str) -> Result<(), DeedError> {
            request(
                "POST",
                "deed/link/attest",
                None,
                Some(&serde_json::json!({
                    "identity": identity,
                    "session_id": session_id,
                    "attesting_pubkey": attesting_pubkey,
                    "sig": sig,
                    "sealed": sealed,
                })),
            )?;
            Ok(())
        }

        /// Polls a session the new device started with `begin`, returning
        /// whether an active device has attested it yet.
        pub fn complete(session_id: &str) -> Result<LinkStatus, DeedError> {
            let resp = request("POST", "deed/link/complete", None, Some(&serde_json::json!({"session_id": session_id})))?;
            Ok(LinkStatus {
                status: resp.get("status").and_then(|v| v.as_str()).unwrap_or_default().to_string(),
                identity: resp.get("identity").and_then(|v| v.as_str()).map(|s| s.to_string()),
                sealed: resp.get("sealed").and_then(|v| v.as_str()).map(|s| s.to_string()),
            })
        }

        /// Deactivates target_pubkey in identity's device registry. Any
        /// currently-active device may revoke another (or itself);
        /// revoking_pubkey is the device doing the revoking, sig its
        /// signature over the canonical {domain,identity,target_pubkey}
        /// message.
        pub fn revoke(identity: &str, target_pubkey: &str, revoking_pubkey: &str, sig: &str) -> Result<(), DeedError> {
            request(
                "POST",
                "deed/link/revoke",
                None,
                Some(&serde_json::json!({
                    "identity": identity,
                    "target_pubkey": target_pubkey,
                    "revoking_pubkey": revoking_pubkey,
                    "sig": sig,
                })),
            )?;
            Ok(())
        }
    }

    // -----------------------------------------------------------------------
    // Pocket — E2EE per-identity app data.
    // -----------------------------------------------------------------------
    //
    // An app's actual data — E2EE, content-keyed, following an identity
    // across every device Link has enrolled. The crypto work happens
    // entirely client-side before anything reaches this primitive; Pocket
    // never encrypts or decrypts the payload itself. Every call takes
    // `token` explicitly (the JWT `keyauth::verify` returned) rather than
    // holding hidden session state — matching the rest of this SDK's
    // stateless posture inside an Atomic function invocation. The token's
    // sub is the only identity a call can read or write under; there is no
    // way to name a different one. Every call sends it as an
    // `Authorization: Bearer <token>` header.

    pub mod pocket {
        use crate::*;
        use super::{request, malformed, DeedError};

        /// Stores blob under key for whichever identity token resolves to.
        pub fn set(token: &str, key: &str, blob: Value) -> Result<(), DeedError> {
            request(
                "POST",
                "deed/pocket/set",
                Some(token),
                Some(&serde_json::json!({"key": key, "blob": blob})),
            )?;
            Ok(())
        }

        /// Returns the blob stored under key for token's identity. Returns
        /// `Err` if no such key exists.
        pub fn get(token: &str, key: &str) -> Result<Value, DeedError> {
            let resp = request(
                "GET",
                &format!("deed/pocket/get?key={}", percent_encode(key)),
                Some(token),
                None,
            )?;
            resp.get("blob").cloned().ok_or_else(|| malformed("deed/pocket/get"))
        }

        /// Removes key for token's identity. Returns `Err` if no such key
        /// exists.
        pub fn delete(token: &str, key: &str) -> Result<(), DeedError> {
            request("POST", "deed/pocket/delete", Some(token), Some(&serde_json::json!({"key": key})))?;
            Ok(())
        }

        /// Returns every key stored under token's identity — never another
        /// identity's, even by guessing.
        pub fn list(token: &str) -> Result<Vec<String>, DeedError> {
            let resp = request("GET", "deed/pocket/list", Some(token), None)?;
            match resp {
                Value::Array(arr) => Ok(arr
                    .into_iter()
                    .filter_map(|v| v.as_str().map(|s| s.to_string()))
                    .collect()),
                _ => Err(malformed("deed/pocket/list")),
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

pub fn log(msg: &str) {
    eprintln!("{}", msg);
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

/// Default 30-second timeout. A function calling a hung remote
/// shouldn't hold an Atomic invocation open longer than this; the
/// runner's per-invocation deadline is the absolute ceiling. Use
/// `http_request_with_timeout` to override.
pub fn http_request(
    method: &str,
    url: &str,
    headers: Option<HashMap<String, String>>,
    body: Option<&str>,
) -> (u16, String) {
    http_request_with_timeout(method, url, headers, body, std::time::Duration::from_secs(30))
}

/// `http_request` with a caller-supplied timeout.
pub fn http_request_with_timeout(
    method: &str,
    url: &str,
    headers: Option<HashMap<String, String>>,
    body: Option<&str>,
    timeout: std::time::Duration,
) -> (u16, String) {
    // TLS is an opt-in feature so the default build stays pure Rust (no C
    // cross-compiler needed to deploy). Fail loudly rather than silently
    // when a function tries HTTPS without it.
    #[cfg(not(feature = "tls"))]
    if url.starts_with("https://") {
        return (
            0,
            "drift-sdk: outbound HTTPS needs the \"tls\" feature — set \
             drift-sdk = { ..., features = [\"tls\"] } in Cargo.toml (deploys then \
             require a C cross-toolchain such as zig)."
                .to_string(),
        );
    }

    let agent = ureq::AgentBuilder::new()
        .timeout(timeout)
        .build();
    let mut req = agent.request(method, url);

    if let Some(hdrs) = headers {
        for (k, v) in hdrs {
            req = req.set(&k, &v);
        }
    }

    let result = if let Some(b) = body {
        req.send_string(b)
    } else {
        req.call()
    };

    match result {
        Ok(r) => {
            let status = r.status();
            let text = r.into_string().unwrap_or_default();
            (status, text)
        }
        Err(ureq::Error::Status(code, resp)) => {
            let text = resp.into_string().unwrap_or_default();
            (code, text)
        }
        Err(_) => (0, String::new()),
    }
}

// ─── SSE (Server-Sent Events) ────────────────────────────────────────────────

/// Entry point for SSE streaming functions.
///
/// ```rust,no_run
/// // Driftfile: name get:events, handler get_events, stream sse
/// drift_sdk::run_sse(|req, emit| {
///     for i in 0..10 {
///         emit("counter", &serde_json::json!({"value": i}));
///         std::thread::sleep(std::time::Duration::from_secs(1));
///     }
/// });
/// ```
pub fn run_sse<F>(handler: F)
where
    F: Fn(serde_json::Value, &dyn Fn(&str, &serde_json::Value)) + Send + Sync + 'static,
{
    if std::env::var("DRIFT_RUNTIME").is_ok() {
        run_sse_deployed(&handler);
    } else {
        run_sse_local(handler);
    }
}

fn run_sse_deployed<F>(handler: &F)
where
    F: Fn(serde_json::Value, &dyn Fn(&str, &serde_json::Value)),
{
    let input = {
        let mut buf = String::new();
        std::io::Read::read_to_string(&mut std::io::stdin(), &mut buf).ok();
        buf
    };
    let req: serde_json::Value = serde_json::from_str(&input).unwrap_or_default();

    let emit = |event: &str, data: &serde_json::Value| {
        let mut stdout = std::io::stdout().lock();
        if !event.is_empty() {
            let _ = write!(stdout, "event: {}\n", event);
        }
        let _ = write!(stdout, "data: {}\n\n", data);
        let _ = stdout.flush();
    };

    handler(req, &emit);
}

// Local-dev SSE server. Mirrors run_local's request-parsing plumbing but
// writes the response chunks directly to the TCP stream and flushes after
// every emit, so SSE events reach the client immediately rather than being
// buffered until process exit.
fn run_sse_local<F>(handler: F)
where
    F: Fn(serde_json::Value, &dyn Fn(&str, &serde_json::Value)) + Send + Sync + 'static,
{
    let port: u16 = std::env::var("PORT")
        .unwrap_or_else(|_| "8080".into())
        .parse()
        .unwrap_or(8080);

    let listener = TcpListener::bind(format!("0.0.0.0:{}", port)).unwrap();
    eprintln!("drift-sdk: local SSE server starting on :{}", port);

    for stream in listener.incoming() {
        let mut stream = match stream {
            Ok(s) => s,
            Err(_) => continue,
        };

        let mut reader = BufReader::new(match stream.try_clone() {
            Ok(s) => s,
            Err(_) => continue,
        });

        let mut request_line = String::new();
        if reader.read_line(&mut request_line).is_err() {
            continue;
        }
        let parts: Vec<&str> = request_line.trim().splitn(3, ' ').collect();
        if parts.len() < 2 {
            continue;
        }
        let _ = &parts[0]; // method is no longer surfaced to handlers
        let path_str = parts[1].to_string();

        let mut headers = serde_json::Map::new();
        let mut content_length: usize = 0;
        loop {
            let mut line = String::new();
            if reader.read_line(&mut line).is_err() {
                break;
            }
            let trimmed = line.trim().to_string();
            if trimmed.is_empty() {
                break;
            }
            if let Some((k, v)) = trimmed.split_once(": ") {
                if k.eq_ignore_ascii_case("content-length") {
                    content_length = v.parse().unwrap_or(0);
                }
                headers.insert(k.to_lowercase(), Value::String(v.to_string()));
            }
        }

        let body = if content_length > 0 {
            let mut buf = vec![0u8; content_length];
            let _ = reader.read_exact(&mut buf);
            let raw = String::from_utf8_lossy(&buf).to_string();
            match serde_json::from_str::<Value>(&raw) {
                Ok(v) => v,
                Err(_) => Value::String(raw),
            }
        } else {
            Value::Null
        };

        let (path, query) = match path_str.split_once('?') {
            Some((p, q)) => (p.to_string(), q.to_string()),
            None => (path_str, String::new()),
        };

        let req = serde_json::json!({
            "path": path,
            "headers": Value::Object(headers),
            "query": query,
            "body": body,
        });

        // Write SSE response headers.
        let head = "HTTP/1.1 200 OK\r\n\
                    Content-Type: text/event-stream\r\n\
                    Cache-Control: no-cache, no-transform\r\n\
                    Connection: keep-alive\r\n\
                    X-Accel-Buffering: no\r\n\
                    \r\n";
        if stream.write_all(head.as_bytes()).is_err() {
            continue;
        }
        let _ = stream.flush();

        // Hand the handler an emit closure that writes + flushes per event.
        // Wrap the stream in a Mutex so emit can borrow it mutably across
        // calls without violating Rust's aliasing rules.
        let stream_mu = std::sync::Mutex::new(stream);
        let emit = |event: &str, data: &Value| {
            if let Ok(mut s) = stream_mu.lock() {
                if !event.is_empty() {
                    let _ = write!(s, "event: {}\n", event);
                }
                let _ = write!(s, "data: {}\n\n", data);
                let _ = s.flush();
            }
        };

        handler(req, &emit);
    }
}

// ─── WebSocket ───────────────────────────────────────────────────────────────

/// Bidirectional WebSocket connection bridged through stdin/stdout.
pub struct WsConn {
    lines: std::io::Lines<std::io::BufReader<std::io::Stdin>>,
}

impl WsConn {
    fn new() -> Self {
        use std::io::BufRead;
        WsConn {
            lines: std::io::BufReader::new(std::io::stdin()).lines(),
        }
    }

    /// Read the next message from the client. Returns None on disconnect.
    pub fn read(&mut self) -> Option<serde_json::Value> {
        match self.lines.next() {
            Some(Ok(line)) if !line.trim().is_empty() => {
                serde_json::from_str(&line).ok().or(Some(serde_json::Value::String(line)))
            }
            _ => None,
        }
    }

    /// Send a JSON message to the client.
    pub fn write(&self, data: &serde_json::Value) {
        println!("{}", data);
    }

    /// Send a raw string message to the client.
    pub fn write_raw(&self, msg: &str) {
        println!("{}", msg);
    }
}

/// Entry point for WebSocket functions.
///
/// ```rust,no_run
/// // Driftfile: name get:chat, handler get_chat, stream ws
/// drift_sdk::run_ws(|req, conn| {
///     loop {
///         match conn.read() {
///             Some(msg) => conn.write(&serde_json::json!({"echo": msg})),
///             None => break,
///         }
///     }
/// });
/// ```
pub fn run_ws<F>(handler: F)
where
    F: FnOnce(serde_json::Value, &mut WsConn),
{
    let mut conn = WsConn::new();

    // First line is the initial request.
    let req = conn.read().unwrap_or_default();
    handler(req, &mut conn);
}

// ---------------------------------------------------------------------------
// Edge: slice-to-slice links
// ---------------------------------------------------------------------------
//
// Not Backbone — this is inter-slice networking. A link is an Edge fact,
// declared under edge.links in the Driftfile:
//
//   edge:
//     links: [billing, docs]
//
// The entrypoint is `edge::slice(name)`; the crate-root `slice(name)` is its
// deprecated alias.

fn link_env_name(name: &str) -> String {
    let mangled: String = name
        .to_uppercase()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '_' })
        .collect();
    format!("DRIFT_LINK_{}_URL", mangled)
}

/// Calls to the slices this slice links to under `edge.links` in the Driftfile.
pub mod edge {
    use crate::SliceClient;

    /// A client for another slice of the same owner that this slice links to
    /// under `edge.links` in the Driftfile. The call travels in-cluster and
    /// carries this slice's identity (X-Drift-Slice) as a label when the
    /// runtime supplies one.
    pub fn slice(name: &str) -> SliceClient {
        SliceClient { name: name.to_string() }
    }
}

/// A client for a linked slice. Construct it with [`edge::slice`].
pub struct SliceClient {
    name: String,
}

/// Returns exactly what [`edge::slice`] returns.
///
/// **Deprecated:** use `drift_sdk::edge::slice(name)`. This spelling keeps
/// working, with the same behaviour. It carries no `#[deprecated]` attribute
/// because that would fail the build of any caller compiling with
/// `deny(warnings)`.
pub fn slice(name: &str) -> SliceClient {
    edge::slice(name)
}

impl SliceClient {
    fn url(&self, path: &str) -> Result<String, String> {
        let base = std::env::var(link_env_name(&self.name)).unwrap_or_default();
        if base.is_empty() {
            return Err(format!(
                "drift: slice \"{}\" is not linked: declare it under edge.links in the Driftfile",
                self.name
            ));
        }
        Ok(format!("{}/{}", base.trim_end_matches('/'), path.trim_start_matches('/')))
    }

    /// Issue a request to the linked slice. On (0, msg) the link is missing.
    ///
    /// The X-Drift-Slice identity header is attached last, and a caller's own
    /// X-Drift-Slice is dropped in any letter case, so a caller cannot relabel
    /// the call. It is omitted when the runtime supplies no identity: an empty
    /// header would assert an identity of "" rather than none. See
    /// [`caller_slice`] for why the header is a label and never evidence.
    pub fn request(
        &self,
        method: &str,
        path: &str,
        headers: Option<HashMap<String, String>>,
        body: Option<&str>,
    ) -> (u16, String) {
        let url = match self.url(path) {
            Ok(u) => u,
            Err(e) => return (0, e),
        };
        let own = std::env::var("DRIFT_SLICE").unwrap_or_default();
        let mut h = headers.unwrap_or_default();
        if !own.is_empty() {
            h.retain(|k, _| !k.eq_ignore_ascii_case("x-drift-slice"));
            h.insert("X-Drift-Slice".to_string(), own);
        }
        http_request(method, &url, Some(h), body)
    }

    pub fn get(&self, path: &str) -> (u16, String) {
        self.request("GET", path, None, None)
    }

    pub fn post(&self, path: &str, body: Option<&str>) -> (u16, String) {
        let mut h = HashMap::new();
        h.insert("Content-Type".to_string(), "application/json".to_string());
        self.request("POST", path, Some(h), body)
    }
}

/// The linked slice that called this request, or "" if not via a link. It reads
/// a header the caller writes, so it is a label for logging and routing and
/// authorises nothing.
pub fn caller_slice(req: &Value) -> String {
    if let Some(headers) = req.get("headers").and_then(|h| h.as_object()) {
        for (k, v) in headers {
            if k.eq_ignore_ascii_case("x-drift-slice") {
                return v.as_str().unwrap_or_default().to_string();
            }
        }
    }
    String::new()
}

/// An environment variable value ("" if unset).
pub fn env(key: &str) -> String {
    std::env::var(key).unwrap_or_default()
}

#[cfg(test)]
mod edge_tests {
    // edge::slice is the spelling a link is declared under, and the crate-root
    // slice is its alias: both must read the same DRIFT_LINK_<NAME>_URL and
    // build the same URL. Names are unique to these tests because the
    // environment is process-wide.
    #[test]
    fn edge_slice_and_slice_resolve_the_same_link() {
        std::env::set_var(
            "DRIFT_LINK_EDGE_TEST_PEER_URL",
            "http://canvas.drift-slice-alice-edge-test-peer.svc.cluster.local:8000/",
        );
        let want =
            "http://canvas.drift-slice-alice-edge-test-peer.svc.cluster.local:8000/api/charge"
                .to_string();
        assert_eq!(crate::edge::slice("edge-test-peer").url("/api/charge"), Ok(want.clone()));
        assert_eq!(crate::slice("edge-test-peer").url("/api/charge"), Ok(want));
    }

    // The not-linked answer names the declaration that creates a link, and both
    // spellings say the same thing, before any network I/O.
    #[test]
    fn not_linked_names_edge_links() {
        let want = "drift: slice \"edge-test-unlinked\" is not linked: declare it under edge.links in the Driftfile".to_string();
        assert_eq!(crate::edge::slice("edge-test-unlinked").get("/x"), (0, want.clone()));
        assert_eq!(crate::slice("edge-test-unlinked").get("/x"), (0, want));
    }
}

#[cfg(test)]
mod list_paging_tests {
    use crate::backbone::nosql;

    // A ledger is only correct when it can be read whole. One page is capped and
    // the response carries nothing marking it short, so a collection past the cap
    // has to be paged or it cannot be read correctly at all.
    //
    // One test rather than several: the local store is a process-wide OnceLock, so
    // parallel tests would count each other's documents.
    #[test]
    fn list_all_reads_every_document_past_one_page() {
        std::env::remove_var("BACKBONE_URL");
        let total = crate::LIST_MAX + 200;
        let c = nosql::collection("ops");
        for i in 0..total {
            c.insert(serde_json::json!({
                "_id": format!("{i:04x}"),
                "group_id": "g1",
                "seq": i,
            }));
        }

        // One page stays one page — an unbounded single read is a memory risk
        // inside a function, so paging must not have removed the cap.
        assert_eq!(
            c.list_limited(None, Some(crate::LIST_MAX + 500)).len(),
            crate::LIST_MAX,
            "an over-large limit must still clamp to one page"
        );
        assert_eq!(
            c.list(None).len(),
            crate::LIST_DEFAULT,
            "an omitted limit is the platform default, not everything"
        );

        let all = c.list_all(None).expect("list_all");
        assert_eq!(all.len(), total, "every document must be reachable");

        // Every document exactly once: a cursor that overlapped would double-count
        // a leaf, which corrupts a Merkle root as surely as dropping one.
        let keys: std::collections::HashSet<&str> = all
            .iter()
            .map(|d| d.get("_key").and_then(|k| k.as_str()).expect("_key"))
            .collect();
        assert_eq!(keys.len(), total, "no document may be served twice");

        // The filtered path is the one an indexed read takes, and the one a ledger
        // uses.
        let mut filter = std::collections::HashMap::new();
        filter.insert("group_id".to_string(), "g1".to_string());
        let filtered = c.list_all(Some(filter)).expect("filtered list_all");
        assert_eq!(filtered.len(), total, "a filtered read must page too");
    }
}

#[cfg(test)]
mod local_filter_tests {
    use crate::backbone::nosql;
    use std::collections::HashMap;

    fn ids(rows: &[serde_json::Value]) -> Vec<String> {
        let mut out: Vec<String> = rows
            .iter()
            .map(|d| d.get("_id").and_then(|v| v.as_str()).unwrap_or("").to_string())
            .collect();
        out.sort();
        out
    }

    fn filter(pairs: &[(&str, &str)]) -> Option<HashMap<String, String>> {
        Some(pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect())
    }

    // A filter with several entries matches the documents that satisfy ALL of
    // them, as it does on a slice. Either pair alone matches two of these four
    // and both together match one, so honouring only one pair answers two. A
    // HashMap iterates in a random order, which is why a one-pair answer here
    // also changed from call to call.
    //
    // One test, in its own collection, because the local store is process-wide.
    #[test]
    fn a_filter_ands_every_entry() {
        std::env::remove_var("BACKBONE_URL");
        let c = nosql::collection("filter_and");
        for (id, a, b) in [("x1", "1", "1"), ("x2", "1", "2"), ("x3", "2", "1"), ("x4", "3", "2")] {
            c.insert(serde_json::json!({ "_id": id, "a": a, "b": b }));
        }

        for _ in 0..20 {
            assert_eq!(ids(&c.list(filter(&[("a", "1"), ("b", "2")]))), vec!["x2"]);
        }

        // THE CONTROL: one entry reads as it always has.
        assert_eq!(ids(&c.list(filter(&[("a", "1")]))), vec!["x1", "x2"]);

        // A value the SDK percent-encodes on the way out is compared decoded.
        c.insert(serde_json::json!({ "_id": "x5", "a": "a b/c", "b": "2" }));
        assert_eq!(ids(&c.list(filter(&[("a", "a b/c"), ("b", "2")]))), vec!["x5"]);

        // The slice refuses more than eight pairs, and a refusal reads as no rows
        // here exactly as a 400 does from a slice. The document matches all nine,
        // so an answer of one row means the cap was not applied.
        let mut wide = serde_json::json!({ "_id": "wide" });
        for i in 0..9 {
            wide[format!("f{i}")] = serde_json::json!("v");
        }
        c.insert(wide);
        let pairs = |n: usize| -> Option<HashMap<String, String>> {
            Some((0..n).map(|i| (format!("f{i}"), "v".to_string())).collect())
        };
        assert_eq!(ids(&c.list(pairs(8))), vec!["wide"], "eight pairs is the cap and is admitted");
        assert!(c.list(pairs(9)).is_empty(), "nine pairs must be refused");
    }
}

#[cfg(test)]
mod local_blob_tests {
    use crate::backbone::blob;

    // A blob written in local dev must come back. It did not: put went through
    // call_raw, which returned immediately with BACKBONE_URL unset and never
    // reached a store, and get did the same — so put reported success, get
    // returned None, and the pair was indistinguishable from "that key was never
    // written". The worst failure shape available, because it looks like an
    // answer.
    //
    // One test, like the paging one above and for the same reason: the local
    // store is a process-wide OnceLock and parallel tests would share it.
    #[test]
    fn a_blob_round_trips_in_local_dev() {
        std::env::remove_var("BACKBONE_URL");

        // With an explicit bucket, and with none — "reports/q3.pdf" addresses a
        // bucket, "notes.txt" falls back to `default`, and both have to work or
        // the fallback is a silent hole of its own.
        blob::put("reports/q3.pdf", b"quarterly", Some("application/pdf"));
        blob::put("notes.txt", b"plain", None);

        assert_eq!(
            blob::get("reports/q3.pdf").as_deref(),
            Some(&b"quarterly"[..]),
            "a bucketed blob did not round trip"
        );
        assert_eq!(
            blob::get("notes.txt").as_deref(),
            Some(&b"plain"[..]),
            "an unbucketed blob did not round trip"
        );

        // BYTES, not text: a blob store that survives only UTF-8 is a text store
        // wearing another name, and the first image anyone puts in it is the
        // failure.
        let binary: Vec<u8> = vec![0x00, 0xff, 0x1f, 0x8b, 0x00];
        blob::put("images/pixel.png", &binary, Some("image/png"));
        assert_eq!(
            blob::get("images/pixel.png"),
            Some(binary),
            "binary content came back changed"
        );

        // The control: a key nobody wrote is None, and that is the ONLY case
        // where None is the right answer. Without this, every assertion above
        // passes against a get that returns whatever it was last handed.
        assert_eq!(
            blob::get("reports/never-written.pdf"),
            None,
            "an unwritten key must be absent"
        );

        // Two buckets, one key name. Addressing on the pair rather than the key
        // alone is the whole reason the store is keyed the way it is.
        blob::put("a/same-name", b"from a", None);
        blob::put("b/same-name", b"from b", None);
        assert_eq!(blob::get("a/same-name").as_deref(), Some(&b"from a"[..]));
        assert_eq!(blob::get("b/same-name").as_deref(), Some(&b"from b"[..]));
    }
}
