# Access project-brain over your LAN

Same idea as [Tailscale access](tailscale.md), just over your local network instead — useful
if you don't want to install Tailscale and everything's already on the same Wi-Fi/router.

Docker publishes the instance's ports on all interfaces by default, so any other device on
your LAN can already reach it once you know the host's local network address — check your
machine's network settings for its LAN IP (usually something like `192.168.x.x` or
`10.x.x.x`); how exactly you find it depends on your OS but every OS exposes it somewhere in
its network/Wi-Fi settings.

Once you have it:

```bash
brain add home 3579 3580 --artifacts-host 192.168.1.42     # or: brain update home --artifacts-host ...
```

(without this, artifact URLs point at the container's internal Docker IP, not your LAN — see
[tailscale.md](tailscale.md) for why). Then point another device's MCP config at
`http://192.168.1.42:3579/mcp` instead of `127.0.0.1`, same as the Tailscale doc's step 4.

If it doesn't connect, the host's firewall is the usual culprit — it may need an inbound
allow rule for the MCP/artifacts ports, however your OS manages that.
