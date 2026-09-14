#!/usr/bin/env bash
# =============================================================================
# run.sh — the same conformance cases against every SDK.
#
#   bash sdk/conformance/run.sh            every language whose toolchain is here
#   bash sdk/conformance/run.sh node ruby  just those
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
# =============================================================================
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SDK="$(cd "$HERE/.." && pwd)"

strict=0
langs=()
for arg in "$@"; do
	case "$arg" in
	--strict) strict=1 ;;
	*) langs+=("$arg") ;;
	esac
done
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

echo
echo "── SDK conformance: the same cases, six languages ──"
echo

for lang in "${langs[@]}"; do
	tool="$(toolchain "$lang")"
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "  $lang: SKIPPED — no $tool on this machine"
		skip=$((skip + 1))
		skipped_names="$skipped_names $lang"
		continue
	fi
	if drive "$lang" 2>/dev/null | python3 "$HERE/check.py" "$lang"; then
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
