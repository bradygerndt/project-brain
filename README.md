# project-brain

A self-hosted MCP (Model Context Protocol) memory service for Claude Code. It gives agents a
persistent, searchable memory (keyword + semantic), a place to store file artifacts, and simple
presence/locking primitives for coordinating multiple agents working on the same project.

Built primarily for Claude Code and other CLIs/agents that connect to an MCP server's URL
directly — the same client model Claude Code uses. Claude Desktop and the Claude mobile/web app
work differently and need an extra step to reach a self-hosted instance like this one; see
[Connect Claude Desktop or the mobile/web app](docs/desktop-mobile.md) if you want that instead.

Runs in Docker, is managed through the `brain` CLI, and supports multiple independent instances
(e.g. one per machine or one per project) side by side.

## Features

- **Memory** — key/value facts with tags and namespaces, full-text search (SQLite FTS5), and
  vector similarity search (LanceDB + local embeddings).
- **Artifacts** — store and serve arbitrary files (HTML, images, JSON, …) over HTTP.
- **Agent coordination** — presence pings (`agent_ping`/`agent_list`) and resource locks
  (`lock_acquire`/`lock_release`) for multiple agents sharing one project.
- **Web UI** — browse memory and artifacts at `http://<host>:<mcp-port>/ui`.
- **Multi-instance** — run several named brains (different ports/data volumes), managed with
  the `brain` CLI.

## Requirements

- [Docker](https://docs.docker.com/get-docker/) (the only thing needed to *run* the service —
  server images are pulled prebuilt from GHCR, never built locally)
- `curl` and `tar` (to install on macOS/Linux/WSL — both are preinstalled on virtually every
  system) or PowerShell 5.1+ (built into Windows, to install natively)

The `brain` CLI is a standalone compiled binary — no Node.js, npm, or Go toolchain needed to
install or run it. (Node is only used to build the server image in CI; contributors editing
`src/` need it too — see [Development](#development).)

## Install

**macOS / Linux / WSL:**

```bash
curl -fsSL https://raw.githubusercontent.com/bradygerndt/project-brain/main/install.sh | bash
```

(WSL presents as Linux, so this is the right one there too — running *inside* WSL changes how
LAN/Tailscale access works, see [the WSL section](docs/lan.md#running-inside-wsl) before setting
that up.)

**Windows (native, PowerShell):**

```powershell
irm https://raw.githubusercontent.com/bradygerndt/project-brain/main/install.ps1 | iex
```

Both detect your OS/architecture, download the matching `brain` binary from the
[latest release](https://github.com/bradygerndt/project-brain/releases/latest) — into
`~/.local/bin` on macOS/Linux/WSL (override with `BRAIN_BIN_DIR`), or `%LOCALAPPDATA%\brain` on
Windows — and add that directory to your `PATH` if needed. No repo checkout, no `git clone`, no
package manager.

Re-running the same command later upgrades the CLI to the newest release.

## Quick start

**The overall flow:** each project-brain *instance* is a self-contained Docker container with
its own memory (its own volume, its own MCP + artifacts ports). Run `brain add` to register
your first one, then `brain start` boots it and `brain config` prints the block that tells
Claude Code where to find it. From there you either connect `home` once and reuse it as one
shared memory across every project, or give a particular project its own isolated instance —
both are covered below.

```bash
brain add           # prompts for name/ports/image
brain start         # start all instances (requires Docker)
brain ps            # check status
brain config        # print MCP config to paste into ~/.claude/settings.json
brain open          # open the web UI in your browser
```

Add the printed config to `~/.claude/settings.json` under `"mcpServers"`, then restart Claude
Code. This connects `home` *personally* — every project you open in Claude Code on this
machine will share its memory, which is fine as a default.

**Adding a new project:** if you'd rather a project's memory stay isolated (its own facts, not
mixed with unrelated work), or the whole team should share one instance via git instead of
everyone running their own, give it a dedicated instance:

```bash
brain add work 3589 3590   # new instance "work" on its own ports/data volume
brain start work
brain config                # now prints both "home" and "work"
```

Then connect it — paste the new block into `~/.claude/settings.json` as above for personal use,
or see [Connect a specific git project](docs/connect-a-project.md) to check the MCP config into
the repo itself so it's automatic for anyone who clones it.

**More setups:**
- [Connect a specific git project](docs/connect-a-project.md) — checked-in `.mcp.json` instead of personal settings
- [Access over Tailscale](docs/tailscale.md) — reach your instance from other devices
- [Access over your LAN](docs/lan.md) — same, without Tailscale
- [Connect Claude Desktop or the mobile/web app](docs/desktop-mobile.md) — these don't connect to an MCP URL directly like Claude Code does
- [Backup and restore](docs/backup-restore.md) — snapshot an instance's data volume and restore it later

## `brain` CLI

`brain` owns Docker image/composition only: it drives the `docker` CLI directly (no
`docker compose` dependency) and stores instance config at
`~/.config/brain/instances.yaml` (override with `BRAIN_CONFIG_DIR`). Server images are pulled
from `ghcr.io/bradygerndt/project-brain`.

| Command | Description |
|---|---|
| `brain start [name]` | Start instance(s) — all if no name given |
| `brain stop [name]` | Stop instance(s) |
| `brain restart [name]` | Restart instance(s) |
| `brain ps` | List all instances and health status |
| `brain logs [name] [-f]` | Show logs (follow with `-f`) |
| `brain add <name> <mcp-port> <artifacts-port>` | Register a new instance (`--tag`/`--image` to pick a server version, `--host` to register a [remote](#remote-instances) one instead) |
| `brain remove <name>` | Remove an instance (data volume preserved) |
| `brain update [name]` | Pull the latest (or `--tag`/`--image`-selected) server image and recreate instance(s) |
| `brain backup <name> [outfile]` | Tar an instance's data volume to `outfile` (default: `./brain-<name>-<time>.tar.gz`) — see [backup and restore](docs/backup-restore.md) |
| `brain restore <name> <file>` | Restore a backup into an instance's volume (`--force` to overwrite existing data) |
| `brain health [name]` | Hit the health endpoint(s) directly |
| `brain open [name]` | Open the web UI in your browser |
| `brain config` | Print MCP config for `~/.claude/settings.json` |
| `brain mcp-bridge <name>` | Low-level stdio↔HTTP bridge to an instance's `/mcp` — see [below](#claude-desktop) |
| `brain version` | Show CLI version and its default server image tag |
| `brain help` | Show all commands |

### Running multiple instances

```bash
brain add work 3589 3590   # new instance "work" on its own ports/data volume
brain start work
brain config                # includes both "home" and "work" now
```

Each instance gets its own Docker volume, so memories and artifacts are isolated per instance.
Embedding-model weights are cached in one volume shared across all instances.

### Server versions

A fresh `brain` install defaults new instances to the image matching its own release (e.g.
CLI `v1.2.0` → `ghcr.io/bradygerndt/project-brain:1.2.0`), so a clean install always gets a
version it was actually tested against. Override per instance:

```bash
brain add work 3589 3590 --tag edge     # track the latest main-branch build
brain update work --tag 1.3.0           # pin to a specific released version
```

`edge` tracks `main`. Tagged releases publish `latest` plus the standard semver tiers —
`1.3.0`, `1.3`, and `1` — all bare (no `v` prefix, the OCI/Docker convention), even though the
CLI's own version and its git/release tags keep the `v` (the Go convention, e.g. `v1.3.0`).

### Remote instances

Every command above assumes `brain` manages the instance's container on this machine via
Docker. `--host` registers the opposite: a pointer to an instance that's managed *elsewhere* —
e.g. a laptop with `brain` installed only to reach a home server over Tailscale, never running
Docker itself.

```bash
brain add work-remote 3589 3590 --host 100.x.y.z
```

This skips the image pull and local port checks (nothing is bound on this machine) and records
the host instead. `brain ps`/`health` still check it over plain HTTP and label it `(remote)`;
`start`/`stop`/`restart`/`logs`/`update` all refuse on it with a clear error, since there's no
local container for them to act on. See [Access over Tailscale](docs/tailscale.md) for the full
setup this is meant to pair with.

### Claude Desktop

Unlike Claude Code, Claude Desktop can't connect to a bare MCP URL — its config only knows how
to launch a local stdio subprocess. `brain mcp-bridge <name>` is that subprocess: it resolves
`<name>` in `instances.yaml`, then translates Desktop's stdio JSON-RPC framing to/from HTTP calls
against the instance's `/mcp` endpoint (including session-header bookkeeping). It's a native
replacement for the third-party `mcp-remote`/`npx` bridge other MCP servers typically need,
so connecting Desktop stays dependency-free. Most users shouldn't invoke `mcp-bridge` by hand —
run `brain connect desktop [name]` instead, which writes Desktop's config to launch it
automatically. See [docs/desktop-mobile.md](docs/desktop-mobile.md) for details.

### Backup and restore

```bash
brain backup home                          # writes ./brain-home-<timestamp>.tar.gz
brain restore home ./brain-home-....tar.gz # restore into an already-registered instance
```

Tars an instance's whole data volume — `memory.sqlite`, its LanceDB vectors, artifact blobs —
runs live by default, and refuses to restore over a running instance or a non-empty volume
without `--force`. See [Backup and restore](docs/backup-restore.md) for details.

## MCP tools

Exposed to any MCP client (e.g. Claude Code) connected to `/mcp`. See
[docs/memory-flow.md](docs/memory-flow.md) for how the memory tools actually read and write
under the hood (SQLite vs. LanceDB, sync vs. async indexing).

**Memory**
- `memory_set` — store or update an entry (indexed for keyword + semantic search)
- `memory_get` — fetch by exact key
- `memory_search` — full-text keyword search
- `memory_search_semantic` — vector similarity search
- `memory_list` — list with optional namespace/tag filters
- `memory_delete` — remove by key
- `memory_find_clusters` — read-only: group near-duplicate/related memories by embedding similarity
- `memory_archive` — soft-delete an entry (still fetchable by exact key, hidden from search/list)

**Artifacts**
- `artifact_write` — store a file, returns id + URL
- `artifact_read` — read stored content
- `artifact_list` — list artifacts
- `artifact_url` — get an artifact's URL without reading its content

**Agent coordination**
- `agent_ping` / `agent_list` — presence announcements for active agents
- `lock_acquire` / `lock_release` — TTL-based resource locks

**Server**
- `ui_url` — get this instance's web UI URL

## HTTP API

Besides `/mcp` (the MCP transport), each instance exposes:

- `GET /health` — liveness + active session count
- `GET /ui` — web UI for browsing memory and artifacts
- `GET /api/memory?q=&type=keyword&ns=&limit=` — keyword search
- `GET /api/memory/semantic?q=&ns=&limit=` — semantic search
- `GET /api/memory/list?ns=&tag=&limit=&offset=` — list memories
- `POST /api/memory` — create/update a memory entry
- `GET /api/artifacts?tag=&limit=&offset=` — list artifacts
- `GET /api/agents` — list active agents
- `GET /api/locks` — list active resource locks

The artifacts server (separate port) serves stored files at `/artifacts/<id>/<filename>`.

## Configuration

`brain` sets these automatically inside each container it creates:

| Variable | Purpose |
|---|---|
| `BRAIN_NAME` | Instance name, set automatically by `brain` |
| `MCP_PORT` | Port for the `/mcp` endpoint and web UI, set automatically by `brain` |
| `ARTIFACTS_PORT` | Port for serving stored artifacts, set automatically by `brain` |
| `ARTIFACTS_HOST` | Host used when building artifact URLs — auto-detected from the host's LAN interfaces by `brain` on every `start`/`update` unless set explicitly via `--artifacts-host` (needed for [Tailscale access](docs/tailscale.md); [LAN access](docs/lan.md) usually needs nothing) |

## Development

**Server** (`src/`, TypeScript, runs natively via Node's built-in type-stripping — no build
step):

```bash
npm install
npm run dev         # node --watch src/server.ts
npm run typecheck    # tsc --noEmit
```

To run the server in Docker from local source instead of the published image:

```bash
docker compose -f docker-compose.dev.yml up --build
```

**CLI** (`cli/`, Go):

```bash
cd cli
go build ./...
go run . help
```

CI publishes the server image to GHCR on every push to `main` (tag `edge`) and on version
tags (bare semver + `latest`), and cross-compiles `brain` releases via GoReleaser on version
tags — see `.github/workflows/`.
