//! The Rust program the start check cases run (sdk/conformance/probe_check.py).
//!
//! It hands a handler that must not run to the SDK's own entry points,
//! `drift_sdk::run` for the entry "run" and `run_sse` and `run_ws` for the
//! streaming ones. Its two arguments are the entry's name and the marker the
//! handler writes to stderr, before it exits 97, if it is ever reached.

fn reached(marker: &str) -> ! {
    eprintln!("{marker}");
    std::process::exit(97)
}

fn main() {
    let mut args = std::env::args().skip(1);
    let (Some(entry), Some(marker)) = (args.next(), args.next()) else {
        usage()
    };
    match entry.as_str() {
        "run" => drift_sdk::run(move |_req| reached(&marker)),
        "run_sse" => drift_sdk::run_sse(move |_req, _emit| reached(&marker)),
        "run_ws" => drift_sdk::run_ws(move |_req, _conn| reached(&marker)),
        _ => usage(),
    }
}

fn usage() -> ! {
    eprintln!("usage: conformance-rust-probe <run|run_sse|run_ws> <marker>");
    std::process::exit(2)
}
