# cloud/public

The public monorepo of Drift: `cli/`, the `drift` command-line client (Go module
`github.com/ondrift/cloud/cli`), and `sdk/`, the library user functions import, in six languages
(Go, Python, Node, Ruby, PHP, Rust) under one version (Go module `github.com/ondrift/cloud/sdk`).
The two are separate modules on separate version lines. The root `package.json` and
`composer.json` exist only so npm and composer can install the SDK from a repository root.

The rules of procedure are in `specs/`. Read `specs/procedure.md` before you change anything
here, and the `specs/procedure.md` of the part you change (`cli/`, `sdk/`). This file holds only
what is expensive to learn by breaking it.

## The rules that bite

- **Nothing runs locally for you.** There is no Makefile, no script and no hook: the first check
  is CI. Run what `.gitea/workflows/pr.yml` runs, starting with `gofmt -l .` (plain gofmt).
- **Main is written by merge, and an agent never merges.** Branch, push, open the pull request on
  Gitea; a person merges once `all gates` is green.
- **The branch prefix is the release.** `feature/` minor, `bugfix/` patch, `breaking/` major,
  `chore/` `docs/` `ci/` `test/` release nothing; CI fails any other prefix. Pull request titles
  start `#<item> - `.
- **Merge release PRs one at a time.** Two merges seconds apart plan the same version and the
  second release fails. Wait for the first one's tag.
- **The version is the tag.** Never edit `var version` in `cli/cmd/drift/main.go`; a release
  binary is stamped from the tag.
- **The two Go module paths are fixed.** They are in every consumer's `go.mod`, so `cli/` and
  `sdk/` never move, and a tag is `cli/vX.Y.Z` or `sdk/vX.Y.Z`, never bare.
- **GitHub receives tags, not branches.** Its `main` is behind this one on purpose.
- **A pull request description reaches github.com** inside a squash message: no instance URL,
  no credential.
- **A new Driftfile key** is copied into `cli/testdata/driftfile.schema.json` from the platform's
  schema in the same change, and `ImplementedDriftfileFormat` moves with it.
- **Nothing a user reads names Kubernetes.** Say instance, slice, platform.

## Where things are

| | |
|---|---|
| `specs/procedure.md` | how a change travels, from a branch to a published release |
| `cli/specs/procedure.md` | the CLI: invariants, schema, error codes, release |
| `sdk/specs/procedure.md` | the SDK: six languages, conformance, one version |
| `.gitea/workflows/pr.yml` | the only gate a pull request has; job `all gates` is the required check |
| `.gitea/workflows/release.yml` | tags, builds and publishes after a merge; header explains the order |
| `.gitea/scripts/next-version.sh` | the version arithmetic, with a test beside it |
| `.gitea/default_merge_message/` | merge message templates; its README says why |
| `.github/pull_request_template.md` | the three sections a pull request body carries |
| `README.md` | the layout table; a layout change updates it in the same change |

Mechanics live in the workflows and scripts, not here: each has a header that says what it does
and why.
