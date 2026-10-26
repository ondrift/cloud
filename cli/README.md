# Drift CLI

`drift` is the command-line client for [Drift](https://ondrift.eu) — a simple, European serverless cloud. Everything you do on Drift happens through this one tool: create your environment, deploy serverless functions in six languages, host static sites, store data, and ship an entire application with a single command. There is no web dashboard and no browser step — running `drift` on its own opens a full-screen terminal dashboard (TUI) over your slices, functions, and data, and shaping a slice draws a form in the terminal too.

A **slice** is your isolated environment. It bundles three primitives:

- **Atomic** — serverless functions in Go, Python, Node.js, Ruby, PHP, and Rust.
- **Backbone** — your data and state: secrets, a NoSQL store, SQL databases, queues, blobs, a cache, locks, realtime channels, passwordless auth (KeyAuth), and a zero-knowledge vault.
- **Canvas** — static site hosting, served same-origin with your functions (no CORS).

## Install

```bash
go install github.com/ondrift/cloud/cli/cmd/drift@latest
```

This installs the `drift` binary into your `$GOBIN`. Or build from source:

```bash
git clone https://github.com/ondrift/cloud/cli && cd cli
go build -o drift ./cmd/drift
```

## Getting started

```bash
drift account create                   # sign up (email verification)
drift file new                         # write a Driftfile: the slice, its shape, one pulse
drift atomic new hello -l go -m get    # write the source the pulse declares
drift plan                             # what applying it creates, and what it costs
drift apply                            # create the slice and deploy
```

The Driftfile is one HCL file declaring the whole slice: what runs on it and its
shape, every booking beside what it books. `drift file explain` describes every
block it can hold.

## Commands

### Dashboard

| Command | Description |
|---------|-------------|
| `drift` | With no arguments in a terminal, launches the full-screen interactive dashboard (a btop/k9s-style TUI) over your slices, functions, and Backbone data. In a pipe or CI (no TTY) it falls back to `--help`. |

### Account

| Command | Description |
|---------|-------------|
| `drift account create` | Sign up — username, email, password, email OTP verification. |
| `drift account login` | Authenticate; stores a session token in `~/.drift/session.json`. |
| `drift upgrade [version]` | Update the CLI via `go install` — latest by default, or pin a version (e.g. `drift upgrade v1.8.1`) to roll back. The dashboard nudges you when a newer release exists. |

### Slices

| Command | Description |
|---------|-------------|
| `drift file new` | Write a starter Driftfile, asking for the slice's name and the first pulse; `drift apply` then creates the slice. |
| `drift slice list` | List your slices; the active one is marked. |
| `drift slice use <name>` | Set the active slice for subsequent commands. |
| `drift slice resize [name]` | Reshape a slice on the same form, opened on what it already is (defaults to the active slice). Repricing and removal are confirmed on the form itself. |
| `drift slice restart` | Restart the active slice. |
| `drift slice delete <name>` | Delete a slice (double confirmation). |
| `drift slice domain verify\|remove\|list <host>` | Verify, list and remove custom domains. Domains are declared as `domain` blocks in the Driftfile's `edge` block. |
| `drift slice link list` | List the slices the active slice may call, in-cluster (e.g. an app → your own observability slice). Links are declared as `links` in the Driftfile's `edge` block; use one from code with `drift.Edge.Slice("<slice>")`. |
| `drift slice snapshot create\|list\|download\|restore\|delete` | Portable backups — your data, with no Drift-specific files. |

### Atomic — serverless functions

| Command | Description |
|---------|-------------|
| `drift atomic new [name]` | Add a function to an element &mdash; a flat file under `atomic/`, not a per-function folder. HTTP or queue trigger. Flags: `-l/--lang`, `-m/--method`, `-q/--queue`, `-a/--auth`, `-e/--element`. |
| `drift atomic fetch [path]` | Resolve dependencies for every function found under a path. |
| `drift atomic run <dir>` | Run a function locally with hot reload. |
| `drift atomic deploy <dir>` | Build, archive, and deploy a function. |
| `drift atomic list` | List deployed functions. |
| `drift atomic logs <name>` | View a function's logs (`logs purge` clears them). |
| `drift atomic metrics <name>` | Request count, error rate, average duration. |
| `drift atomic redeploy\|rollback\|history <name>` | Manage deployed versions. |
| `drift atomic trigger\|alert\|egress\|auth <name>` | Configure triggers, alerts, outbound egress, and API-key auth. |
| `drift atomic element list` | List your elements (single-language backends) and the functions each contains. |
| `drift atomic delete <name>` | Remove a function. |

### Backbone — data

| Command | Description |
|---------|-------------|
| `drift backbone secret set\|get\|delete` | Encrypted secrets (the key never reaches your code). |
| `drift backbone nosql write\|read\|list\|drop` | NoSQL collections. |
| `drift backbone sql list\|drop` | Per-slice SQLite databases. |
| `drift backbone queue push\|pop\|peek\|len\|drop` | Queues. |
| `drift backbone blob put\|get\|list\|delete` | Blob storage. |
| `drift backbone cache set\|get\|del\|exists` | Ephemeral cache. |
| `drift backbone lock acquire\|release\|renew` | Distributed locks. |
| `drift backbone status` | Backbone health and resource usage for the active slice. |

### Canvas — static sites

| Command | Description |
|---------|-------------|
| `drift canvas deploy <dir>` | Deploy a static site, served same-origin with your functions. |

### The Driftfile

| Command | Description |
|---------|-------------|
| `drift plan [env]` | Compare the Driftfile with the live slice: every difference, the monthly price before and after (quoted by the platform), whether the slice restarts, and what a removal could lose. Changes nothing. `--out plan.json` records it. |
| `drift apply [env \| plan.json]` | Make the slice what the Driftfile says: create or resize it, asking before the price moves, before anything is removed and before any data could be lost, then deploy. Pulses and fields whose source is unchanged are skipped; `--force` redeploys everything. |
| `drift size` | Write what each pulse and field has measured into the Driftfile's `memory` lines, shown as a diff. |
| `drift file new` | Write a starter Driftfile. |
| `drift file lint` | Validate the Driftfile exactly as `drift plan` would, offline and without its secrets. |
| `drift file explain [block]` | Describe a block or attribute of the Driftfile format. |
| `drift file resolve [--env]` | Print what the file provisions: every booking, size and limit. |
| `drift file fmt [--write]` | Lay the file out canonically, keeping its comments. |
| `drift file run [env]` | Build and run the whole project locally in Docker — no account, no cloud. |
| `drift file test [env]` | Run the project locally and run its `test` commands against it, always tearing the instance down afterward. |
| `drift file stop [env]` / `logs [env]` | Stop or tail the local run. |
| `drift file benchmark` | What each pulse and field has cost, measured by the slice, and what it should book. |

### Migrate — leave another cloud

Free, read-only, and honest about the parts that don't fit. Drift never holds your
cloud credentials or your data — every command runs locally with your own provider
login. The first provider is Azure.

| Command | Description |
|---------|-------------|
| `drift migrate azure estimate -g <rg>` | Read-only: map an Azure resource group's cost to the equivalent Drift slice. |
| `drift migrate azure snapshot -g <rg> -o <dir>` | Read-only: pull function source (Python/Node, incl. Linux Consumption), Cosmos Mongo documents, blobs, queue messages (peeked, never dequeued), `$web` static sites, and app settings into a vendor-neutral folder you own. |
| `drift migrate azure transform -i <dir> -o <dir>` | Offline, deterministic: rewrite the export into a deployable Drift project (validated `Driftfile` + scaffolds). |
| `drift migrate azure apply -i <dir>` | Deploy the workspace (`-i`/`--in`, default `./drift_workspace`); hard-refuses around anything in `REFUSED.md` unless `--accept-refusals`. |

Anything that can't move is written to `REFUSED.md` with a reason; review it and
`REPORT.md` before deploying. Cosmos export needs `mongoexport`; Linux Consumption
source needs `squashfs-tools` — the tool prints the exact install if either is missing.

## Configuration

| Variable | Purpose |
|----------|---------|
| `NO_COLOR` | Disable coloured output. |

Session tokens live in `~/.drift/session.json`.

## License

MIT — see [LICENSE](LICENSE).
