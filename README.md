# project-brain

A self-hosted MCP (Model Context Protocol) memory service for Claude Code. It gives agents a
persistent, searchable memory (keyword + semantic), a place to store file artifacts, and simple
presence/locking primitives for coordinating multiple agents working on the same project.

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
- `curl` and `tar` (to install the CLI — both are preinstalled on virtually every system)
- An `ANTHROPIC_API_KEY` (only required for the `memory_extract` tool)

The `brain` CLI is a standalone compiled binary — no Node.js, npm, or Go toolchain needed to
install or run it. (Node is only used to build the server image in CI; contributors editing
`src/` need it too — see [Development](#development).)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bradygerndt/project-brain/main/install.sh | bash
```

This detects your OS/architecture, downloads the matching `brain` binary from the
[latest release](https://github.com/bradygerndt/project-brain/releases/latest) into
`~/.local/bin` (override with `BRAIN_BIN_DIR`), and adds that directory to your `PATH` if
needed. No repo checkout, no `git clone`, no package manager.

Re-running the same command later upgrades the CLI to the newest release.

After installing, add your Anthropic API key (only needed for `memory_extract`):

```bash
$EDITOR ~/.config/brain/.env   # set ANTHROPIC_API_KEY=sk-ant-...
```

(Override that location with `BRAIN_CONFIG_DIR`.)

## Quick start

```bash
brain start        # start all instances (requires Docker)
brain ps           # check status
brain config       # print MCP config to paste into ~/.claude/settings.json
brain open         # open the web UI in your browser
```

Add the printed config to `~/.claude/settings.json` under `"mcpServers"`, then restart Claude
Code. A `home` instance is seeded automatically on first run, listening on MCP port `3579` and
artifacts port `3580` — no config file to hand-edit.

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
CLI `v1.2.0` → `ghcr.io/bradygerndt/project-brain:v1.2.0`), so a clean install always gets a
version it was actually tested against. Override per instance:

```bash
brain add work 3589 3590 --tag edge     # track the latest main-branch build
brain update work --tag v1.3.0          # pin to a specific released version
```

`edge` tracks `main`; `latest` and `vX.Y.Z` are only ever published on tagged releases.

## MCP tools

Exposed to any MCP client (e.g. Claude Code) connected to `/mcp`:

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
| `ARTIFACTS_HOST` | Override the host used when building artifact URLs |

## Development

See [WORKTREES.md](WORKTREES.md) for the branch/worktree convention used for feature work.

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
tags (`vX.Y.Z` + `latest`), and cross-compiles `brain` releases via GoReleaser on version tags
— see `.github/workflows/`.
