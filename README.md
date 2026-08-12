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
- **Multi-instance** — run several named brains (different ports/data volumes) from one
  `docker-compose.yml`, managed with the `brain` CLI.

## Requirements

- [Node.js](https://nodejs.org) 20+
- [Docker](https://docs.docker.com/get-docker/) (to run the service)
- `curl` and `tar` (to install — both are preinstalled on virtually every system)
- `git` (only if you're cloning for development, see [WORKTREES.md](WORKTREES.md))
- An `ANTHROPIC_API_KEY` (only required for the `memory_extract` tool)

## Install

No `git clone` needed — this downloads a source tarball (via `curl`/`tar`), so it works with
just those two tools installed:

```bash
curl -fsSL https://raw.githubusercontent.com/bradygerndt/project-brain/main/install.sh | bash
```

This fetches the repo into `~/project-brain` (override with `BRAIN_INSTALL_DIR`), installs npm
dependencies, and symlinks the `brain` CLI into `~/.local/bin` (override with `BRAIN_BIN_DIR`).
If that directory isn't already on your `PATH`, the installer offers to add it to your shell rc
file. Re-running the same command later updates the code in place — `.env`, `data/`, and
`node_modules/` are left untouched.

Already have the repo checked out (e.g. via `git clone` for development)? Just run the script in
place — it detects the local checkout and skips the download:

```bash
./install.sh
```

After installing, add your Anthropic API key (only needed for `memory_extract`):

```bash
$EDITOR ~/project-brain/.env   # set ANTHROPIC_API_KEY=sk-ant-...
```

## Quick start

```bash
brain start        # start all instances (requires Docker)
brain ps           # check status
brain config       # print MCP config to paste into ~/.claude/settings.json
brain open         # open the web UI in your browser
```

Add the printed config to `~/.claude/settings.json` under `"mcpServers"`, then restart Claude
Code. The `home` instance is defined out of the box in `docker-compose.yml`, listening on MCP
port `3579` and artifacts port `3580`.

## `brain` CLI

| Command | Description |
|---|---|
| `brain start [name]` | Start instance(s) — all if no name given |
| `brain stop [name]` | Stop instance(s) |
| `brain restart [name]` | Restart instance(s) |
| `brain ps` | List all instances and health status |
| `brain logs [name] [-f]` | Show logs (follow with `-f`) |
| `brain add <name> <mcp-port> <artifacts-port>` | Add a new instance to `docker-compose.yml` |
| `brain remove <name>` | Remove an instance (data volume preserved) |
| `brain health [name]` | Hit the health endpoint(s) directly |
| `brain open [name]` | Open the web UI in your browser |
| `brain config` | Print MCP config for `~/.claude/settings.json` |
| `brain help` | Show all commands |

### Running multiple instances

```bash
brain add work 3589 3590   # new instance "work" on its own ports/data volume
brain start work
brain config                # includes both "home" and "work" now
```

Each instance gets its own Docker volume, so memories and artifacts are isolated per instance.

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

Set in `.env` (copied from `.env.example` by the installer) or as environment variables:

| Variable | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | Required for `memory_extract` |
| `BRAIN_NAME` | Instance name (set per-service in `docker-compose.yml`) |
| `MCP_PORT` | Port for the `/mcp` endpoint and web UI |
| `ARTIFACTS_PORT` | Port for serving stored artifacts |
| `ARTIFACTS_HOST` | Override the host used when building artifact URLs |

## Development

See [WORKTREES.md](WORKTREES.md) for the branch/worktree convention used for feature work.

```bash
npm install
npm run dev     # node --watch src/server.js
```
