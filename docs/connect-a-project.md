# Connect a git project to project-brain

This wires a project-brain instance into a specific repo, checked into git, so anyone who
clones it and opens it in Claude Code gets the same MCP server automatically — as opposed to a
personal `~/.claude/settings.json` entry that only exists on your own machine.

## 1. Make sure the instance is running

```bash
brain start          # or `brain start <name>` for a specific instance
brain ps              # confirm it's "running" and note its MCP port
```

## 2. Add it to the project

From the repo root:

```bash
claude mcp add --scope project --transport http project-brain http://127.0.0.1:3579/mcp
```

(Swap the port for whatever `brain ps`/`brain config` printed if you're not using the default
`home` instance.) `--scope project` writes to a `.mcp.json` file at the repo root instead of
your personal config — that's the file you commit.

Prefer to edit it by hand? `brain config` prints the exact `mcpServers` block to paste into
`.mcp.json`:

```bash
brain config
```

```json
{
  "mcpServers": {
    "project-brain": {
      "type": "http",
      "url": "http://127.0.0.1:3579/mcp"
    }
  }
}
```

## 3. Commit it

```bash
git add .mcp.json
git commit -m "Connect project-brain MCP server"
```

Anyone who pulls this and opens the repo in Claude Code gets prompted to approve the
project-scoped server the first time (a trust gate so a cloned repo can't silently launch
processes on your machine). After approving, run `/mcp` inside Claude Code to confirm it
connected.

## Gotchas

- **Restart required.** Claude Code reads `.mcp.json` at session start — changes to it need a
  fresh session to take effect.
- **`127.0.0.1` only works for you.** If everyone on the team runs their own local instance,
  that's fine as committed. If you want the whole team hitting one shared instance instead,
  use a reachable address (Tailscale hostname or LAN IP) instead of `127.0.0.1` — see
  [tailscale.md](tailscale.md) or [lan.md](lan.md).
