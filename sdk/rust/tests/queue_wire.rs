//! The queue methods against a stand-in for a slice's queue routes.
//!
//! The stand-in answers as the slice's backbone router does
//! (src/slice/src/backbone/mod.rs in the platform): a pop names its queue in the
//! query string and nowhere else, and one that names it in a body is answered
//! 400 "queue query param required". Every SDK sent the name in a body, so a pop
//! never once worked on a slice. The conformance suite runs the same cases
//! against a real slice.
//!
//! An integration test, so it is its own process: BACKBONE_URL is process-wide,
//! and the library's unit tests read the local store.

use drift_sdk::backbone;
use serde_json::{json, Value};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};

/// Start the stand-in and return its base URL.
fn start_slice() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let queues: Arc<Mutex<HashMap<String, (Vec<Value>, u64)>>> = Arc::default();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { continue };
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut line = String::new();
            if reader.read_line(&mut line).is_err() {
                continue;
            }
            let mut parts = line.split_whitespace();
            let method = parts.next().unwrap_or("").to_string();
            let target = parts.next().unwrap_or("").to_string();
            let mut length = 0usize;
            loop {
                let mut h = String::new();
                if reader.read_line(&mut h).is_err() || h.trim().is_empty() {
                    break;
                }
                if let Some((k, v)) = h.split_once(':') {
                    if k.trim().eq_ignore_ascii_case("content-length") {
                        length = v.trim().parse().unwrap_or(0);
                    }
                }
            }
            let mut body = vec![0u8; length];
            let _ = reader.read_exact(&mut body);

            let (path, query) = target.split_once('?').unwrap_or((target.as_str(), ""));
            let name = query
                .split('&')
                .find_map(|p| p.strip_prefix("queue="))
                .unwrap_or("")
                .to_string();
            let mut qs = queues.lock().unwrap();
            let (status, reply): (u16, Option<Value>) = match (method.as_str(), path) {
                ("POST", "/queue/push") => {
                    let req: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
                    let q = req.get("queue").and_then(|v| v.as_str()).unwrap_or("");
                    let b = req.get("body").cloned().unwrap_or(Value::Null);
                    if q.is_empty() || !b.is_object() {
                        (400, Some(json!({"error": "queue and body required"})))
                    } else {
                        let entry = qs.entry(q.to_string()).or_default();
                        let id = entry.1.to_string();
                        entry.1 += 1;
                        entry.0.push(json!({"id": id, "body": b, "timestamp": "2026-01-01T00:00:00.000000000Z"}));
                        (200, Some(json!({"id": id})))
                    }
                }
                ("POST", "/queue/pop") | ("GET", "/queue/peek") | ("GET", "/queue/len") if name.is_empty() => {
                    (400, Some(json!({"error": "queue query param required"})))
                }
                ("POST", "/queue/pop") | ("GET", "/queue/peek") | ("GET", "/queue/len") => match qs.get_mut(&name) {
                    None => (404, Some(json!({"error": format!("queue {name} not found")}))),
                    Some((msgs, _)) if path == "/queue/len" => (200, Some(json!({"length": msgs.len()}))),
                    Some((msgs, _)) if msgs.is_empty() => (204, None),
                    Some((msgs, _)) if path == "/queue/pop" => (200, Some(msgs.remove(0))),
                    Some((msgs, _)) => (200, Some(msgs[0].clone())),
                },
                _ => (404, Some(json!({"error": "not found"}))),
            };
            drop(qs);
            let text = reply.map(|v| v.to_string()).unwrap_or_default();
            let _ = write!(
                stream,
                "HTTP/1.1 {status} X\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{text}",
                text.len()
            );
        }
    });
    format!("http://127.0.0.1:{port}")
}

/// One test, so nothing else in this process reads BACKBONE_URL meanwhile.
#[test]
fn queue_methods_reach_a_slice() {
    std::env::set_var("BACKBONE_URL", start_slice());
    let q = backbone::queue("jobs");
    for (n, want) in [(1, "0"), (2, "1")] {
        assert_eq!(q.push(json!({"n": n})).as_deref(), Some(want), "push returns the slice's id");
    }

    // Pop returns the body each message was pushed with, first in first out.
    // Naming the queue in a body was answered 400, and `pop` returned the
    // slice's error object as though it were a message.
    assert_eq!(q.pop(), Some(json!({"n": 1})), "pop must reach the slice and return the pushed body");
    assert_eq!(q.try_pop(), Ok(Some(json!({"n": 2}))));
    assert_eq!(q.try_pop(), Ok(None), "an empty queue answers 204, which is no message and no error");

    // A queue nothing was pushed to does not exist, and saying so is an error
    // rather than an empty queue.
    let err = backbone::queue("never-pushed").try_pop().unwrap_err();
    assert!(
        err.contains("HTTP 404") && err.contains("never-pushed not found"),
        "a never-pushed queue must be the slice's 404, in the slice's words, got {err}"
    );

    assert_eq!(q.push(json!({"n": 3})).as_deref(), Some("2"));
    assert_eq!(q.peek(), Ok(Some(json!({"n": 3}))));
    assert_eq!(q.len(), Ok(1), "peek must leave the message in place");
    assert!(q.try_push(json!("a bare string")).is_err(), "a body that is not an object is refused");
}
