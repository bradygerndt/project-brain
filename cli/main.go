// brain is the CLI for managing project-brain instances. It owns Docker
// image/composition only — no native bindings, no Node/npm. Instance state
// lives at ~/.config/brain/instances.yaml; server images are pulled from
// ghcr.io/bradygerndt/project-brain rather than built locally.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/bradygerndt/project-brain/cli/internal/bridge"
	"github.com/bradygerndt/project-brain/cli/internal/config"
	"github.com/bradygerndt/project-brain/cli/internal/desktop"
	"github.com/bradygerndt/project-brain/cli/internal/docker"
	"github.com/bradygerndt/project-brain/cli/internal/health"
	"github.com/bradygerndt/project-brain/cli/internal/hostip"
	"github.com/bradygerndt/project-brain/cli/internal/prompt"
	"github.com/bradygerndt/project-brain/cli/internal/state"
	"github.com/bradygerndt/project-brain/cli/internal/ui"
)

// Set via -ldflags at release build time (see cli/.goreleaser.yaml). Dev
// builds fall back to "edge", the floating main-branch image tag.
var (
	version         = "dev"
	defaultImageTag = "edge"
)

const imageRepo = "ghcr.io/bradygerndt/project-brain"

// alpineImage is the throwaway container image `backup`/`restore` use to
// read/write an instance's data volume — small, ubiquitous, and already
// what the plan's own `docker run ... tar` recipe assumes.
const alpineImage = "alpine"

func imageRef(tag string) string {
	return imageRepo + ":" + tag
}

func main() {
	args := os.Args[1:]
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	var err error
	switch cmd {
	case "start":
		err = cmdStart(args)
	case "stop":
		err = cmdStop(args)
	case "restart":
		err = cmdRestart(args)
	case "ps":
		err = cmdPs(args)
	case "logs":
		err = cmdLogs(args)
	case "add":
		err = cmdAdd(args)
	case "remove":
		err = cmdRemove(args)
	case "health":
		err = cmdHealth(args)
	case "open":
		err = cmdOpen(args)
	case "config":
		err = cmdConfig(args)
	case "mcp-bridge":
		err = cmdMcpBridge(args)
	case "connect":
		err = cmdConnect(args)
	case "update":
		err = cmdUpdate(args)
	case "backup":
		err = cmdBackup(args)
	case "restore":
		err = cmdRestore(args)
	case "version":
		cmdVersion()
	case "help":
		cmdHelp()
	default:
		ui.Err("Unknown command: %s", cmd)
		cmdHelp()
		os.Exit(1)
	}
	if err != nil {
		ui.Err("%s", err.Error())
		os.Exit(1)
	}
}

// --- flag parsing ---

// newFlagSet returns a pflag.FlagSet configured the way every command here
// wants it: silent on error/help (each caller reports its own usage string
// through the same path every other command error takes, formatted by
// main()'s single ui.Err call, instead of pflag's own stderr writer).
// pflag (not the stdlib flag package) specifically because its parser
// doesn't stop at the first positional arg — flags can appear anywhere
// relative to positionals, matching this CLI's existing documented usage
// (e.g. "brain add work 3589 3590 --tag edge") with no extra code needed.
func newFlagSet(name string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// resolveImage picks the image for a new/updated instance: --image wins
// outright, else --tag against the default repo, else the CLI's own
// embedded default tag.
func resolveImage(tag, image string) string {
	if image != "" {
		return image
	}
	if tag != "" {
		return imageRef(tag)
	}
	return imageRef(defaultImageTag)
}

// resolveArtifactsHost returns the instance's explicit --artifacts-host
// override if one was set; otherwise it auto-detects a LAN-reachable
// address fresh on every call (deliberately not cached in instances.yaml,
// so it tracks the host's current network rather than going stale).
func resolveArtifactsHost(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if hostip.IsWSL() {
		ui.Info("Detected WSL — its own network address isn't reachable from your LAN, so")
		ui.Info("defaulting artifact URLs to 127.0.0.1 (works fine from Windows itself).")
		ui.Info("For other devices, find your Windows host's LAN IP (`ipconfig` in Windows)")
		ui.Info("and set it: brain update <name> --artifacts-host <windows-lan-ip>")
		return "127.0.0.1"
	}
	return hostip.Detect()
}

// --- state helpers ---

// requireInstances returns a clear error when the registry is empty,
// instead of a command silently seeding a default or (worse) doing nothing
// when it loops over zero "all instances". No command mutates the registry
// as a side effect of running it — brain add is the only thing that adds an
// instance, and it says so explicitly here rather than a phantom instance
// just appearing in instances.yaml the first time any other command runs.
func requireInstances(s *state.State) error {
	if len(s.Instances) == 0 {
		return fmt.Errorf("no instances registered — run `brain add` (with no arguments, it'll prompt you for the details)")
	}
	return nil
}

func sortedNames(s *state.State) []string {
	names := make([]string, 0, len(s.Instances))
	for name := range s.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// targets resolves the instance name(s) a command should act on: the
// explicit name if given (must exist), or every instance in sorted order.
func targets(s *state.State, args []string) ([]string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name := args[0]
		if _, ok := s.Instances[name]; !ok {
			return nil, fmt.Errorf(`instance "%s" not found`, name)
		}
		return []string{name}, nil
	}
	return sortedNames(s), nil
}

// explicitTarget reports whether targets(s, args) resolved to a single
// user-named instance rather than "all instances" (the no-name case). The
// distinction matters for the Host-set guard: an explicitly named remote
// instance should hard-refuse, while "all instances" should just skip it
// with a note.
func explicitTarget(args []string) bool {
	return len(args) > 0 && !strings.HasPrefix(args[0], "-")
}

// --- commands ---

func cmdStart(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	explicit := explicitTarget(args)
	for _, name := range names {
		inst := s.Instances[name]
		if err := state.RequireManaged(name, inst, "start"); err != nil {
			if explicit {
				return err
			}
			ui.Info("skipping %s (remote, not managed here)", name)
			continue
		}
		container := state.ContainerName(name)

		if err := docker.VolumeEnsure(state.DataVolume(name)); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		if err := docker.VolumeEnsure(state.CacheVolume); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}

		if !docker.ContainerExists(container) {
			if !docker.ImageExistsLocally(inst.Image) {
				ui.Info("Pulling %s…", inst.Image)
				if err := docker.Pull(inst.Image); err != nil {
					ui.Err("%s: %s", name, err.Error())
					continue
				}
			}
			key, _ := config.AnthropicKey()
			if err := docker.Create(docker.CreateOpts{
				ContainerName: container,
				Image:         inst.Image,
				MCPPort:       inst.MCPPort,
				ArtifactsPort: inst.ArtifactsPort,
				DataVolume:    state.DataVolume(name),
				CacheVolume:   state.CacheVolume,
				InstanceName:  name,
				AnthropicKey:  key,
				ArtifactsHost: resolveArtifactsHost(inst.ArtifactsHost),
			}); err != nil {
				ui.Err("%s: %s", name, err.Error())
				continue
			}
		}

		if err := docker.Start(container); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		ui.Info("Starting brain-%s…", name)
	}
	ui.Ok("Done. Run `brain ps` to check status.")
	return nil
}

func cmdStop(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	explicit := explicitTarget(args)
	for _, name := range names {
		if err := state.RequireManaged(name, s.Instances[name], "stop"); err != nil {
			if explicit {
				return err
			}
			ui.Info("skipping %s (remote, not managed here)", name)
			continue
		}
		ui.Info("Stopping brain-%s…", name)
		if err := docker.Stop(state.ContainerName(name)); err != nil {
			ui.Err("%s: %s", name, err.Error())
		}
	}
	ui.Ok("Stopped.")
	return nil
}

func cmdRestart(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	explicit := explicitTarget(args)
	for _, name := range names {
		if err := state.RequireManaged(name, s.Instances[name], "restart"); err != nil {
			if explicit {
				return err
			}
			ui.Info("skipping %s (remote, not managed here)", name)
			continue
		}
		if err := docker.Restart(state.ContainerName(name)); err != nil {
			ui.Err("%s: %s", name, err.Error())
		}
	}
	ui.Ok("Restarted.")
	return nil
}

func cmdPs(_ []string) error {
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names := sortedNames(s)

	ui.Bold("\nBrain instances:\n")
	for _, name := range names {
		inst := s.Instances[name]
		remote := inst.Host != ""
		fetchHost := "127.0.0.1"
		if remote {
			fetchHost = inst.Host
		}
		h := health.Fetch(fetchHost, inst.MCPPort)
		var status string
		sessions := ""
		switch {
		case h != nil && h.OK:
			status = ui.GreenStr("running")
			if remote {
				status += " " + ui.DimStr("(remote)")
			}
			plural := "s"
			if h.Sessions == 1 {
				plural = ""
			}
			sessions = fmt.Sprintf(" · %d session%s", h.Sessions, plural)
		case remote:
			// Distinct from "offline": a remote entry has no local
			// Docker container to be down, it's just unreachable.
			status = ui.DimStr("(remote)")
		default:
			status = ui.DimStr("offline")
		}
		mcpURL := ui.DimStr(fmt.Sprintf("mcp: http://%s:%d/mcp", fetchHost, inst.MCPPort))
		uiURL := ui.DimStr(fmt.Sprintf("ui: http://%s:%d/ui", fetchHost, inst.MCPPort))
		artifactsURL := ui.DimStr(fmt.Sprintf("artifacts: http://%s:%d/artifacts", fetchHost, inst.ArtifactsPort))
		fmt.Printf("  %-16s %s%s\n      %s  %s  %s\n", ui.Magenta(name), status, sessions, mcpURL, uiURL, artifactsURL)
	}
	fmt.Println()
	return nil
}

func cmdLogs(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	fs := newFlagSet("logs")
	var follow bool
	fs.BoolVarP(&follow, "follow", "f", false, "follow log output")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s\nusage: brain logs [name] [-f | --follow]", err)
	}
	filtered := fs.Args()

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, filtered)
	if err != nil {
		return err
	}
	explicit := explicitTarget(filtered)

	managed := names[:0:0]
	for _, name := range names {
		if err := state.RequireManaged(name, s.Instances[name], "show logs for"); err != nil {
			if explicit {
				return err
			}
			ui.Info("skipping %s (remote, not managed here)", name)
			continue
		}
		managed = append(managed, name)
	}
	names = managed

	if len(names) == 1 {
		return docker.Logs(state.ContainerName(names[0]), follow)
	}

	// Multiple instances: `docker logs` can't multiplex like `docker
	// compose logs` did, so stream each concurrently with a name prefix.
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_ = docker.LogsPrefixed(state.ContainerName(name), name, follow, os.Stdout)
		}(name)
	}
	wg.Wait()
	return nil
}

// cmdAdd is the only command that mutates the registry from an empty
// state (see requireInstances) — with no arguments at all it walks the
// user through it interactively instead of erroring on a usage message.
// Any arguments, even partial ones, keep the exact flag/positional
// behavior this always had.
func cmdAdd(args []string) error {
	if len(args) == 0 {
		return cmdAddInteractive()
	}

	fs := newFlagSet("add")
	var tag, image, artifactsHost, host string
	fs.StringVar(&tag, "tag", "", "image tag from the default repo")
	fs.StringVar(&image, "image", "", "full image ref (overrides --tag)")
	fs.StringVar(&artifactsHost, "artifacts-host", "", "override the host used in artifact URLs")
	fs.StringVar(&host, "host", "", "register a remote instance managed elsewhere (mutually exclusive with --tag/--image)")
	const usage = "usage: brain add <name> <mcp-port> <artifacts-port> [--tag T | --image I] [--artifacts-host HOST]\n" +
		"       or:    brain add <name> <mcp-port> <artifacts-port> --host HOST   (register a remote instance; not managed by this brain)\n" +
		"       or:    brain add                                                  (prompts for everything)\n" +
		"       e.g.  brain add work 3589 3590\n" +
		"       LAN artifact URLs are auto-detected; --artifacts-host only needed to override (e.g. for Tailscale):\n" +
		"       e.g.  brain add remote 3589 3590 --artifacts-host 100.x.y.z\n" +
		"       (already have \"home\"? use `brain update home --artifacts-host ...` instead)"
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, usage)
	}

	rest := fs.Args()
	if len(rest) < 3 {
		return fmt.Errorf("%s", usage)
	}
	name, mcpStr, artStr := rest[0], rest[1], rest[2]

	mcpPort, err1 := strconv.Atoi(mcpStr)
	artPort, err2 := strconv.Atoi(artStr)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("ports must be integers")
	}

	if host != "" && (tag != "" || image != "") {
		return fmt.Errorf("--host can't be combined with --tag/--image — a remote instance isn't a locally-pulled image")
	}

	s, err := state.Load()
	if err != nil {
		return err
	}
	return addInstance(s, name, mcpPort, artPort, tag, image, artifactsHost, host)
}

// cmdAddInteractive prompts for every brain add option one at a time,
// suggesting the same defaults instances.yaml used to get seeded with
// silently — now explicit and confirmable instead of a side effect of
// running some unrelated command on an empty registry.
func cmdAddInteractive() error {
	s, err := state.Load()
	if err != nil {
		return err
	}

	fmt.Println("No arguments given — let's walk through it. Press enter to accept the default.")
	p := prompt.New(os.Stdin)

	defaultName := ""
	if len(s.Instances) == 0 {
		defaultName = "home"
	}
	name, err := p.String("Instance name", defaultName)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}

	remote, err := p.YesNo("Remote instance (managed on another host, e.g. via Tailscale)?", false)
	if err != nil {
		return err
	}

	mcpPort, err := p.Int("MCP port", 3579)
	if err != nil {
		return err
	}
	artPort, err := p.Int("Artifacts port", 3580)
	if err != nil {
		return err
	}

	if remote {
		host, err := p.String("Remote host (IP or hostname)", "")
		if err != nil {
			return err
		}
		if host == "" {
			return fmt.Errorf("host is required for a remote instance")
		}
		return addInstance(s, name, mcpPort, artPort, "", "", "", host)
	}

	tag, err := p.String("Image tag (blank = match this CLI's own version)", "")
	if err != nil {
		return err
	}
	artifactsHost, err := p.String("Artifacts host override (blank = auto-detect)", "")
	if err != nil {
		return err
	}
	return addInstance(s, name, mcpPort, artPort, tag, "", artifactsHost, "")
}

// addInstance validates and saves one new instance, then prints the same
// confirmation cmdAdd always has — shared by the flag-driven and
// interactive paths so there's exactly one place that enforces name
// uniqueness and port conflicts.
func addInstance(s *state.State, name string, mcpPort, artPort int, tag, image, artifactsHost, host string) error {
	if _, exists := s.Instances[name]; exists {
		return fmt.Errorf(`instance "%s" already exists`, name)
	}

	inst := &state.Instance{
		MCPPort:       mcpPort,
		ArtifactsPort: artPort,
		Host:          host,
	}
	if host == "" {
		// Nothing is bound locally for a remote entry, so the local
		// port-conflict check doesn't apply — the numbers just describe
		// where the remote instance already lives.
		if conflict := s.PortConflict(mcpPort, artPort, ""); conflict != "" {
			return fmt.Errorf("port conflict with existing instance %q", conflict)
		}
		inst.Image = resolveImage(tag, image)
		inst.ArtifactsHost = artifactsHost
	}
	s.Instances[name] = inst
	if err := state.Save(s); err != nil {
		return err
	}

	if host != "" {
		ui.Ok(`Registered remote instance "%s" -> %s:%d (not managed by this brain)`, name, host, mcpPort)
		ui.Info(fmt.Sprintf("MCP URL: http://%s:%d/mcp", host, mcpPort))
		ui.Info(fmt.Sprintf("UI:      http://%s:%d/ui", host, mcpPort))
		return nil
	}

	ui.Ok(`Added instance "%s" (MCP :%d, artifacts :%d)`, name, mcpPort, artPort)
	ui.Info("Start it with: brain start " + name)
	ui.Info(fmt.Sprintf("MCP URL: http://127.0.0.1:%d/mcp", mcpPort))
	ui.Info(fmt.Sprintf("UI:      http://127.0.0.1:%d/ui", mcpPort))
	return nil
}

func cmdRemove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: brain remove <name>")
	}
	name := args[0]

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	if _, exists := s.Instances[name]; !exists {
		return fmt.Errorf(`instance "%s" not found`, name)
	}

	container := state.ContainerName(name)
	_ = docker.Stop(container)
	docker.RemoveContainer(container)

	delete(s.Instances, name)
	if err := state.Save(s); err != nil {
		return err
	}

	ui.Ok(`Removed instance "%s"`, name)
	vol := state.DataVolume(name)
	fmt.Printf("\nNote: The Docker volume %q still holds your data.\n", vol)
	fmt.Printf("To permanently delete it: docker volume rm %s\n", vol)
	return nil
}

func cmdHealth(args []string) error {
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	for _, name := range names {
		inst := s.Instances[name]
		fetchHost := "127.0.0.1"
		if inst.Host != "" {
			fetchHost = inst.Host
		}
		h := health.Fetch(fetchHost, inst.MCPPort)
		if h != nil && h.OK {
			plural := "s"
			if h.Sessions == 1 {
				plural = ""
			}
			ui.Ok("%s (port %d) — %d active session%s", name, inst.MCPPort, h.Sessions, plural)
		} else {
			ui.Err("%s (port %d) — not responding", name, inst.MCPPort)
		}
	}
	return nil
}

func cmdOpen(args []string) error {
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	all := sortedNames(s)
	name := ""
	if len(args) > 0 {
		name = args[0]
	} else if len(all) > 0 {
		name = all[0]
	}
	if name == "" {
		return fmt.Errorf("no instances found")
	}
	inst, ok := s.Instances[name]
	if !ok {
		return fmt.Errorf(`instance "%s" not found`, name)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/ui", inst.MCPPort)
	ui.Info("Opening %s", url)

	var openCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		openCmd = exec.Command("open", url)
	case "windows":
		openCmd = exec.Command("cmd", "/c", "start", url)
	default:
		openCmd = exec.Command("xdg-open", url)
	}
	return openCmd.Start()
}

func cmdConfig(_ []string) error {
	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names := sortedNames(s)

	ui.Bold("\nAdd to ~/.claude/settings.json → \"mcpServers\":\n")
	entries := make([]string, 0, len(names))
	for _, name := range names {
		inst := s.Instances[name]
		entries = append(entries, fmt.Sprintf(
			"    \"project-brain-%s\": {\n      \"type\": \"http\",\n      \"url\": \"http://127.0.0.1:%d/mcp\"\n    }",
			name, inst.MCPPort,
		))
	}
	fmt.Printf("{\n  \"mcpServers\": {\n%s\n  }\n}\n", strings.Join(entries, ",\n"))
	return nil
}

// cmdConnect dispatches `brain connect <target>`. Only "desktop" exists
// today; the two-level shape leaves room for other clients later without
// a top-level command per client.
func cmdConnect(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: brain connect desktop [name]")
	}
	target := args[0]
	rest := args[1:]
	switch target {
	case "desktop":
		return cmdConnectDesktop(rest)
	default:
		return fmt.Errorf(`unknown connect target %q (only "desktop" is supported)`, target)
	}
}

// cmdConnectDesktop writes (or merges into) Claude Desktop's
// claude_desktop_config.json so Desktop launches `brain mcp-bridge <name>`
// for each target instance. See cli/internal/desktop for path resolution
// and the actual merge/backup logic.
//
// desktop.ConfigPath is resolved before anything else touches disk or even
// loads instances.yaml, so the "no Desktop app on this OS" case (Linux/WSL)
// fails fast without writing anything.
func cmdConnectDesktop(args []string) error {
	path, err := desktop.ConfigPath()
	if err != nil {
		return err
	}

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary: %w", err)
	}

	entries := make(map[string]desktop.Entry, len(names))
	for _, name := range names {
		entries[fmt.Sprintf("project-brain-%s", name)] = desktop.Entry{
			Command: exe,
			Args:    []string{"mcp-bridge", name},
		}
	}

	if err := desktop.Merge(path, entries); err != nil {
		return err
	}

	ui.Ok("Updated %s", path)
	for _, name := range names {
		ui.Info(`  project-brain-%s -> "%s mcp-bridge %s"`, name, exe, name)
	}
	ui.Info("Restart Claude Desktop completely (quit, not just close the window) to pick this up.")
	return nil
}

// cmdMcpBridge is the thin CLI entry point for the stdio<->HTTP proxy;
// see cli/internal/bridge for the actual proxy logic. This is the
// subprocess Claude Desktop's config launches per instance — stdout is
// reserved for JSON-RPC framing, so nothing here (or in bridge.Run) may
// write anything else to it.
func cmdMcpBridge(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: brain mcp-bridge <name>")
	}
	name := args[0]

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	inst, ok := s.Instances[name]
	if !ok {
		return fmt.Errorf(`instance "%s" not found`, name)
	}

	host := inst.Host
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d/mcp", host, inst.MCPPort)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return bridge.New(url).Run(ctx, os.Stdin, os.Stdout, os.Stderr)
}

func cmdUpdate(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	fs := newFlagSet("update")
	var tag, image, artifactsHost string
	fs.StringVar(&tag, "tag", "", "image tag from the default repo")
	fs.StringVar(&image, "image", "", "full image ref (overrides --tag)")
	fs.StringVar(&artifactsHost, "artifacts-host", "", "override the host used in artifact URLs")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s\nusage: brain update [name] [--tag T | --image I] [--artifacts-host HOST]", err)
	}
	rest := fs.Args()

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	names, err := targets(s, rest)
	if err != nil {
		return err
	}
	explicit := explicitTarget(rest)

	changed := false
	for _, name := range names {
		inst := s.Instances[name]
		if err := state.RequireManaged(name, inst, "update"); err != nil {
			if explicit {
				return err
			}
			ui.Info("skipping %s (remote, not managed here)", name)
			continue
		}
		newImage := inst.Image
		if tag != "" || image != "" {
			newImage = resolveImage(tag, image)
		}
		if artifactsHost != "" && artifactsHost != inst.ArtifactsHost {
			inst.ArtifactsHost = artifactsHost
			changed = true
		}

		ui.Info("Updating brain-%s to %s…", name, newImage)
		if err := docker.Pull(newImage); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}

		container := state.ContainerName(name)
		_ = docker.Stop(container)
		docker.RemoveContainer(container)

		if newImage != inst.Image {
			inst.Image = newImage
			changed = true
		}

		if err := docker.VolumeEnsure(state.DataVolume(name)); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		if err := docker.VolumeEnsure(state.CacheVolume); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		key, _ := config.AnthropicKey()
		if err := docker.Create(docker.CreateOpts{
			ContainerName: container,
			Image:         inst.Image,
			MCPPort:       inst.MCPPort,
			ArtifactsPort: inst.ArtifactsPort,
			DataVolume:    state.DataVolume(name),
			CacheVolume:   state.CacheVolume,
			InstanceName:  name,
			AnthropicKey:  key,
			ArtifactsHost: resolveArtifactsHost(inst.ArtifactsHost),
		}); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		if err := docker.Start(container); err != nil {
			ui.Err("%s: %s", name, err.Error())
			continue
		}
		ui.Ok("%s updated.", name)
	}

	if changed {
		if err := state.Save(s); err != nil {
			return err
		}
	}
	return nil
}

// ensureAlpineImage pulls the small throwaway image backup/restore run a
// container from, printing progress the first time — mirrors how cmdStart
// pulls a missing server image rather than letting `docker run` block
// silently on its own implicit pull.
func ensureAlpineImage() error {
	if docker.ImageExistsLocally(alpineImage) {
		return nil
	}
	ui.Info("Pulling %s (used to read/write the backup archive)…", alpineImage)
	return docker.Pull(alpineImage)
}

// cmdBackup tars an instance's whole data volume to a file — whatever's in
// memory.sqlite, its WAL, the LanceDB vectors dataset, and artifact blobs.
// Deliberately not SQLite/LanceDB-aware: a portable volume snapshot rather
// than logic that has to track internal schema changes. Runs against a live
// instance by default (no --live/--force flag in v1) — memory_set already
// writes SQLite and LanceDB via a fire-and-forget setImmediate, so the two
// stores are already only eventually-consistent with each other during
// normal operation; a live volume tar is no worse than that existing
// steady state. The shared brain-hf-cache volume is deliberately excluded —
// it's a re-downloadable model-weights cache, not instance data.
func cmdBackup(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: brain backup <name> [outfile]")
	}
	name := args[0]

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	inst, ok := s.Instances[name]
	if !ok {
		return fmt.Errorf(`instance "%s" not found`, name)
	}
	if err := state.RequireManaged(name, inst, "back up"); err != nil {
		return err
	}

	outFile := fmt.Sprintf("brain-%s-%s.tar.gz", name, time.Now().Format("20060102-150405"))
	if len(args) >= 2 {
		outFile = args[1]
	}
	absOut, err := filepath.Abs(outFile)
	if err != nil {
		return err
	}
	outDir := filepath.Dir(absOut)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	if err := ensureAlpineImage(); err != nil {
		return err
	}

	volume := state.DataVolume(name)
	ui.Info("Backing up %s (volume %s) to %s…", name, volume, absOut)
	if err := docker.BackupVolume(volume, outDir, filepath.Base(absOut)); err != nil {
		return err
	}
	ui.Ok("Backed up %s to %s", name, absOut)
	return nil
}

// cmdRestore repopulates an already-registered instance's data volume from a
// backup produced by cmdBackup. Scoped narrowly on purpose: it assumes
// `<name>` already exists via a normal `brain add` (ports/image are
// host-specific and could conflict on a different target machine, so
// restore doesn't try to recreate the registry entry) and only touches the
// volume's contents.
func cmdRestore(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	fs := newFlagSet("restore")
	var force bool
	fs.BoolVar(&force, "force", false, "overwrite an existing non-empty volume")
	const usage = "usage: brain restore <name> <backup-file> [--force]"
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, usage)
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("%s", usage)
	}
	name, backupFile := rest[0], rest[1]

	s, err := state.Load()
	if err != nil {
		return err
	}
	if err := requireInstances(s); err != nil {
		return err
	}
	inst, ok := s.Instances[name]
	if !ok {
		return fmt.Errorf(`instance "%s" not found — register it first with "brain add"`, name)
	}
	if err := state.RequireManaged(name, inst, "restore"); err != nil {
		return err
	}

	absBackup, err := filepath.Abs(backupFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absBackup); err != nil {
		return fmt.Errorf("backup file %q not found", backupFile)
	}

	// Refuse while running — the volume's data would be in active use by
	// the server process (open SQLite handles, in-flight writes).
	if docker.ContainerRunning(state.ContainerName(name)) {
		return fmt.Errorf(`instance "%s" is running — stop it first: brain stop %s`, name, name)
	}

	if err := ensureAlpineImage(); err != nil {
		return err
	}

	volume := state.DataVolume(name)
	if err := docker.VolumeEnsure(volume); err != nil {
		return err
	}
	if !force {
		empty, err := docker.VolumeEmpty(volume)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("volume %q already has data — pass --force to overwrite it", volume)
		}
	}

	ui.Info("Restoring %s from %s into volume %s…", name, absBackup, volume)
	if err := docker.RestoreVolume(volume, filepath.Dir(absBackup), filepath.Base(absBackup)); err != nil {
		return err
	}
	ui.Ok("Restored %s. Start it with: brain start %s", name, name)
	return nil
}

func cmdVersion() {
	fmt.Printf("brain %s\n", version)
	fmt.Printf("default server image: %s\n", imageRef(defaultImageTag))
}

func cmdHelp() {
	ui.Bold("\nproject-brain CLI\n")
	rows := [][2]string{
		{"start [name]", "Start instance(s) — all if no name given"},
		{"stop [name]", "Stop instance(s)"},
		{"restart [name]", "Restart instance(s)"},
		{"ps", "List all instances and health status"},
		{"logs [name] [-f]", "Show logs (follow with -f)"},
		{"add [name] [mcp] [art]", "Add a new instance (--tag/--image, --artifacts-host, --host for remote; no args = interactive)"},
		{"remove <name>", "Remove an instance (data volume preserved)"},
		{"update [name]", "Pull + recreate instance(s) (--tag/--image, --artifacts-host)"},
		{"backup <name> [outfile]", "Tar an instance's data volume to outfile (default: ./brain-<name>-<time>.tar.gz)"},
		{"restore <name> <file>", "Restore a backup into an instance's volume (--force to overwrite existing data)"},
		{"health [name]", "Hit health endpoint(s) directly"},
		{"open [name]", "Open Web UI in browser"},
		{"config", "Print MCP config for ~/.claude/settings.json"},
		{"connect desktop [name]", "Write Claude Desktop's config to launch instance(s) via mcp-bridge"},
		{"mcp-bridge <name>", "stdio<->HTTP bridge for Claude Desktop (low-level; used as a subprocess)"},
		{"version", "Show CLI version and default server image"},
		{"help", "Show this help"},
	}
	for _, row := range rows {
		fmt.Printf("  %sbrain %-26s%s%s%s\n", "\x1b[36m", row[0], "\x1b[0m", "\x1b[2m", row[1]+"\x1b[0m")
	}
	fmt.Println()
}
