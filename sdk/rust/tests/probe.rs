//! A process started to be checked says hello and exits, and never reaches the
//! handler.
//!
//! Before a deploy replaces the function that serves, the slice starts the new
//! version once: DRIFT_INVOKE=probe1, stdin closed. It wants one line on stdout,
//! `{"hello":{"proto":"drift-probe","v":1}}`, and exit status 0, with the handler
//! never called, because a handler writes to databases and sends mail. The
//! platform owns that contract; these tests hold the SDK's side of it.
//!
//! Answering means leaving the process, so each entry point is started as a
//! process of its own: `examples/probe_serve.rs`, whose handler fails the test if
//! it is called (it writes HANDLER_CALLED to stderr and exits 97). Each entry
//! also runs without the check, with a request on stdin, where the handler IS
//! reached; without that the absence of the marker could mean the guard cannot
//! fire.

use serde_json::Value;
use std::io::{Read, Write};
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::sync::OnceLock;
use std::thread;
use std::time::{Duration, Instant};

/// The line the platform's start check reads, as the SDK repeats it.
const HELLO: &str = "{\"hello\":{\"proto\":\"drift-probe\",\"v\":1}}\n";
const CALLED: &str = "HANDLER_CALLED";

/// The slice allows a check ten seconds from spawn.
const WITHIN: Duration = Duration::from_secs(10);

/// What an entry reads when it is not being checked: a request, on one line.
const REQUEST: &str = "{\"path\":\"/\",\"headers\":{},\"query\":\"\",\"body\":null}\n";

/// The built example. It is built here once (a no-op when current), because
/// `cargo test --test probe` does not build examples: cargo itself says where the
/// executable is (`--message-format=json`, the `executable` of the `probe_serve`
/// artifact), whatever target directory, profile or target triple is in use.
fn program() -> PathBuf {
    static BUILT: OnceLock<PathBuf> = OnceLock::new();
    BUILT
        .get_or_init(|| {
            let cargo = std::env::var_os("CARGO").unwrap_or_else(|| "cargo".into());
            let out = Command::new(cargo)
                .args(["build", "--quiet", "--example", "probe_serve", "--message-format=json"])
                .stderr(Stdio::inherit())
                .output()
                .expect("cargo runs");
            assert!(out.status.success(), "building the probe_serve example failed");
            String::from_utf8_lossy(&out.stdout)
                .lines()
                .filter_map(|l| serde_json::from_str::<Value>(l).ok())
                .filter(|m| m["reason"] == "compiler-artifact" && m["target"]["name"] == "probe_serve")
                .filter_map(|m| m["executable"].as_str().map(PathBuf::from))
                .last()
                .expect("cargo names the probe_serve executable")
        })
        .clone()
}

/// What a started entry wrote, and how it ended.
struct Finished {
    stdout: String,
    stderr: String,
    /// The exit status, None when a signal ended the process.
    status: Option<i32>,
}

impl Finished {
    fn describe(&self) -> String {
        format!("stdout {:?}, status {:?}, stderr {:?}", self.stdout, self.status, self.stderr.trim())
    }
}

/// The entry point as its own process, as the slice starts a function:
/// DRIFT_RUNTIME set and nothing else but `invoke`. A request of None is a closed
/// stdin (the null device, as the slice gives a check); otherwise it is written
/// to stdin, which is then closed. Panics when the process is still running
/// after WITHIN.
fn start(entry: &str, invoke: Option<&str>, request: Option<&str>) -> Finished {
    let mut command = Command::new(program());
    command
        .arg(entry)
        .env_clear()
        .env("DRIFT_RUNTIME", "1")
        .stdin(if request.is_some() { Stdio::piped() } else { Stdio::null() })
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if let Some(invoke) = invoke {
        command.env("DRIFT_INVOKE", invoke);
    }
    let mut child = command.spawn().expect("the probe_serve example is built by cargo test");
    if let Some(request) = request {
        let mut stdin = child.stdin.take().expect("stdin is piped");
        // A process that has already ended is not an error to write to here.
        let _ = stdin.write_all(request.as_bytes());
    }

    let mut out = child.stdout.take().expect("stdout is piped");
    let mut err = child.stderr.take().expect("stderr is piped");
    let stdout = thread::spawn(move || {
        let mut text = String::new();
        let _ = out.read_to_string(&mut text);
        text
    });
    let stderr = thread::spawn(move || {
        let mut text = String::new();
        let _ = err.read_to_string(&mut text);
        text
    });

    let started = Instant::now();
    let status = loop {
        if let Some(status) = child.try_wait().expect("the process can be waited for") {
            break status.code();
        }
        if started.elapsed() > WITHIN {
            let _ = child.kill();
            let _ = child.wait();
            panic!("{entry} was still running after {WITHIN:?}");
        }
        thread::sleep(Duration::from_millis(5));
    };
    Finished { stdout: stdout.join().unwrap(), stderr: stderr.join().unwrap(), status }
}

fn answers_the_start_check(entry: &str) {
    let done = start(entry, Some("probe1"), None);
    assert_eq!(done.stdout, HELLO, "{entry} must write the hello line and nothing else: {}", done.describe());
    assert_eq!(done.status, Some(0), "{entry} must exit 0: {}", done.describe());
    assert!(!done.stderr.contains(CALLED), "{entry} reached the handler: {}", done.describe());
}

fn still_reaches_the_handler(entry: &str, invoke: Option<&str>) {
    let done = start(entry, invoke, Some(REQUEST));
    assert!(
        done.stderr.contains(CALLED) && done.status == Some(97) && !done.stdout.contains("drift-probe"),
        "{entry} with DRIFT_INVOKE {invoke:?} must reach the handler and say no hello: {}",
        done.describe()
    );
}

#[test]
fn the_version_of_the_start_check_is_one() {
    assert_eq!(drift_sdk::PROBE_PROTOCOL, 1);
}

#[test]
fn run_answers_the_start_check() {
    answers_the_start_check("run");
}

#[test]
fn run_sse_answers_the_start_check() {
    answers_the_start_check("run_sse");
}

#[test]
fn run_ws_answers_the_start_check() {
    answers_the_start_check("run_ws");
}

/// THE CONTROLS. Without the check an entry reads its request and calls the
/// handler, so the marker above can appear, and an SDK that answered every start
/// as a check would show here.
#[test]
fn without_the_check_every_entry_still_reaches_the_handler() {
    for entry in ["run", "run_sse", "run_ws"] {
        still_reaches_the_handler(entry, None);
    }
}

#[test]
fn another_version_of_the_check_is_not_answered() {
    for entry in ["run", "run_sse", "run_ws"] {
        still_reaches_the_handler(entry, Some("probe2"));
    }
}
