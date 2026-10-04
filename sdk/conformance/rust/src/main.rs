//! Drives the Rust SDK through the shared conformance cases and prints what it
//! got, as JSON, for check.py to compare against expected.json.
//!
//! It asserts NOTHING itself. The assertions are in expected.json, once, for all
//! six languages — six independently-worded suites can each pass while asserting
//! subtly different things, and that is how five SDKs came to disagree with Go
//! about local blobs without anybody noticing.

use drift_sdk::{backbone, edge};
use serde_json::{json, Map, Value};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;

fn main() {
    // No BACKBONE_URL: every case runs against the in-memory local store, which
    // is the loop a developer meets first and the one where the six drifted.
    std::env::set_var("BACKBONE_URL", "");

    let mut out = Map::new();

    // ── Blob ────────────────────────────────────────────────────────────────
    backbone::blob::put("uploads/greeting.txt", b"hello, slice", Some("text/plain"));
    match backbone::blob::get("uploads/greeting.txt") {
        Some(b) => {
            out.insert("blob_with_bucket".into(), json!(String::from_utf8_lossy(&b)));
        }
        None => return fail(&mut out, "Blob::get with a bucket returned None"),
    }

    backbone::blob::put("greeting.txt", b"bare", None);
    match backbone::blob::get("greeting.txt") {
        Some(b) => {
            out.insert("blob_without_bucket".into(), json!(String::from_utf8_lossy(&b)));
        }
        None => return fail(&mut out, "Blob::get without a bucket returned None"),
    }

    // A key never written must be DISTINGUISHABLE from one holding empty bytes,
    // and the shared case spells that answer "error".
    //
    // Rust reaches it through the type rather than through a raised error, and
    // that is not a weaker answer: `get` returns Option<Vec<u8>>, so absent is
    // `None` and a stored empty value is `Some(vec![])`. They cannot be confused
    // — the compiler will not let a caller treat one as the other — which is
    // exactly the property the other five had to raise an exception to get.
    //
    // This does NOT let a broken implementation through. An SDK returning `None`
    // for everything fails the two cases above, which is the accident this case
    // exists to catch. What it maps is the shape of the honest answer, not the
    // answer itself.
    match backbone::blob::get("uploads/never-written.txt") {
        None => {
            out.insert("blob_absent".into(), json!("error"));
        }
        Some(b) if b.is_empty() => {
            out.insert("blob_absent".into(), Value::Null);
        }
        Some(b) => {
            out.insert("blob_absent".into(), json!(String::from_utf8_lossy(&b)));
        }
    }

    // ── Ordered NoSQL reads ─────────────────────────────────────────────────
    let small = backbone::nosql::collection("conformance_small");
    for i in 1..=12 {
        small.insert(json!({"n": i}));
    }

    out.insert("list_in_order".into(), json!(field_n(&small.list_in_order(Some(100)))));
    out.insert("list_default_order".into(), json!(field_key(&small.list(None))));

    // Past the 1000-row page cap, which is where a cursor that advances in KEY
    // order rather than in the ordered sequence silently truncates.
    let big = backbone::nosql::collection("conformance_big");
    for i in 1..=1200 {
        big.insert(json!({"n": i}));
    }
    match big.list_all_in_order() {
        Ok(rows) => {
            let ns = field_n(&rows);
            out.insert("list_all_in_order_count".into(), json!(ns.len()));
            if !ns.is_empty() {
                out.insert("list_all_in_order_first".into(), json!(ns[0]));
                out.insert("list_all_in_order_last".into(), json!(ns[ns.len() - 1]));
            }
            if ns.len() >= 1002 {
                out.insert("list_all_in_order_boundary".into(), json!(&ns[998..1002]));
            }
        }
        Err(e) => return fail(&mut out, &format!("list_all_in_order: {e}")),
    }

    // ── Filtered NoSQL reads ────────────────────────────────────────────────
    let filtered = backbone::nosql::collection("conformance_filter");
    let mut wide = json!({"_id": "wide"});
    for i in 0..9 {
        wide[format!("f{i}")] = json!("v");
    }
    for doc in [
        json!({"_id": "x1", "a": "1", "b": "1"}),
        json!({"_id": "x2", "a": "1", "b": "2"}),
        json!({"_id": "x3", "a": "2", "b": "1"}),
        json!({"_id": "x4", "a": "3", "b": "2"}),
        wide,
    ] {
        filtered.insert(doc);
    }
    let filter = |pairs: Vec<(String, String)>| Some(pairs.into_iter().collect());
    let two = filter(vec![("a".into(), "1".into()), ("b".into(), "2".into())]);
    out.insert("list_filter_two_fields".into(), json!(field_ids(&filtered.list(two))));
    let one = filter(vec![("a".into(), "1".into())]);
    out.insert("list_filter_one_field".into(), json!(field_ids(&filtered.list(one))));

    // `list` has no error channel: a refusal reads as no rows, as a slice's 400
    // does. The document matches all nine entries, so no rows IS the refusal
    // here, and it is reported as the shared case spells one.
    let nine = filter((0..9).map(|i| (format!("f{i}"), "v".to_string())).collect());
    let rows = filtered.list(nine);
    if rows.is_empty() {
        out.insert("list_filter_over_cap".into(), json!("error"));
    } else {
        out.insert("list_filter_over_cap".into(), json!(field_ids(&rows)));
    }

    // ── Edge links ──────────────────────────────────────────────────────────
    let port = match start_peer() {
        Ok(p) => p,
        Err(e) => return fail(&mut out, &format!("the loopback peer did not start: {e}")),
    };
    std::env::remove_var("DRIFT_SLICE");
    std::env::set_var("DRIFT_LINK_BILLING_API_URL", format!("http://127.0.0.1:{port}/base/"));

    let e = echoed(edge::slice("billing-api").get("/api/events"));
    out.insert("edge_slice_url".into(), e.get("path").cloned().unwrap_or(Value::Null));
    out.insert("slice_identity_absent".into(), e.get("slice").cloned().unwrap_or(Value::Null));
    let e = echoed(drift_sdk::slice("billing-api").get("/api/events"));
    out.insert("slice_url".into(), e.get("path").cloned().unwrap_or(Value::Null));

    std::env::set_var("DRIFT_SLICE", "billing");
    let mut caller = HashMap::new();
    caller.insert("X-Drift-Slice".to_string(), "operator".to_string());
    let e = echoed(edge::slice("billing-api").request("GET", "/api/events", Some(caller), None));
    out.insert("slice_identity_kept".into(), e.get("slice").cloned().unwrap_or(Value::Null));
    std::env::remove_var("DRIFT_SLICE");

    // `request` has no error channel: a missing link answers (0, message)
    // before any network I/O, and the message is what the shared case compares.
    let (_, msg) = edge::slice("billing").get("/x");
    out.insert("edge_slice_not_linked".into(), json!(msg));
    let (_, msg) = drift_sdk::slice("billing").get("/x");
    out.insert("slice_not_linked".into(), json!(msg));

    emit(&out);
}

/// Start a loopback peer that echoes the path it was asked for and the identity
/// header it received, null when the header is absent. Returns its port.
fn start_peer() -> std::io::Result<u16> {
    let listener = TcpListener::bind("127.0.0.1:0")?;
    let port = listener.local_addr()?.port();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { continue };
            let Ok(clone) = stream.try_clone() else { continue };
            let mut reader = BufReader::new(clone);
            let mut request_line = String::new();
            if reader.read_line(&mut request_line).is_err() {
                continue;
            }
            let target = request_line.split(' ').nth(1).unwrap_or("");
            let path = target.split('?').next().unwrap_or("").to_string();
            let mut slices: Vec<String> = Vec::new();
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() || line.trim().is_empty() {
                    break;
                }
                if let Some((k, v)) = line.split_once(':') {
                    if k.trim().eq_ignore_ascii_case("x-drift-slice") {
                        slices.push(v.trim().to_string());
                    }
                }
            }
            let slice = if slices.is_empty() { Value::Null } else { json!(slices.join(",")) };
            let body = json!({"path": path, "slice": slice}).to_string();
            let _ = write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
        }
    });
    Ok(port)
}

/// What the loopback peer echoed back, or an empty object if the call failed.
fn echoed((_, body): (u16, String)) -> Value {
    serde_json::from_str(&body).unwrap_or_else(|_| json!({}))
}

/// The `_id` of each row, sorted, so a case compares the set.
fn field_ids(rows: &[Value]) -> Vec<String> {
    let mut ids: Vec<String> = rows
        .iter()
        .filter_map(|d| d.get("_id").and_then(|v| v.as_str()).map(|s| s.to_string()))
        .collect();
    ids.sort();
    ids
}

/// The `n` field of each row, in the order the rows came back.
fn field_n(rows: &[Value]) -> Vec<i64> {
    rows.iter().filter_map(|d| d.get("n").and_then(|v| v.as_i64())).collect()
}

/// The storage key, which is what the default order is over.
fn field_key(rows: &[Value]) -> Vec<String> {
    rows.iter()
        .filter_map(|d| d.get("_key").and_then(|v| v.as_str()).map(|s| s.to_string()))
        .collect()
}

/// Report the first thing that went wrong and stop. A driver that carried on
/// would report later cases against a store in a state nobody intended.
fn fail(out: &mut Map<String, Value>, message: &str) {
    out.insert("error".into(), json!(message));
    emit(out);
}

fn emit(out: &Map<String, Value>) {
    println!("{}", serde_json::to_string_pretty(out).unwrap_or_default());
}
