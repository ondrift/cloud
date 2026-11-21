# cli: rules of procedure

These rules add to `specs/procedure.md` at the repository root and never contradict it. Read that
first. This file is only what is true of the CLI.

## What it is

The `drift` binary, Go module `github.com/ondrift/cloud/cli` (`cli/go.mod`), entry point
`cli/cmd/drift`. It is the user's only client: it talks to the platform's api and holds no
platform logic. It has its own version line (`cli/vX.Y.Z`), built for darwin and linux on amd64
and arm64, installed with `go install github.com/ondrift/cloud/cli/cmd/drift@<version>` or from
the Homebrew tap, and updated by `drift upgrade` (`cmd/upgrade`), which runs that same
`go install`, tells a Homebrew install to use `brew upgrade drift` instead, and asks GitHub only
which version is latest.

It is not the owner of anything it reads. The platform owns the Driftfile format, the route
contract and what a slice costs; the CLI states which it implements and says so when they differ.

## Its surface, and what it holds

The commands under `cli/cmd/` (one directory per group). The credentials it holds are the user's:
`~/.drift/session.json` (tokens and the active slice, one profile per account), a per-machine
`~/.drift/device_id` that a refresh token is bound to, and the cached Driftfile schema
`~/.drift/driftfile.schema.json`, fetched from the api once and kept. The API address is compiled
in (`common.APIBaseURL`) and overridden at run time by `DRIFT_API_URL`.

## Invariants

Each is pinned by a test; read it before you change the rule.

- **The Driftfile is the whole declaration.** Nothing reads a function's trigger, gate or memory
  out of source comments. `cmd/atomic/common/parse.go` only finds the callable the manifest names.
- **The schema in `testdata` is at least the format the CLI implements.** CI installs
  `cli/testdata/driftfile.schema.json` as the cache, so the tests never depend on what the api
  serves today. A change that writes a new Driftfile key copies the platform's
  `src/common/driftfile/driftfile.schema.json` over it and raises `ImplementedDriftfileFormat`
  (`common/driftfile.go`) in the same pull request, and does not wait for the api to deploy.
  `TestTheTestSchemaIsAtLeastTheFormatTheCLIImplements` fails a bump that forgot. The CI step
  that compares the copy with the served schema only warns: a copy can rot, and alpha lagging is
  a fact about the world, not about the diff.
- **The route contract major is stated, not enforced.** `ImplementedRouteContract`
  (`common/routecontract.go`) moves with the api's own version when a route is removed or changes
  meaning. Nothing blocks or retries on skew; the CLI says so. `cmd/drift/routes_test.go` holds
  every call to the canonical route spellings.
- **The failure-code registry is append only.** `common/errorcodes.json` is embedded in the binary
  so `drift doctor explain` works during an outage. A code is never renumbered, reused or deleted
  (retire it in place), its `key` never changes, and the sequence stays dense. CI hands the
  base branch's copy to `TestErrorCodes_AppendOnlyAgainstBase` and fails the job if that test does
  not run to a pass, so a rename of the test cannot turn the gate into a silent skip.
- **`cloud/sites` holds a byte-identical copy of the registry** (`docs/src/errorcodes.json`) and
  generates the troubleshooting pages from it, and the CLI prints the address of a code's section
  there. Two tests (`errorcodes_docscopy_test.go`, `errorcodes_page_test.go`) compare them where a
  checkout of `cloud/sites` stands beside this one, and skip where it does not. After a registry
  change, copy it to `cloud/sites` in a pull request there.
- **A 401 is not always an expired session.** `isApplicationRefusal` (`common/request.go`) lets a
  401 that carries a machine-readable key (`step_up_required`, `passphrase_required`) through
  unchanged, because refreshing and re-sending would spend a single-use credential twice and hide
  the real refusal. It matches the key, never the sentence, and restores the body on every path
  out (`TestIsApplicationRefusal_RestoresABodyLargerThanThePeek`). Before adding a single-use or
  purpose-bound credential to a request, read what the transport does with a 401.
- **A command returns its error; none prints one and exits.** `cmd/drift/errorconvention_test.go`
  and `exitstatus_test.go` hold this, so a failure exits non-zero.
- **A name leaves in two steps.** A command, flag or spelling is deprecated first (it keeps
  working and says once, on stderr, what to type instead; `common/deprecate.go`) and removed
  later. Nothing the CLI says may send someone to a page that no longer answers
  (`cmd/drift/retiredsurface_test.go`) or tell them to type something that does not parse
  (`typeablehints_test.go`).
- **`--yes` skips a cost prompt or a first-stage confirmation and never the identifier.** The
  target (a slice name, an account name, a snapshot id) is a required argument and must still match
  with `--yes`. `cmd/account/delete_gate_test.go` pins it for `account delete`.
- **The version is the tag.** `var version` in `cmd/drift/main.go` stays empty. `drift upgrade`
  resolves a version from tags that begin `cli/` (`common/release.go`: `CLIRepo`, `CLITagPrefix`)
  and ignores any other prefix, so an `sdk/` tag is never offered as the CLI's next version.
- **The Go install path follows the major** (`CLIInstallPath`). The module is on v0 and carries
  no suffix.
- **Nothing a user reads names Kubernetes.** Output, help, errors and the text the CLI generates
  into a user's project say instance, slice and platform.

## How it relates to the SDK

The CLI generates the program a function runs and vendors the SDK into it. For Python, Node, Ruby
and PHP a function that declares only the SDK is laid out from a tagged tarball, resolved by
`git/matching-refs/tags/sdk/` and not the full tag list (`cmd/atomic/cmd/deploy/sdkvendor.go`);
`DRIFT_SDK_SOURCE` names a checkout instead and nothing is downloaded. Go functions are built
against `github.com/ondrift/cloud/sdk@latest` (`DriftGoModule`). The generated Go entry point
imports `sdk/go/genmain`, so a change to `genmain` is a change to what released CLIs generate:
read `sdk/specs/procedure.md`. A change that moves an SDK import path or its major reaches the
templates the comment in `cmd/atomic/common/sdk.go` lists.

## Release

Through the root procedure: the prefix of the branch decides, the tag is `cli/vX.Y.Z`, and the
release builds the four binaries, publishes them and the checksums to GitHub, and updates the
Homebrew formula. `cloud/sites` documents the CLI and quotes or paraphrases some of its output, so
a change to a printed line can make that site disagree with the binary; check it there.

## What proves what

- `go build`, `go vet`, `gofmt -l` and `go test ./...` from `cli/` are the whole CLI job.
- The tests touch no platform: fake servers answer, and the SDK comes from this checkout.
- A passing unit run does not prove a command works against a live platform, a new schema key
  against the api that serves it, or `go install` of a tag. Those are not checked in this
  repository.

## Never

- Never edit `var version`, or add a version to a commit.
- Never renumber, reuse, delete or rename the key of a code in `errorcodes.json`.
- Never match a refusal by its prose.
- Never let `--yes` remove the typed identifier.
- Never print a token, a passphrase or a key.
- Never run `gofumpt` and commit what it changes: the gate is plain `gofmt`.
