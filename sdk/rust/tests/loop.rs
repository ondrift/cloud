//! `drift_sdk::run` under DRIFT_RUNTIME: one process serving many calls.
//!
//! The program is `examples/loop_serve.rs`, started as a slice starts a warm
//! worker: DRIFT_RUNTIME set, nothing else in its environment, envelopes
//! written one per line to its stdin, one reply line read back for each. The
//! same conversation, for every SDK, is held by sdk/conformance; these tests
//! are the crate's own, so `cargo test` fails here before the conformance job
//! gets to it.

use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc::{self, Receiver};
use std::sync::{Arc, Mutex, Once};
use std::thread;
use std::time::{Duration, Instant};

/// How long a worker has to answer an envelope or to end after its input does.
const WAIT: Duration = Duration::from_secs(5);

/// The built example, next to the test binary's own directory under target/.
/// A plain `cargo test` has built it; `cargo test --test loop` has not, and
/// would run a stale one, so it is built here once: a no-op when current.
fn program() -> PathBuf {
    static BUILT: Once = Once::new();
    let mut dir = std::env::current_exe().unwrap();
    dir.pop(); // the test binary
    dir.pop(); // deps/
    let exe = dir.join("examples").join(format!("loop_serve{}", std::env::consts::EXE_SUFFIX));
    BUILT.call_once(|| {
        let cargo = std::env::var_os("CARGO").unwrap_or_else(|| "cargo".into());
        let status = Command::new(cargo)
            .args(["build", "--quiet", "--example", "loop_serve"])
            .status()
            .expect("cargo runs");
        assert!(status.success(), "building the loop_serve example failed");
    });
    exe
}

/// A worker: its pipes, what it has written to stdout (line by line) and to
/// stderr (all of it).
struct Worker {
    child: Child,
    stdin: Option<ChildStdin>,
    lines: Receiver<String>,
    stderr: Arc<Mutex<String>>,
}

impl Worker {
    fn start() -> Worker {
        let mut child = Command::new(program())
            .env_clear()
            .env("DRIFT_RUNTIME", "1")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("the loop_serve example is built by cargo test");
        let stdin = child.stdin.take();

        let (tx, lines) = mpsc::channel();
        let out = child.stdout.take().unwrap();
        thread::spawn(move || {
            for line in BufReader::new(out).lines().map_while(Result::ok) {
                if tx.send(line).is_err() {
                    return;
                }
            }
        });

        let stderr = Arc::new(Mutex::new(String::new()));
        let err = child.stderr.take().unwrap();
        let sink = Arc::clone(&stderr);
        thread::spawn(move || {
            for line in BufReader::new(err).lines().map_while(Result::ok) {
                let mut text = sink.lock().unwrap();
                text.push_str(&line);
                text.push('\n');
            }
        });

        Worker { child, stdin, lines, stderr }
    }

    /// Write one envelope and its newline.
    fn send(&mut self, envelope: &Value) {
        let stdin = self.stdin.as_mut().expect("stdin is open");
        writeln!(stdin, "{envelope}").unwrap();
        stdin.flush().unwrap();
    }

    /// The next line the worker wrote to stdout; panics when none comes.
    fn reply_line(&self) -> String {
        self.lines
            .recv_timeout(WAIT)
            .unwrap_or_else(|_| panic!("no reply line within {WAIT:?}; stderr: {}", self.stderr()))
    }

    /// The next reply, parsed. A line that is not JSON is a failure: stdout
    /// carries replies and nothing else.
    fn reply(&self) -> Value {
        let line = self.reply_line();
        serde_json::from_str(&line)
            .unwrap_or_else(|_| panic!("a stdout line that is not a reply: {line:?}"))
    }

    /// Send one envelope and read its reply.
    fn call(&mut self, envelope: Value) -> Value {
        self.send(&envelope);
        self.reply()
    }

    /// Close stdin, as the slice does to retire a worker.
    fn close_stdin(&mut self) {
        self.stdin = None;
    }

    /// The exit code once the process has ended; panics if it outlives WAIT.
    fn exit_code(&mut self) -> Option<i32> {
        let start = Instant::now();
        loop {
            if let Some(status) = self.child.try_wait().unwrap() {
                return status.code();
            }
            assert!(start.elapsed() < WAIT, "the worker did not end within {WAIT:?} of its input ending");
            thread::sleep(Duration::from_millis(10));
        }
    }

    fn stderr(&self) -> String {
        self.stderr.lock().unwrap().clone()
    }
}

impl Drop for Worker {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn payload(reply: &Value) -> &Value {
    &reply["payload"]
}

#[test]
fn three_envelopes_are_answered_by_one_process() {
    let mut worker = Worker::start();
    let pids: Vec<u64> = (0..3)
        .map(|i| {
            let reply = worker.call(json!({"path": "/", "headers": {}, "body": {"n": i}}));
            assert_eq!(reply["status"], 200, "call {i}: {reply}");
            payload(&reply)["pid"].as_u64().expect("the handler reports its pid")
        })
        .collect();

    assert_eq!(pids[0], pids[1], "calls 1 and 2 were served by different processes");
    assert_eq!(pids[1], pids[2], "calls 2 and 3 were served by different processes");
    assert_eq!(pids[0], worker.child.id() as u64, "the pid is not the process the test started");
}

#[test]
fn a_printed_line_reaches_stderr_and_the_reply_stays_one_line() {
    let mut worker = Worker::start();
    let first = worker.call(json!({"path": "/", "body": {"print": "loop-test-printed-line"}}));
    assert_eq!(first["status"], 200, "the call that printed: {first}");
    // The reply after the print is still the next line on stdout, intact.
    let second = worker.call(json!({"path": "/", "body": {}}));
    assert_eq!(second["status"], 200, "the call after the print: {second}");

    worker.close_stdin();
    assert_eq!(worker.exit_code(), Some(0));
    assert!(
        worker.stderr().contains("loop-test-printed-line"),
        "the printed text is not on stderr: {:?}",
        worker.stderr()
    );
}

#[test]
fn end_of_input_ends_the_process_with_status_zero() {
    let mut worker = Worker::start();
    for i in 0..3 {
        worker.call(json!({"path": "/", "body": {"n": i}}));
    }
    worker.close_stdin();
    assert_eq!(worker.exit_code(), Some(0));
}

#[test]
fn the_scratch_directory_is_the_envelopes_on_every_call() {
    let mut worker = Worker::start();
    for dir in ["/scratch/loop-1", "/scratch/loop-2", "/scratch/loop-3"] {
        let reply = worker.call(json!({"path": "/", "tmpdir": dir}));
        let seen = payload(&reply);
        assert_eq!(seen["tmpdir"], dir, "TMPDIR: {reply}");
        assert_eq!(seen["home"], dir, "HOME: {reply}");
        assert_eq!(seen["temp_dir"], dir, "std::env::temp_dir(): {reply}");
    }
}

#[test]
fn a_secret_the_envelope_no_longer_carries_is_gone() {
    let mut worker = Worker::start();
    let secrets = |reply: &Value| payload(reply)["secrets"].clone();

    let one = worker.call(json!({"secrets": {"API_KEY": "key-one", "WEBHOOK_TOKEN": "token"}}));
    assert_eq!(secrets(&one), json!({"DRIFT_SECRET_API_KEY": "key-one", "DRIFT_SECRET_WEBHOOK_TOKEN": "token"}));

    let two = worker.call(json!({"secrets": {"API_KEY": "key-two"}}));
    assert_eq!(secrets(&two), json!({"DRIFT_SECRET_API_KEY": "key-two"}));

    let three = worker.call(json!({}));
    assert_eq!(secrets(&three), json!({}));
}

#[test]
fn a_previous_secret_follows_the_same_rule() {
    let mut worker = Worker::start();
    let secrets = |reply: &Value| payload(reply)["secrets"].clone();

    let one = worker.call(json!({"secrets": {"API_KEY": "new"}, "secrets_previous": {"API_KEY": "old"}}));
    assert_eq!(secrets(&one), json!({"DRIFT_SECRET_API_KEY": "new", "DRIFT_SECRET_API_KEY_PREVIOUS": "old"}));

    let two = worker.call(json!({"secrets": {"API_KEY": "new"}}));
    assert_eq!(secrets(&two), json!({"DRIFT_SECRET_API_KEY": "new"}));
}

#[test]
#[cfg(target_os = "linux")]
fn every_reply_carries_the_peak_resident_size() {
    let mut worker = Worker::start();
    for i in 0..3 {
        let reply = worker.call(json!({"path": "/", "body": {"n": i}}));
        let rss = reply["rss_bytes"].as_u64();
        assert!(rss.is_some_and(|n| n > 0), "call {i}: no positive rss_bytes in {reply}");
    }
}

/// A trigger's spawn: one envelope, no trailing newline, then end of input. It
/// is answered once and the process exits 0. This holds before and after the
/// loop, which is the point of it.
#[test]
fn one_envelope_without_a_newline_is_answered_once() {
    let mut worker = Worker::start();
    {
        let stdin = worker.stdin.as_mut().unwrap();
        stdin.write_all(br#"{"path":"/","headers":{},"body":{}}"#).unwrap();
        stdin.flush().unwrap();
    }
    worker.close_stdin();

    let reply = worker.reply();
    assert_eq!(reply["status"], 200, "{reply}");
    assert_eq!(worker.exit_code(), Some(0));
    assert!(
        worker.lines.recv_timeout(Duration::from_millis(200)).is_err(),
        "a second line came after the one reply"
    );
}
