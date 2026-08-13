# Connect project-brain to Claude Desktop or the Claude mobile/web app

project-brain is built primarily for Claude Code and other clients that connect to an MCP
server's URL directly — the same way `curl` would. Claude Desktop and the Claude mobile/web apps
(claude.ai) don't work that way, so this is a secondary path, not the primary one these docs are
otherwise written for.

## Custom Connectors (Desktop, web, and mobile)

This is the officially supported way to reach a remote MCP server from any of the Claude apps,
including mobile — Settings → Connectors → Add custom connector (or the mobile equivalent).

The catch: a Custom Connector is fetched by Anthropic's cloud infrastructure, not by your device.
Quoting Anthropic's docs, your server "must be reachable over the public internet from
Anthropic's IP ranges" — `127.0.0.1`, your LAN, and a plain Tailscale address (without
[Funnel](https://tailscale.com/kb/1223/funnel)) all won't work here, unlike every other setup in
these docs.

**Think carefully before doing this.** project-brain has no built-in authentication (the same
caveat [tailscale.md](tailscale.md) makes about your tailnet) — it was designed to be reached
only by devices *you already trust to reach it directly* over Tailscale/LAN/localhost. Making it
reachable from the public internet so a Custom Connector can find it means anyone who obtains
that URL can read and write your memory too, not just Anthropic's infrastructure. If you still
want this — the mobile app has no other option — put a real auth layer in front of it first: a
reverse proxy with an API key, basic auth, or OAuth, rather than exposing `brain`'s bare `/mcp`
endpoint.

## Claude Desktop, local-only

If you only need Desktop — not mobile — and want to avoid exposing anything publicly, one command
wires it up:

```bash
brain connect desktop          # every registered instance
brain connect desktop home     # or just one, by name
```

This writes Claude Desktop's config file for you, using a stdio↔HTTP bridge built into the
`brain` binary itself (`brain mcp-bridge <name>`) — no Node.js/`npx`, no third-party dependency.
Desktop's own config only starts servers via a local command, so this runs locally and forwards
to your instance over the same Streamable HTTP transport Claude Code uses; nothing needs to be
internet-reachable.

It's conservative about the file it's editing — a real GitHub issue documents Desktop corrupting
this file when something writes it a value it doesn't expect, so `brain` parses generically,
leaves unrelated top-level keys and any other `mcpServers` entries untouched, and backs up the
previous version to `claude_desktop_config.json.bak` before writing. Each instance gets its own
entry keyed `project-brain-<name>` (not a single fixed key), so running the command again for a
second instance adds alongside the first rather than overwriting it:

```json
{
  "mcpServers": {
    "project-brain-home": {
      "command": "/path/to/brain",
      "args": ["mcp-bridge", "home"]
    },
    "project-brain-work": {
      "command": "/path/to/brain",
      "args": ["mcp-bridge", "work"]
    }
  }
}
```

**Restart Claude Desktop completely** (quit, not just close the window) for it to pick up the
change. The tools show up under the connector/hammer icon in the message box once it connects.

Available on macOS and Windows — wherever Claude Desktop itself runs. There's no Linux build of
Desktop (WSL included), so `brain connect desktop` refuses with a clear error there instead of
writing a config file nothing will ever read. The instance just needs to be registered in
`instances.yaml`, not currently running — `brain mcp-bridge` fails with a clear error at
Desktop-launch time if it's down, the same as any other URL misconfiguration would. If Desktop
runs on a different machine than the instance itself (e.g. a laptop reaching a home server), see
[Tailscale](tailscale.md) or [LAN](lan.md) for registering it with `brain add ... --host`; you
still run `brain connect desktop` on the machine Desktop is actually installed on.

One gap worth knowing: removing an instance later (`brain remove work`) doesn't prune its
`project-brain-work` entry from Desktop's config — it'll just fail quietly next time Desktop
tries to launch it. `brain config`'s Claude Code output has the same characteristic today, so
this isn't a regression, just something to clean up by hand if it bothers you.
