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

## Claude Desktop, local-only (optional, third-party)

If you only need Desktop — not mobile — and want to avoid exposing anything publicly, you can
bridge Desktop's config file to your instance with
[`mcp-remote`](https://github.com/geelen/mcp-remote), a small stdio↔HTTP proxy. Desktop's own
config only starts servers via a local command, so this runs locally and forwards to your
instance over Streamable HTTP — nothing needs to be internet-reachable, same trust model as
Claude Code.

Worth calling out before using it: `mcp-remote`'s own README describes it as *"a working
proof-of-concept... should be considered experimental"* — it's community-maintained (not
Anthropic), unaffiliated with this project, and the most established option available, but not a
polished or guaranteed-stable one. Needs [Node.js](https://nodejs.org/) (18+) installed for
`npx` — the `brain` CLI and server don't otherwise require it, this bridge is the exception.

1. **Open the config file.** In Claude Desktop: Settings → Developer → Edit Config. This creates
   the file if it doesn't exist yet:
   - **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
   - **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

2. **Add project-brain**, pointing at your instance's MCP URL (swap in your port, or a
   [Tailscale](tailscale.md)/[LAN](lan.md) address if Desktop isn't on the same machine as
   `brain`):

   ```json
   {
     "mcpServers": {
       "project-brain": {
         "command": "npx",
         "args": ["mcp-remote", "http://127.0.0.1:3579/mcp"]
       }
     }
   }
   ```

3. **Restart Claude Desktop completely** (quit, not just close the window) for it to pick up the
   change. The tools show up under the connector/hammer icon in the message box once it connects.
