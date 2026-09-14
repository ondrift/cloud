//! Drives the Rust SDK through the shared conformance cases and prints what it
//! got, as JSON, for check.py to compare against expected.json.
//!
//! It asserts NOTHING itself. The assertions are in expected.json, once, for all
//! six languages — six independently-worded suites can each pass while asserting
//! subtly different things, and that is how five SDKs came to disagree with Go
//! about local blobs without anybody noticing.

use drift_sdk::backbone;
use serde_json::{json, Map, Value};

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

    emit(&out);
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
