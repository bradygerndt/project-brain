# Access project-brain over Tailscale

Run project-brain on one machine (a home server, a NAS, your desktop) and reach it from your
laptop, another machine, or your phone — without exposing it to the public internet. The MCP
server has no built-in auth, so this relies on your tailnet already being private and
authenticated between your own devices.

## 1. Install Tailscale on the host

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

(`install.sh` already checks for this and prints the same commands if it's missing.)

## 2. Find the host's Tailscale address

```bash
tailscale ip -4          # e.g. 100.101.102.103
# or use the MagicDNS name shown by:
tailscale status
```

Either the IP or the MagicDNS hostname (e.g. `my-host.tailnet-name.ts.net`) works below.

## 3. Point artifact URLs at that address

Docker already publishes the instance's ports on all interfaces, so the MCP endpoint itself is
reachable over Tailscale with no extra config. Artifact URLs are the one thing that needs
telling explicitly: `brain` auto-detects a LAN address for these by default (see
[lan.md](lan.md)), which is right for devices on the same local network but not for a device
reaching you only over Tailscale from elsewhere — tell it to use the tailnet address instead:

```bash
# the auto-seeded "home" instance (already exists after your first `brain start`):
brain update home --artifacts-host 100.101.102.103

# a new instance instead:
brain add remote 3589 3590 --artifacts-host 100.101.102.103
```

Use the MagicDNS name here too if you'd rather not hardcode an IP.

## 4. Connect from another device

On any machine on your tailnet, point its MCP config at the Tailscale address instead of
`127.0.0.1`:

```json
{
  "mcpServers": {
    "project-brain": {
      "type": "http",
      "url": "http://100.101.102.103:3579/mcp"
    }
  }
}
```

See [connect-a-project.md](connect-a-project.md) for where that block goes (project-scoped
`.mcp.json` vs. personal settings).
