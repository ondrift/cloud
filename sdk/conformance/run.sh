#!/usr/bin/env bash
# =============================================================================
# run.sh — the same conformance cases against every SDK.
#
#   bash sdk/conformance/run.sh            every language whose toolchain is here
#   bash sdk/conformance/run.sh node ruby  just those
#   bash sdk/conformance/run.sh --slice http://127.0.0.1:8000
#                                          the queue_ cases, against a slice
#
# ── --slice: THE QUEUE CASES AGAINST A REAL SLICE ───────────────────────────
#
# The slice is the authority on the queue wire, and a local store agreeing with
# itself proves nothing about it. --slice hands each driver a slice's Backbone
# URL as DRIFT_CONFORMANCE_BACKBONE_URL; the driver then runs the queue_ cases
# against it and nothing else, and they are checked against the same
# expectations the local store meets. A standalone slice is enough:
#
#   BACKBONE_DATA_DIR=$(mktemp -d) <the slice binary> &
#   bash sdk/conformance/run.sh --slice http://127.0.0.1:8000
#
# Drift ships six SDKs so a customer can build in the language their team
# already uses, and that promise is worth exactly as much as the weakest one.
# The roadmap's Phase 2 milestone asks for "the same Put/Get/list-in-order
# integration test" to pass unmodified against all six — so the assertions live
# in expected.json, once, and each language ships a thin driver that performs
# the operations and prints what it got.
#
# ── A MISSING TOOLCHAIN IS A SKIP, AND A SKIP IS NOT A PASS ─────────────────
#
# A language whose runtime is not installed is reported as SKIPPED and counted
# separately, and the final line says how many ran. `ok` from a suite that ran
# nothing is indistinguishable from one that passed — which is the same defect
# this harness exists to find in the SDKs themselves, and it would be absurd to
# ship it in the thing doing the finding.
#
# CI installs all six, so a skip there is a workflow bug rather than a local
# convenience. --strict turns any skip into a failure, and CI passes it.
#
# ── THE LOOP CASES, FOR EVERY SDK THAT SERVES MANY CALLS ────────────────────
#
# After a language's driver, loop_check.py runs a small program that serves one
# handler through the SDK's own run entry, as a slice's warm worker talks to it.
# expected.json, under "$loop", owns the cases and which languages are not
# applicable, pending (a failing case is reported, a PASSING language fails the
# run until it is taken off the list) or held to every case. Not run with
# --slice, which is about the queue cases and nothing else.
#
# ── THE PROBE CASES, FOR EVERY SDK ───────────────────────────────────────────
#
# Before a deploy replaces the function that serves, the slice starts the new
# version once with DRIFT_INVOKE=probe1 and stdin closed, and wants one hello
# line, exit status 0 and the handler never called. After the loop cases,
# probe_check.py starts a small program per language that hands a handler which
# must not run to the SDK's own entries (run, run_sse, run_ws), under exactly
# that, and holds what the process does to expect in expected.json's "$probe".
# Every language is held to every case, with no list of exceptions: a language
# with no probe program, one that does not build, or an SDK that does not answer
# fails the run, strict or not. --strict adds the missing toolchain. Not run with
# --slice.
# =============================================================================
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SDK="$(cd "$HERE/.." && pwd)"
PROGRAM_DIR="$(mktemp -d)"
trap 'rm -rf "$PROGRAM_DIR"' EXIT

strict=0
slice=""
langs=()
while [ $# -gt 0 ]; do
	case "$1" in
	--strict) strict=1 ;;
	--slice)
		[ $# -ge 2 ] || { echo "--slice needs the slice's Backbone URL"; exit 2; }
		slice="$2"
		shift
		;;
	*) langs+=("$1") ;;
	esac
	shift
done
only=()
if [ -n "$slice" ]; then
	export DRIFT_CONFORMANCE_BACKBONE_URL="$slice"
	only=(--only queue_)
fi
[ ${#langs[@]} -eq 0 ] && langs=(go node python ruby php rust)

pass=0
fail=0
skip=0
skipped_names=""

# toolchain <language> — the command whose absence means "cannot run this one".
toolchain() {
	case "$1" in
	go) echo "go" ;;
	node) echo "node" ;;
	python) echo "python3" ;;
	ruby) echo "ruby" ;;
	php) echo "php" ;;
	rust) echo "cargo" ;;
	esac
}

# drive <language> — print the driver's JSON on stdout.
drive() {
	case "$1" in
	go) (cd "$SDK" && go run ./conformance/go) ;;
	node) node "$HERE/run_node.js" ;;
	python) python3 "$HERE/run_python.py" ;;
	ruby) ruby "$HERE/run_ruby.rb" ;;
	php) php "$HERE/run_php.php" ;;
	rust) (cd "$HERE/rust" && cargo run --quiet 2>/dev/null) ;;
	esac
}

# rust_exe <bin>: print the path of one of the conformance crate's binaries,
# found from cargo's own report, so the process the cases start is the program
# and not a `cargo run` in front of it. Fails when it does not build.
rust_exe() {
	(cd "$HERE/rust" && cargo build --quiet --bin "$1" --message-format=json |
		python3 -c '
import json, sys
for line in sys.stdin:
    try:
        message = json.loads(line)
    except ValueError:
        continue
    if message.get("reason") == "compiler-artifact" and message.get("executable") and message["target"]["name"] == sys.argv[1]:
        print(message["executable"])
' "$1")
}

# loop_program <language>: build what the loop cases run, and set LOOP_PROGRAM
# to the command that starts it. Empty for a language that is not applicable:
# loop_check.py says so itself. Fails when the program does not build.
loop_program() {
	LOOP_PROGRAM=()
	case "$1" in
	go)
		(cd "$SDK" && go build -o "$PROGRAM_DIR/go-loop" ./conformance/go/loop) || return 1
		LOOP_PROGRAM=("$PROGRAM_DIR/go-loop")
		;;
	ruby) LOOP_PROGRAM=(ruby "$HERE/loop_ruby.rb") ;;
	php) LOOP_PROGRAM=(php "$HERE/loop_php.php") ;;
	rust)
		local exe
		exe="$(rust_exe conformance-rust-loop)" || return 1
		[ -n "$exe" ] || return 1
		LOOP_PROGRAM=("$exe")
		;;
	esac
}

# loop_cases <language>: the loop cases against that language's program.
loop_cases() {
	if ! loop_program "$1"; then
		echo "  $1 loop: the program the loop cases run did not build"
		return 1
	fi
	python3 "$HERE/loop_check.py" "$1" -- ${LOOP_PROGRAM[@]+"${LOOP_PROGRAM[@]}"}
}

# probe_program <language>: build what the probe cases run, and set PROBE_PROGRAM
# to the command that starts it. Every language has one; a language without
# stays empty and probe_check.py fails it. Fails when the program does not build.
probe_program() {
	PROBE_PROGRAM=()
	case "$1" in
	go)
		(cd "$SDK" && go build -o "$PROGRAM_DIR/go-probe" ./conformance/go/probe) || return 1
		PROBE_PROGRAM=("$PROGRAM_DIR/go-probe")
		;;
	node) PROBE_PROGRAM=(node "$HERE/probe_node.js") ;;
	python) PROBE_PROGRAM=(python3 "$HERE/probe_python.py") ;;
	ruby) PROBE_PROGRAM=(ruby "$HERE/probe_ruby.rb") ;;
	php) PROBE_PROGRAM=(php "$HERE/probe_php.php") ;;
	rust)
		local exe
		exe="$(rust_exe conformance-rust-probe)" || return 1
		[ -n "$exe" ] || return 1
		PROBE_PROGRAM=("$exe")
		;;
	esac
}

# probe_cases <language>: the probe cases against that language's program.
probe_cases() {
	if ! probe_program "$1"; then
		echo "  $1 probe: the program the probe cases run did not build"
		return 1
	fi
	python3 "$HERE/probe_check.py" "$1" -- ${PROBE_PROGRAM[@]+"${PROBE_PROGRAM[@]}"}
}

echo
if [ -n "$slice" ]; then
	echo "── SDK conformance: the queue cases, against the slice at $slice ──"
else
	echo "── SDK conformance: the same cases, six languages, the loop cases and the start check ──"
fi
echo

for lang in "${langs[@]}"; do
	tool="$(toolchain "$lang")"
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "  $lang: SKIPPED — no $tool on this machine"
		skip=$((skip + 1))
		skipped_names="$skipped_names $lang"
		continue
	fi
	ok=1
	drive "$lang" 2>/dev/null | python3 "$HERE/check.py" "$lang" "${only[@]}" || ok=0
	if [ -z "$slice" ]; then
		loop_cases "$lang" || ok=0
		probe_cases "$lang" || ok=0
	fi
	if [ "$ok" -eq 1 ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
	fi
done

echo
ran=$((pass + fail))
echo "── $ran language(s) ran: $pass passed, $fail failed; $skip skipped"

if [ "$skip" -gt 0 ]; then
	echo "   skipped:$skipped_names"
	if [ "$strict" -eq 1 ]; then
		echo "   ❌ --strict: a skipped language is a language nobody tested."
		exit 1
	fi
	echo "   A skip asserts NOTHING about that language. CI runs with --strict."
fi

[ "$fail" -eq 0 ] || exit 1
[ "$ran" -gt 0 ] || { echo "   ❌ no language ran at all"; exit 1; }
echo "   ✅ every language that ran agrees with expected.json"
