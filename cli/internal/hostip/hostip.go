// Package hostip guesses a LAN-reachable address for this host. The CLI
// runs on the host itself (unlike the server, sandboxed inside its
// container), so it can see the host's real interfaces — used as the
// default ARTIFACTS_HOST so artifact URLs aren't stuck pointing at the
// container's unreachable internal Docker IP.
package hostip

import (
	"net"
	"os"
	"strings"
)

// IsWSL reports whether this process is running under Windows Subsystem
// for Linux. WSL2 sits behind Windows' own NAT by default — its "eth0"
// gets an internal address (commonly 172.x.x.x) that only exists inside
// that one Windows machine's virtual network. It looks exactly like a
// normal LAN IP to Detect(), but no other device on the LAN can actually
// reach it; only Windows itself can, via WSL2's automatic localhost
// forwarding. Callers should not trust Detect()'s result here.
func IsWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	s := strings.ToLower(string(data))
	return strings.Contains(s, "microsoft") || strings.Contains(s, "wsl")
}

// virtualPrefixes are host-side interface names created by Docker itself
// (bridges, veth pairs) or other container/VM runtimes — never what we
// want to hand out as "the address to reach this host at."
var virtualPrefixes = []string{"docker", "br-", "veth", "virbr", "podman"}

func isVirtual(name string) bool {
	name = strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Detect returns a best-guess LAN IPv4 address for this host, preferring
// 192.168.x.x/10.x.x.x ranges over 172.16-31.x.x (which Docker's own
// default bridge networks commandeer, so a 172.x hit is weaker evidence
// of a real LAN). Falls back to 127.0.0.1 — always correct for reaching
// this same host, just not from anywhere else — if nothing better turns
// up.
func Detect() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	var preferred, fallback []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtual(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			if strings.HasPrefix(ip4.String(), "172.") {
				fallback = append(fallback, ip4.String())
			} else {
				preferred = append(preferred, ip4.String())
			}
		}
	}

	if len(preferred) > 0 {
		return preferred[0]
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return "127.0.0.1"
}
