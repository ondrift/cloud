//! The Rust program the loop cases run (sdk/conformance/loop_check.py).
//!
//! It serves one handler through `drift_sdk::run`, the entry point the generated
//! Rust wrapper calls, so what the cases assert is what a slice's warm worker
//! gets. It re-implements no loop.
//!
//! The handler reports what a function can see of its call: its pid, TMPDIR and
//! HOME, and every DRIFT_SECRET_ variable. Two requests change what it does: a
//! body with "print" writes that text to stdout, as a function's own println
//! does, and a body with "fail" panics.

use serde_json::{json, Map, Value};

fn main() {
    drift_sdk::run(|req: Value| -> Value {
        let ask = req.get("body").cloned().unwrap_or(Value::Null);
        if let Some(text) = ask.get("print").and_then(Value::as_str) {
            println!("{text}");
        }
        if ask.get("fail").and_then(Value::as_bool) == Some(true) {
            panic!("loop conformance: the handler was asked to fail");
        }

        let secrets: Map<String, Value> = std::env::vars()
            .filter(|(name, _)| name.starts_with("DRIFT_SECRET_"))
            .map(|(name, value)| (name, Value::String(value)))
            .collect();
        json!({
            "status": 200,
            "message": "ok",
            "payload": {
                "pid": std::process::id(),
                "tmpdir": std::env::var("TMPDIR").ok(),
                "home": std::env::var("HOME").ok(),
                "secrets": secrets,
            },
        })
    });
}
