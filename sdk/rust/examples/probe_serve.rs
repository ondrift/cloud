//! The program tests/probe.rs starts, as the slice starts a function to check it.
//!
//! It hands a handler to the entry point its first argument names (`run`,
//! `run_sse` or `run_ws`), the entry points the generated wrapper calls. The
//! handler must never run under a start check: if it does, it writes
//! HANDLER_CALLED to stderr and exits 97.

fn called() -> ! {
    eprintln!("HANDLER_CALLED");
    std::process::exit(97)
}

fn main() {
    match std::env::args().nth(1).as_deref() {
        Some("run") => drift_sdk::run(|_req| called()),
        Some("run_sse") => drift_sdk::run_sse(|_req, _emit| called()),
        Some("run_ws") => drift_sdk::run_ws(|_req, _conn| called()),
        other => {
            eprintln!("probe_serve: no entry point named {other:?}");
            std::process::exit(2)
        }
    }
}
