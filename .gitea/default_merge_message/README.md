# Merge message templates

**This repository's commits reach github.com, and the default merge message
names the machine they were made on.**

Gitea builds a merge commit message itself and appends two trailers:

```
Merge pull request 'Push the tag to GitHub, not the branch' (#76) from …

Reviewed-on: https://git.ondrift.eu/cloud/public/pulls/76
```

That is fine on an instance nobody outside can see. It is not fine here: the
release job pushes tags to `github.com/ondrift/cloud`, a tag push carries the
commits it points at, and those commit messages are then public — naming the
internal host, its repository paths and its pull-request numbers, on a page
anyone can read.

Three commits went out that way before this existed: `68af995`, `f801e98`,
`7c745f5`, reachable from `sdk/v0.9.2`. They are not being rewritten — see
below.

## What these files do

Gitea reads `<STYLE>_TEMPLATE.md` from this directory on the BASE branch when
it builds a merge message, and uses it instead of the default. `${ReviewedOn}`
and `${ReviewedBy}` are template variables, so a template that does not mention
them does not get them.

| file | merge style |
|---|---|
| `MERGE_TEMPLATE.md` | a merge commit |
| `REBASE-MERGE_TEMPLATE.md` | rebase, then merge commit |
| `SQUASH_TEMPLATE.md` | squash |

There is deliberately no `REBASE_TEMPLATE.md` or `MANUALLY-MERGED_TEMPLATE.md`:
plain rebase writes no merge commit, and a manual merge carries whatever message
the person typed.

⚠ **A squash message includes the pull request DESCRIPTION.** If a description
ever quotes an instance URL, the squash carries it. The templates cannot filter
that; writing the description is where it has to be caught.

## Why the three published commits are not being rewritten

The tag they are reachable from is already cached by the Go module proxy, with
the commit hash recorded:

```
proxy.golang.org/github.com/ondrift/cloud/sdk/@v/v0.9.2.info
  → {"Version":"v0.9.2", … "Hash":"68af99541110…", "Ref":"refs/tags/sdk/v0.9.2"}
```

Re-cutting `v0.9.2` at a different commit would give anyone who already fetched
it a `go.sum` mismatch — a real breakage for consumers, to remove a hostname
that public DNS already answers for. **The disclosure is that the host runs
Gitea and these repository paths exist; it is not a credential.** Not worth it.

## If a message still carries the trailer

The template is read from the base branch, so it only takes effect for merges
after this lands. If one appears afterwards, the template is not being picked up
and the next lever is the merge API, which takes `merge_message_field` and
`merge_title_field` directly.
