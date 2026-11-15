//! The program tests/loop.rs starts, as a slice starts a warm worker.
//!
//! It serves one handler through `drift_sdk::run`, the entry point the
//! generated wrapper calls. The handler reports what a function can see of its
//! call: its pid, `std::env::temp_dir()`, TMPDIR and HOME, and every
//! DRIFT_SECRET_ variable. A body with "print" makes it `println!` that text,
//! as a function's own logging does, and a body with "fail" makes it panic.

use serde_json::{json, Map, Value};

fn main() {
    drift_sdk::run(|req: Value| -> Value {
        let ask = req.get("body").cloned().unwrap_or(Value::Null);
        if let Some(text) = ask.get("print").and_then(Value::as_str) {
            println!("{text}");
        }
        if ask.get("fail").and_then(Value::as_bool) == Some(true) {
            panic!("the handler was asked to fail");
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
                "temp_dir": std::env::temp_dir().to_string_lossy(),
                "tmpdir": std::env::var("TMPDIR").ok(),
                "home": std::env::var("HOME").ok(),
                "secrets": secrets,
            },
        })
    });
}
