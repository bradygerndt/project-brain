# Access project-brain over your LAN

Same idea as [Tailscale access](tailscale.md), just over your local network instead — useful
if you don't want to install Tailscale and everything's already on the same Wi-Fi/router.

**This works automatically — no config needed.** `brain` runs on the host itself, so it can
see the host's real network interfaces (unlike the server, sandboxed inside its container),
and picks a LAN-reachable address for artifact URLs by default whenever it creates or
recreates a container. Docker already publishes the instance's ports on all interfaces, so
once you know the host's LAN IP, any other device on the network can reach it:

```bash
# from another device's MCP config, instead of 127.0.0.1:
http://192.168.1.42:3579/mcp
```

Check `brain ps` or `docker inspect brain-<name> --format '{{.Config.Env}}'` (look for
`ARTIFACTS_HOST`) to see what it auto-detected.

## Running inside WSL

If `brain` is running inside WSL (as opposed to natively on Windows — see the
[Windows install section](../README.md#install)), auto-detection deliberately doesn't try to
guess: WSL2's own network address sits behind Windows' NAT and isn't reachable from other
devices on the LAN at all, even though it looks like a normal private IP from inside WSL.
`brain` falls back to `127.0.0.1` there instead of confidently handing out a wrong address —
find your Windows host's real LAN IP (`ipconfig` in Windows) and set it explicitly (see below).

## If auto-detection picks the wrong address

Machines with multiple network interfaces (VPNs, virtual adapters, several NICs) can confuse
the heuristic. Override it explicitly:

```bash
brain update home --artifacts-host 192.168.1.42            # the auto-seeded "home" instance
brain add work 3589 3590 --artifacts-host 192.168.1.42      # a new instance instead
```

If it still doesn't connect from another device, the host's firewall is the usual next
culprit — it may need an inbound allow rule for the MCP/artifacts ports, however your OS
manages that.
