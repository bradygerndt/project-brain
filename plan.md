# Plan: native `brain mcp-bridge` + `brain connect desktop`

## Goal

Replace `mcp-remote` — the third-party bridge `docs/desktop-mobile.md` currently tells users to
run via `npx` to connect Claude Desktop locally — with a bridge built into the `brain` binary
itself, and automate writing Desktop's config so nobody hand-edits JSON.

## Background

- Claude Desktop's `claude_desktop_config.json` only knows how to launch a local stdio command;
  it doesn't accept a bare MCP URL like Claude Code's `settings.json` does. Today's workaround is
  `"command": "npx", "args": ["mcp-remote", "<url>"]`.
- `mcp-remote` (geelen, ~1.5k stars) is community-maintained and its own README says it
  "should be considered experimental." It also requires Node.js/`npx`, which `brain` otherwise
  goes out of its way not to need (README: "no Node.js, npm, or Go toolchain needed to install
  or run it").
- `brain` already ships as a dependency-free, prebuilt cross-platform binary (macOS/Windows/
  Linux, amd64/arm64, via GoReleaser). We only need to bridge to *our own* server's Streamable
  HTTP transport — not arbitrary remote servers with OAuth — so the scope is much narrower than
  what `mcp-remote` solves in general. Building it ourselves removes both the Node/npx dependency
  and the third-party trust question in one move.

## Design

### 0. Prerequisite: distinguish Docker-managed vs. remote-reference instances

**Why:** today every `instances.yaml` entry is assumed to be something `brain` manages via Docker
on this host — `start`/`stop`/`restart`/`logs`/`update` all shell out to `docker` using
`state.ContainerName(name)`. There's no way to register "I just want to remember this instance's
URL" — e.g. a laptop with `brain` installed purely to run `connect desktop`/`config` against a
home server reached over Tailscale, never running Docker itself. This is a real prerequisite, not
a nice-to-have: `connect desktop [name]`'s "no name = all instances" behavior (§2) isn't sound
until remote entries can exist and be told apart from managed ones.

**Schema change:** add `Host string` to `Instance` (`cli/internal/state/state.go`) — dual
purpose:
- Empty (default): Docker-managed on this host; every URL uses `127.0.0.1`, exactly today's
  behavior, nothing changes for existing instances.
- Set: reference-only. Every URL uses this host instead; `brain` never shells out to `docker`
  for it.

**Guardrail — the actual concern to design for, not just an implementation detail:** if
`update`/`start`/`restart` treated a `Host`-set entry the same as a local one, they could
`docker run` a brand-new container bound to whatever ports happen to be registered — silently
standing up a real local brain under the same name the user only meant as a pointer elsewhere.
`update` is the sharpest edge here, since its entire job is "pull the latest image and recreate
instance(s)." To make this structurally hard to get wrong rather than something each command has
to remember to check:

- One shared guard — e.g. `requireManaged(name string, inst *state.Instance) error` in `state`
  — called at the very top of every code path that shells out to `docker` for a *specific*
  instance (`cmdStart`, `cmdStop`, `cmdRestart`, `cmdLogs`, `cmdUpdate`), before any `docker`
  command runs. Not scattered ad-hoc `if inst.Host != ""` checks re-derived in each command.
- When iterating "all instances" (no name given), skip `Host`-set entries but print an explicit
  line per skip (`ui.Info("skipping %s (remote, not managed here)", name)`) — silence in an
  "update everything" command reads as "nothing happened" or looks like a bug, when what actually
  happened is "correctly did nothing."
- When a `Host`-set instance is named *explicitly* (`brain start work-remote`), refuse with a
  specific error rather than a raw `docker` failure: `instance "work-remote" is remote (host:
  100.x.y.z) — not managed by this brain, nothing to start`.
- `cmdAdd` already refuses to re-add an existing name (`instance "%s" already exists`,
  `main.go:376`) — so there's no existing path where a follow-up `brain add` without `--host`
  could silently flip an existing remote entry into a local one, or vice versa. That guard stays
  as-is; nothing to add there.
- `brain add`'s own success output should look different for a remote entry — drop the
  `"Start it with: brain start <name>"` line (misleading; there's nothing to start), and confirm
  explicitly what kind of entry was just registered instead:
  `ui.Ok('Registered remote instance "%s" -> %s:%d (not managed by this brain)', name, host,
  mcpPort)`. Making the local/remote distinction maximally visible right at `add` time is the
  best defense against a "forgot `--host`" mistake, since the user sees immediate confirmation of
  what they just created rather than finding out later.
- `brain ps` labels remote entries distinctly (e.g. a `(remote)` tag next to the name) instead of
  showing "offline" the way a genuinely-down local instance would — `health.Fetch` already works
  over plain HTTP, so reachability still gets checked, just without any Docker-managed
  implication.

**`brain add`'s CLI surface:** new `--host <host>` flag, mutually exclusive with `--tag`/`--image`
(those only make sense for a locally-pulled image). When present: skip `docker pull`, image
resolution, and the port-conflict check against locally-bound ports (nothing is bound locally);
write `{ MCPPort, ArtifactsPort, Host }`, leaving `Image`/`ArtifactsHost` unused.

**Already host-agnostic, small change needed:** `cmdPs`/`cmdHealth`/`cmdConfig` (and the new
`mcp-bridge`/`connect desktop`) already just do plain HTTP against a host:port, no Docker
involved — they only need their hardcoded `127.0.0.1` swapped for `inst.Host` when set.
`cli/internal/health/health.go`'s `Fetch` needs a `host` parameter added (currently hardcodes
`127.0.0.1` itself).

### 1. `brain mcp-bridge <name>` — the stdio↔HTTP bridge

New subcommand in `cli/main.go`'s dispatch (alongside `start`/`stop`/`config`/etc.), with the
actual proxy logic in a new `cli/internal/bridge` package to keep `main.go` thin, matching the
existing pattern (`cli/internal/state`, `cli/internal/docker`, ...).

Behavior:
- Resolve `<name>` via `state.Load()` — the same registry `cmdConfig`/`cmdOpen` already read — to
  get its MCP URL (`http://<host>:<mcpPort>/mcp`, where `<host>` is `inst.Host` if set from §0,
  else `127.0.0.1`). This is exactly why the bridge naturally supports remote instances too —
  it's just an HTTP client, locality is someone else's problem.
- Read newline-delimited JSON-RPC messages from stdin (Desktop's stdio transport framing).
- For each message, `POST` it to the instance's `/mcp` with `Content-Type: application/json` and
  `Accept: application/json, text/event-stream`.
  - Capture the `Mcp-Session-Id` response header from the first response; send it as a request
    header on every subsequent POST.
  - Write the response body back to stdout in the same framing.
- On stdin EOF or SIGINT/SIGTERM, send `DELETE /mcp` (with the session id) to close cleanly —
  matches the server's existing `app.delete('/mcp', ...)` handler — then exit 0.
- **v1 scope:** synchronous request/response only, which covers every tool call today. Not in
  v1: consuming the `GET /mcp` SSE stream for server-pushed notifications (logging, etc.) —
  nothing in project-brain sends those yet, so skip until something actually needs it.

### 2. `brain connect desktop [name]` — automate the config edit

New subcommand. Resolves Claude Desktop's config path:
- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`
- **Linux/WSL:** no Desktop app exists here — print a clear message and exit non-zero rather than
  write a file nobody will read (same "don't guess" precedent as the WSL LAN auto-detect in
  `docs/lan.md`).

**Multiple instances:** each entry is keyed `project-brain-<name>`, matching the convention
`cmdConfig` already uses for Claude Code (`main.go:503`, `"project-brain-%s"`) — not a fixed
`project-brain` key, which would make a second `connect desktop` call silently overwrite the
first instead of both coexisting. With `name` omitted, mirror `cmdConfig`'s existing behavior
(it ignores its args and always emits every registered instance) and merge one entry per instance
in `instances.yaml` in a single run; with `name` given, scope to just that one. The bridge process
itself needs no multi-instance awareness — Desktop spawns one `brain mcp-bridge <name>` subprocess
per config entry, so multiplicity is purely a config-generation concern here, not something
`mcp-bridge` has to handle.

Known gap, accepted rather than solved in v1: if an instance is later removed (`brain remove
work`), nothing prunes its now-stale `project-brain-work` entry — it'll just fail quietly next
time Desktop tries to launch it. `brain config`'s Claude Code output has the same characteristic
(nothing prunes `settings.json` either), so this is consistent with existing behavior, not a
regression, but worth calling out.

Behavior:
- Read the existing file if present (missing/empty is fine — start from `{}`).
- Parse generically (preserve unknown keys and other `mcpServers` entries — don't clobber
  anything not ours). `desktop-mobile.md`'s research turned up a real GitHub issue about Desktop
  corrupting this file on bad entries, so a tool that writes it should be conservative.
- Back up the existing file (e.g. `claude_desktop_config.json.bak`) before writing.
- Merge in, one block per target instance:
  ```json
  "mcpServers": {
    "project-brain-home": {
      "command": "<absolute path to this brain binary, via os.Executable()>",
      "args": ["mcp-bridge", "home"]
    }
  }
  ```
  (`os.Executable()` matters because Desktop won't share the user's shell `PATH`.)
- Write the file back, print next steps (restart Desktop).

### 3. Docs

- Rewrite `docs/desktop-mobile.md`'s "Claude Desktop, local-only" section to lead with
  `brain connect desktop <name>` as the one-command path. Drop the manual JSON-editing
  instructions and the `mcp-remote` dependency/experimental caveat for this path entirely.
- Leave the Custom Connectors section as-is — unrelated to this change, still the only option
  for mobile.

## Related feature: instance backup / restore

Not a prerequisite for the Desktop-connection work above — a separate, standalone feature, but
worth planning here since §0's `requireManaged` guard is directly reusable for it.

### What's being backed up

Everything an instance owns lives in one Docker volume, `brain-<name>-data`
(`state.DataVolume(name)`): `memory.sqlite` (+ its WAL files), the `data/vectors/` LanceDB
dataset, and `data/artifacts/<id>/<filename>` blobs. The shared `brain-hf-cache` volume
(embedding model weights, `state.CacheVolume`) is *not* instance data — just a re-downloadable
cache — and is excluded.

### `brain backup <name> [outfile]`

Whole-volume tar rather than anything that understands SQLite/LanceDB internals — future-proof
against schema changes, and produces one portable file:

```
docker run --rm -v brain-<name>-data:/data:ro -v <outdir>:/backup alpine \
  tar czf /backup/brain-<name>-<timestamp>.tar.gz -C /data .
```

Default output path is the current directory (so the user visibly owns the file and can move it
off-site), overridable with the optional `outfile` argument.

**Consistency while running:** worth naming, not worth over-engineering around. `memory_set`
already writes SQLite and LanceDB via a fire-and-forget `setImmediate` (`src/memory.ts`), so the
two stores are already only eventually-consistent with each other during normal operation. A live
volume tar is no worse than that existing steady state — it doesn't need the instance stopped to
be "safe enough," though stopping first guarantees no mid-write torn file for anyone who wants
belt-and-suspenders.

### `brain restore <name> <backup-file>`

Scoped narrowly:
- Assumes `<name>` is already registered via a normal `brain add` — restore only repopulates the
  volume, it doesn't recreate the registry entry (ports/image go through the existing add flow,
  since they're host-specific and could conflict on a different target machine anyway).
- Refuses if the instance is currently running (data would be in use).
- Refuses onto an existing non-empty volume unless `--force` — not a silent-clobber trap.

### Interaction with §0 (remote-reference instances)

A `Host`-set instance has no local volume at all, so `backup`/`restore` go through the same
`requireManaged` guard as `start`/`stop`/`restart`/`update` — another real consumer of that
check, reinforcing that it belongs as one shared function rather than per-command logic.

### Open questions

1. Naming — `backup`/`restore`, or something else (`export`/`import`, `snapshot`)?
2. Default output location — current directory, or a dedicated `~/.config/brain/backups/`?
3. Should `backup` require the instance to be stopped by default (safest) with a `--live` opt-in,
   or default to live given the eventual-consistency argument above?

### Testing plan

- Backup a running instance, `memory_set` a few facts, `restore` into a fresh instance of the
  same name, confirm the pre-backup facts round-trip via `memory_get`/`memory_search`.
- `brain backup`/`restore` on a `Host`-set remote instance refuses with the same "not managed by
  this brain" error as `start`/`update`.
- `restore` refuses onto a non-empty existing volume without `--force`, and refuses while the
  target instance is running.

## Open questions to confirm before implementing

1. Subcommand names — `mcp-bridge` / `connect desktop` good enough, or prefer something else?
2. Should `brain connect desktop` require the instance to be currently running, or just
   registered in `instances.yaml`? Leaning: just registered — the bridge fails with a clear error
   at Desktop-launch time if it's not up, same as any other URL misconfiguration would.
3. Backup handling — overwrite the single `.bak` each time, or keep timestamped backups?
   Leaning: overwrite; this isn't meant to be a version history.
4. `--host` flag name for §0 — `--host` reads a little generic next to `--artifacts-host`.
   Alternatives: `--remote <host>`, or reuse `--artifacts-host`'s naming pattern as `--mcp-host`
   (but that undersells that it also blocks all Docker management, not just artifact URLs).
   Leaning: `--host`, since it's the one flag that also *is* the "this is remote" marker — a
   separate name would invite a confusing state where one is set without the other.

## Testing plan

- `brain add work-remote 3589 3590 --host 100.x.y.z`, then confirm `brain start`/`restart`/
  `update`/`logs work-remote` all refuse with the specific "remote, not managed" error — and that
  `brain start`/`update` with no name print an explicit skip line for it and don't touch Docker.
- Confirm `brain ps` shows `work-remote` with a `(remote)` label and a real (HTTP-based) health
  check against the given host, not "offline."
- Unit test the JSON-RPC framing / session-header logic in `cli/internal/bridge` against a fake
  HTTP server.
- Manual test: `brain connect desktop home`, restart Desktop, confirm the tool list populates and
  a `memory_set`/`memory_get` round-trip works.
- Manual test with two instances registered (`brain add work ...`): `brain connect desktop home`
  then `brain connect desktop work`, confirm both `project-brain-home` and `project-brain-work`
  entries coexist in the config afterward, and confirm running `brain connect desktop` with no
  name in one shot produces the same two entries.
- Confirm `brain connect desktop` on Linux prints the no-op message and exits non-zero without
  writing a file.
