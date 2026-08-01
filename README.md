# Drift Cloud — CLI and SDK

The user-facing halves of [Drift](https://ondrift.eu): the `drift` command-line
interface and the six-language SDK that deployed functions import.

| Path | Module / package | What it is |
|---|---|---|
| [`cli/`](cli/) | `github.com/ondrift/cloud/cli` | The `drift` CLI |
| [`sdk/`](sdk/) | `github.com/ondrift/cloud/sdk` | The SDK, in six languages |

Both were separate repositories until 2026-08-01 (`ondrift/cli` and
`ondrift/sdk`). Their full histories are preserved here — this is a merge, not
a re-import, so `git log --follow` works across the move.

Drift is in closed alpha. Both are versioned `v0.1.x` to say so plainly.
