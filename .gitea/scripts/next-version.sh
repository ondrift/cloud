#!/usr/bin/env bash
# next-version.sh <prefix> <bump> — the next version on one release line, read
# from this checkout's tags: `cli` or `sdk`, and `patch`, `minor` or `major`.
# Prints `vX.Y.Z`.
#
# The ONE place the arithmetic lives. release.yml's plan job asks it what to
# cut, and each release job asks it again when the version it was planned at
# turns out to have been released by a concurrent run, so the two can never
# disagree about what "next" means.
#
# BOTH LINES STAY ON 0.x FOR THE WHOLE ALPHA, deliberately. `cli` is what people
# install and `sdk` is what they import, so a 1.0.0 here is a stability promise
# made to someone else, and Go changes module resolution at v1. Read the
# arithmetic with MA=0 and notice that NO bump leaves 0.x: `major` moves the
# minor, `minor` moves the minor, `patch` moves the patch. The guard at the end
# refuses anything else, so an edit to the arithmetic fails here instead of
# quietly publishing a stability claim. 1.0.0 is reachable only by cutting a
# tag by hand.
set -euo pipefail

prefix="${1:?usage: next-version.sh <cli|sdk> <patch|minor|major>}"
bump="${2:?usage: next-version.sh <cli|sdk> <patch|minor|major>}"

last=$(git tag -l "${prefix}/v*" | sed "s|^${prefix}/v||" \
       | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
[ -n "$last" ] || last="0.0.0"
IFS=. read -r MA MI PA <<EOF
$last
EOF
case "$bump" in
  major) if [ "$MA" -eq 0 ]; then MI=$((MI+1)); PA=0; else MA=$((MA+1)); MI=0; PA=0; fi ;;
  minor) MI=$((MI+1)); PA=0 ;;
  patch) PA=$((PA+1)) ;;
  *) echo "next-version.sh: unknown bump '$bump'" >&2; exit 2 ;;
esac
v="v${MA}.${MI}.${PA}"

case "$v" in
  v0.*) echo "$v" ;;
  *)
    echo "::error::refusing to cut ${prefix}/${v}: both lines stay on 0.x for the alpha." >&2
    echo "cli is installed and sdk is imported, so a 1.0.0 is a promise made to" >&2
    echo "someone else, and Go changes module resolution at v1. If leaving the alpha" >&2
    echo "is the intent, cut the tag by hand and remove this guard in the same change." >&2
    exit 1 ;;
esac
