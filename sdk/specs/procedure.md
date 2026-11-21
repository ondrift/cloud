# sdk: rules of procedure

These rules add to `specs/procedure.md` at the repository root and never contradict it. Read that
first. This file is only what is true of the SDK.

## What it is

The library a deployed function imports to reach its slice's Backbone (secrets, cache, NoSQL, SQL,
queues, blobs, locks, realtime), Deed (identity) and Edge (calls to a linked slice). One
specification in six implementations: Go (`go/`, module `github.com/ondrift/cloud/sdk`), Python
(`python/drift.py`), Node (`node/index.js`), Ruby (`ruby/drift.rb`), PHP (`php/drift.php`) and
Rust (`rust/`, crate `drift-sdk`). The six share one version line (`sdk/vX.Y.Z`), and a tag
versions all of them. They are not separately released packages, so there is one procedure file
here and none per language.

It is transport. It marshals a request, returns the slice's answer and holds no secret of its own
and no signing key; a signature is passed in by the caller. The interpreted four are single
stdlib-only files, which is what lets the CLI copy them into a function with no package manager.
It is not the platform: what the slice does with a call is the slice's.

## Its surface, and who may call it

User code calls the per-language API in `sdk/README.md`. `go/genmain` is the exception: it is
imported only by the program the CLI generates, it is not API, and it carries a compatibility
promise to the templates of released CLIs alone (its package comment says so). Do not import it
elsewhere. A change to it is a change to what already-released CLIs generate, so keep it
compatible with them. The slice's side of the protocol is in `cloud/platform`
(`src/slice/src/atomic/warmpool.rs` and `protocol.rs`); the README section "How a deployed
function is served" states the contract both sides keep.

## Invariants

- **The same cases pass in all six.** `conformance/expected.json` holds the assertions once; each
  language has a thin driver (`conformance/run_*`, `go/`, `rust/`) that performs the operations
  and prints what it got, and `conformance/check.py` compares. A case a driver does not report is a
  failure, not a skip. A new primitive or a changed behaviour changes `expected.json` and all six
  drivers together. A language whose loop is unbuilt is listed under `"$loop"` `pending`; a pending
  language that passes fails the run until it is taken off the list.
- **A missing toolchain is a failure.** `run.sh` counts a skip apart and CI passes `--strict`.
  Never soften that to make a run shorter; the point is the six.
- **A refusal is an error carrying the slice's words,** in every language. The wire tests
  `php/status_test.php`, `python/status_test.py` and the Rust tests each exist because one language
  got this wrong while the others did not. A SQL statement always sends an args array, never null
  (`node/sql_args_test.js`, `ruby/sql_args_test.rb`, `go/sql_args_test.go`).
- **A deployed process serves many calls from one process.** Under `DRIFT_RUNTIME` the entry
  (`genmain.Serve`, `drift_sdk::run`, `Drift.run`, `\Drift\run`) reads one envelope per line,
  answers with exactly one line, keeps handler output off the response stream, and gives each call
  its own `TMPDIR`, `HOME` and `DRIFT_SECRET_*`. `conformance/loop_check.py` holds each language to
  the `"$loop"` cases, and `conformance/loop_check_test.py` proves the controller can fail.
- **A queue works on a real slice.** CI runs the queue cases against a standalone slice from the
  published runner image: a local store agreeing with itself proves nothing about the wire.
- **`callerSlice` authorises nothing.** It reads a header any caller can write; it is a label.
- **No dependency the interpreted SDKs cannot vendor.** Go has none. Rust's HTTP client has no
  bundled TLS by default, so a Rust deploy needs no C cross-compiler; TLS is the opt-in `tls`
  feature.

## One version, six manifests

`sdk/package.json`, `python/pyproject.toml`, `rust/Cargo.toml` and `ruby/drift.gemspec` carry
the same number; the `version consistency` step in `pr.yml` fails a change that leaves one behind.
`go.mod` and both `composer.json` files carry none: Go and PHP resolve from the tag. Run `cargo
check` in `rust/` when you touch the Rust version so `Cargo.lock` follows; never edit the lock by
hand. These numbers are not stamped from the tag, so they do not track the released version; the
check compares them with each other and nothing else.

Because one number covers six languages, a major tracks the most-churning one and the other five
inherit a false signal. So a `breaking/` pull request names the languages it actually breaks in a
`Breaking: ruby, node` line (checked in `pr.yml`). A language not on the line knows the major is
not theirs.

## How the SDK reaches a consumer

By tag, from GitHub, and each language reaches the subdirectory differently: Go resolves the root
module path `github.com/ondrift/cloud/sdk` by the `sdk/` tag prefix; npm cannot install from a
subdirectory and composer reads its manifest from the repository root, which is why the root
`package.json` and `composer.json` exist and point into `sdk/`; pip and bundler reach `sdk/python`
and `sdk/ruby` with a subdirectory and a glob. Name the root module for Go, not a nested path:
the repository's early history holds a nested module whose pseudo-versions still resolve first.
The CLI resolves the latest `sdk/` tag with the `matching-refs` API and vendors the file for the
four interpreted languages (`cli/cmd/atomic/cmd/deploy/sdkvendor.go`). A Go module path is not a
clone URL: `https://github.com/ondrift/cloud/sdk` is not a repository, and `sdk/README.md` and
the Rust, Ruby and PHP install snippets still write it that way.

A Go major bump needs the module path to end in `/vN` and every import, including the CLI's
templates (the list is the comment in `cli/cmd/atomic/common/sdk.go`), to follow in the same
release. Both modules are on v0 and carry no suffix.

## What proves what

- `go build`, `go vet`, `gofmt -l` and `go test ./...` from `sdk/`, the per-language tests, and
  `bash conformance/run.sh --strict`: the steps of the `sdk` and `conformance` jobs in `pr.yml`.
- A language's own tests passing does not show the six agree; only conformance does.
- Nothing here shows a change works inside a running slice beyond the queue cases. Publishing to
  npm, PyPI, RubyGems and crates.io is not part of the release; the tag is the whole release.

## Never

- Never import `go/genmain` outside the CLI's generated program, or break the templates of a
  released CLI.
- Never let a change to one language's behaviour land without its case in `expected.json` and the
  other five drivers.
- Never bump one manifest and not the others.
- Never make a refusal a value (`nil`, `null`, an empty result) where the others raise.
- Never add code that signs or holds a secret to the SDK.
