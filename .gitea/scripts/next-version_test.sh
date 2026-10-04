#!/usr/bin/env bash
# next-version_test.sh — the release arithmetic, against tags in a scratch
# repository.
#
#   bash .gitea/scripts/next-version_test.sh
#
# Includes the concurrent-release case: a run planned cli/v0.64.0, another run
# published it first, and asking again once its tag has arrived must answer
# v0.65.0.
set -uo pipefail

SCRIPT="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
cd "$scratch"
git init -q
git -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m init

passed=0
failed=0
expect() { # <case> <want> <prefix> <bump>
  local got
  got=$(bash "$SCRIPT" "$3" "$4" 2>/dev/null) || got="(refused)"
  if [ "$got" = "$2" ]; then
    echo "  PASS  $1"; passed=$((passed + 1))
  else
    echo "  FAIL  $1: got $got, want $2"; failed=$((failed + 1))
  fi
}
tag() { git -c tag.gpgSign=false tag "$1"; }

expect "no tags yet: minor" v0.1.0 cli minor
expect "no tags yet: patch" v0.0.1 cli patch

tag cli/v0.9.0
tag cli/v0.10.0
tag cli/v0.63.0
tag sdk/v0.11.0
expect "versions sort as numbers, not text" v0.64.0 cli minor
expect "patch moves the patch" v0.63.1 cli patch
expect "major stays on 0.x and moves the minor" v0.64.0 cli major
expect "each line counts only its own tags" v0.11.1 sdk patch

# The race: this run planned v0.64.0, another run published it, and its tag is
# now in this checkout. Asking again gives the next one.
tag cli/v0.64.0
expect "after a concurrent release, the next version" v0.65.0 cli minor

tag cli/v1.2.0
expect "a line past 0.x is refused, never extended" "(refused)" cli minor

out=$(bash "$SCRIPT" cli sideways 2>&1) && { echo "  FAIL  an unknown bump was accepted: $out"; failed=$((failed + 1)); } \
  || { echo "  PASS  an unknown bump is refused"; passed=$((passed + 1)); }

echo
echo "next-version_test: $passed passed, $failed failed"
[ "$failed" = 0 ]
