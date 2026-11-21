# Procedure: from a branch to a published release

How a change travels through this repository, and why each rule is there. `cli/` and `sdk/` have a
`specs/procedure.md` of their own; each adds to this one and never contradicts it.

The mechanics are owned by the workflows and the script, each with a header that says what it does
and why. This file states the rules and the reasons and points at the owner. It does not copy what
they derive.

## What this repository is

Two Go modules, `github.com/ondrift/cloud/cli` (`cli/go.mod`) and `github.com/ondrift/cloud/sdk`
(`sdk/go.mod`), on two version lines, in one repository. The CLI is what people install; the SDK
is what their functions import, in six languages. Everyone who consumes either one reaches it
through GitHub (`github.com/ondrift/cloud`), while the source, the review and the build are on
this instance. The root `package.json` and `composer.json` are manifests for npm and composer,
which can only install from a repository root; they describe the SDK with paths relative to the
root, and `sdk/package.json` and `sdk/composer.json` describe the same code relative to `sdk/`.

## The path of a change

1. Branch from `origin/main` as `<prefix>/<what-you-did>`. The prefix decides the release.
2. Change the code, with its tests beside it. Nothing here runs a gate for you (no Makefile, no
   script, no hook), so before you push run what CI runs, from the part you changed:
   - `cli/`: `gofmt -l .` (it must print nothing; plain `gofmt`, not `gofumpt`), `go build ./...`,
     `go vet ./...`, `go test ./... -count=1`.
   - `sdk/`: the same four, then the per-language tests and `bash conformance/run.sh --strict`
     that `.gitea/workflows/pr.yml` lists step by step. They need `php`, `ruby`, `node`,
     `python3` and `cargo`; a missing toolchain is a failure, never a skip.
   - `.gitea/scripts/`: `bash .gitea/scripts/next-version_test.sh`. CI does not run it.
3. Push the branch and open the pull request on Gitea. The title starts `#<item> - `. The body has
   the three sections of `.github/pull_request_template.md`: what it implements, what was tested
   (and what was not), and the pasted test results. A `breaking/` branch's body also carries one
   line, `Breaking: ruby, node`, naming the SDK languages it breaks, lower case, comma separated,
   from `go python node ruby php rust`.
4. `.gitea/workflows/pr.yml` runs on the pull request. It is path aware: `cli/` changes run the
   CLI job, `sdk/` changes run the SDK and conformance jobs, anything else runs neither. The job
   named `all gates` is the one to require: it passes when every job that ran passed, treats a
   skip as fine, and fails if `sdk/` changed and conformance did not run.
5. A person merges. An agent never does.
6. `.gitea/workflows/release.yml` runs on the merged pull request. It reads the branch prefix,
   works out which of `cli/` and `sdk/` the merge touched, asks `.gitea/scripts/next-version.sh`
   for each next version, builds, publishes to GitHub and tags both sides. A `chore/`, `docs/`,
   `ci/` or `test/` branch, or a merge that touched neither directory, releases nothing, and the
   change ships with the next release of its part. `workflow_dispatch` on the same file cuts a
   release by hand (a module and a bump).

## Rules, and why

### Main is written by merge
Every change is a pull request so CI and a reviewer see it first. Nothing local refuses a push to
`main`, so the rule is kept deliberately: branch, push the branch, open the pull request.

### The branch prefix is the release
It is the only input `release.yml` has for the bump, and `pr.yml` fails a prefix it does not
recognise, so a merge cannot silently release nothing.

| prefix | bump | |
|---|---|---|
| `feature/` | minor | a new capability |
| `bugfix/` | patch | a fix |
| `breaking/` | major | an incompatible change |
| `chore/` `docs/` `ci/` `test/` | none | nothing is tagged or published |

Both lines stay on 0.x. `next-version.sh` makes `major` move the minor and refuses any result
outside 0.x, because a 1.0.0 is a stability promise to the people who install and import, and Go
changes module resolution at v1. Leaving 0.x is a hand-cut tag and a change to that script in the
same pull request.

### The version is the tag, and the tag carries its directory
Release tags are `cli/vX.Y.Z` and `sdk/vX.Y.Z`. Go resolves a module in a subdirectory by a
prefixed tag and silently ignores a bare `vX.Y.Z`. The CLI is stamped with the tag
(`-X main.version`) and the release verifies the binary reports it, so `var version` in
`cli/cmd/drift/main.go` is a development fallback that no pull request edits. No release commits
anything: every merge commit here is unsigned and GitHub's `main` requires signed commits.

### The import paths cannot move
`github.com/ondrift/cloud/cli` and `github.com/ondrift/cloud/sdk` are written in the `go.mod` of
every consumer and of every Go function the CLI builds. The module proxy fetches them from GitHub
by tag. Renaming, moving a module to another directory, or putting either under a `/vN` suffix
breaks all of them. The suffix is Go's rule for v2 and above and both modules are on v0, so they
carry none; a major bump of the SDK would also need the CLI's templates changed
(`cli/cmd/atomic/common/sdk.go` lists where). A Go module path is not a clone URL:
`github.com/ondrift/cloud/sdk` 404s as a git remote, and the repository to clone is
`https://github.com/ondrift/cloud`.

### GitHub receives tags, never the branch
GitHub's `main` demands verified signatures and passing checks that cannot run until the commit
is there; this instance's merge commits are unsigned. A tag is outside both rules, carries the
commits it points at, and is all the module proxy, Homebrew and `drift upgrade` read. So
`release.yml` pushes the tag and nothing else, and GitHub's default branch lags this one. Do not
push `main` there, and do not read the lag as lost work.

The order inside a release is the correctness argument: GitHub is written first and completely
(tag, release, assets, the Homebrew formula in `ondrift/homebrew-tap`), the tag on this instance
is written last. A tag here therefore means fully published. A run that dies halfway leaves no tag
here, and the next run plans the same version. The pre-flight refuses a version that exists on one
side only and says which; do not bump past it, that skips a version.

### Merge release pull requests one at a time
The plan step reads the tags that exist when it runs. Two pull requests merged seconds apart can
both plan the same version. The release job re-checks and takes the next one when it finds both
sides already hold the planned tag, and it retries up to five times, but a person who merges
two together should still wait for the first release's tag (check the tags, not the run). A
release run that fails on a merge leaves that merge unreleased until the next one.

### A pull request description reaches github.com
The tag carries the commit messages, and a squash message includes the pull request description.
`.gitea/default_merge_message/` keeps the instance's reviewer trailer out of merge messages; it
cannot filter a description, so none quotes an instance URL, a repository path on it, or a
credential. A published commit is not rewritten: the module proxy has recorded its hash.

### Secrets
The release holds a GitHub token and a tap token as repository secrets, and pushes the tag here
as a release identity because tags on this instance are protected and the Actions identity cannot
be allowed. A secret is checked for presence and never printed; a workflow secret cannot be named
with a `GITEA_` prefix, and the refusal happens when it is saved. `secrets.GITHUB_TOKEN` cannot
reach another repository.

### The required check is `all gates`
Renaming that job in `pr.yml` leaves a required context nothing reports and a pull request that
can never go green. `cli`, `sdk` and `conformance` skip by design, so they cannot be the required
ones.

### The root manifests are outside the path filter
`pr.yml` and `release.yml` decide by `^cli/` and `^sdk/`. A change to the root `package.json` or
`composer.json` alone runs no SDK job and releases nothing. The SDK version check reads
`sdk/package.json` and not the root one; keep the two files describing the same code.

### Nothing a user reads names Kubernetes
CLI output, error messages, and the text the SDK raises say instance, slice and platform.

## What is generated or derived

- The version of a release, from the tags by `next-version.sh`.
- `Formula/drift.rb` in the tap, written by the release job. A hand edit lasts until the next
  release.
- `cli/common/errorcodes.json` is hand-written (append only; see `cli/specs/procedure.md`) and
  copied byte for byte into `cloud/sites`.
- `cli/testdata/driftfile.schema.json` is a copy of the platform's Driftfile schema.

## Tests that need something outside the repository

- `go test ./...` in `sdk/` needs the Go toolchain only. The CLI's `cmd/project` tests read the
  Driftfile schema from `~/.drift/driftfile.schema.json` and fail without one; CI copies
  `cli/testdata/driftfile.schema.json` there first, and so should a machine with no cache (run
  with a `HOME` that holds the copy, so your own cache is not replaced).
- The SDK's six-language tests need the six toolchains. The queue cases of conformance can also run
  against a standalone slice (`--slice <url>`); CI does that with the published runner image.
- The error-code tests that compare with `cloud/sites` skip unless a checkout of it stands beside
  this one (`cloud/public` and `cloud/sites` under `cloud/`).
- The CLI's tests vendor the SDK from this checkout (`DRIFT_SDK_SOURCE`, set by the `cmd/project`
  test main) so they do not depend on GitHub answering.
