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
- **Fact extraction** — `memory_extract` uses Claude Haiku to pull structured facts out of raw
  text (conversation logs, notes, docs) and store them automatically.
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
- An `ANTHROPIC_API_KEY` — **optional.** The server runs fully without one; it's only used by
  `memory_extract`, where *the server itself* (not your MCP client) makes its own separate
  call to Claude Haiku to pull structured facts out of raw text — handy for importing a
  meeting transcript or chat log, bulk-loading notes from a document, or letting a
  non-agentic script (a cron job, a webhook receiver) ingest raw text without an LLM already
  in the loop to decide what's worth keeping. Skip it and every other tool still works, you
  just enter facts one at a time via `memory_set` instead of extracting many at once.

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

Optional — add your Anthropic API key if you want `memory_extract` (skip this otherwise):

```bash
$EDITOR ~/.config/brain/.env   # set ANTHROPIC_API_KEY=sk-ant-...
```

(`%APPDATA%\brain\.env` on native Windows. Override the location with `BRAIN_CONFIG_DIR`.)

## Quick start

**The overall flow:** each project-brain *instance* is a self-contained Docker container with
its own memory (its own volume, its own MCP + artifacts ports). `brain start` boots the
`home` instance seeded automatically on first run (MCP port `3579`, artifacts port `3580`), and
`brain config` prints the block that tells Claude Code where to find it. From there you either
connect `home` once and reuse it as one shared memory across every project, or give a
particular project its own isolated instance — both are covered below.

```bash
brain start        # start all instances (requires Docker)
brain ps           # check status
brain config       # print MCP config to paste into ~/.claude/settings.json
brain open         # open the web UI in your browser
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
| `brain add <name> <mcp-port> <artifacts-port>` | Register a new instance (`--tag`/`--image` to pick a server version) |
| `brain remove <name>` | Remove an instance (data volume preserved) |
| `brain update [name]` | Pull the latest (or `--tag`/`--image`-selected) server image and recreate instance(s) |
| `brain health [name]` | Hit the health endpoint(s) directly |
| `brain open [name]` | Open the web UI in your browser |
| `brain config` | Print MCP config for `~/.claude/settings.json` |
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
- `memory_extract` — extract structured facts from raw text via Claude Haiku

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

The artifacts server (separate port) serves stored files at `/artifacts/<id>/<filename>`.

## Configuration

`~/.config/brain/.env` (created by the installer) or an already-exported shell env var
(which takes precedence) supplies `ANTHROPIC_API_KEY` — `brain` reads it and passes it into
each container it creates. Inside the container itself:

| Variable | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | Required for `memory_extract` |
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
