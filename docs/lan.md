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

Native Windows `brain` sees the host's real network interfaces, so LAN access here works
exactly as described above — no different from bare Linux or macOS. Running `brain` *inside*
WSL is where this gets tricky: WSL2's own network address sits behind Windows' NAT and isn't
reachable from other devices on the LAN at all, even though it looks like a normal private IP
from inside WSL. Auto-detection deliberately doesn't try to guess here — `brain` falls back to
`127.0.0.1` instead of confidently handing out a wrong address — find your Windows host's real
LAN IP (`ipconfig` in Windows) and set it explicitly (see below). If you're on Windows mainly
to use Docker Desktop, installing `brain` natively instead (see the
[Windows install section](../README.md#install)) sidesteps this entirely.

**Mirrored networking mode changes this.** WSL2's newer "mirrored" networking mode (opt-in —
add `networkingMode=mirrored` under `[wsl2]` in `.wslconfig`; needs a recent Windows 11 build)
drops the NAT layer: WSL shares the host's network interfaces directly instead of getting its
own virtual one, so a WSL2-hosted `brain` sees the same LAN-reachable addresses Windows itself
does, and auto-detection picks a real, working IP with no override needed. Run `hostname -I`
inside WSL and compare against `ipconfig` on the Windows side — if the addresses already match,
you're in mirrored mode and can skip the override below.

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
