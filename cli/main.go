// brain is the CLI for managing project-brain instances. It owns Docker
// image/composition only — no native bindings, no Node/npm. Instance state
// lives at ~/.config/brain/instances.yaml; server images are pulled from
// ghcr.io/bradygerndt/project-brain rather than built locally.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bradygerndt/project-brain/cli/internal/config"
	"github.com/bradygerndt/project-brain/cli/internal/docker"
	"github.com/bradygerndt/project-brain/cli/internal/health"
	"github.com/bradygerndt/project-brain/cli/internal/hostip"
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
	case "update":
		err = cmdUpdate(args)
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

// extractFlag pulls "--name value" out of args wherever it appears,
// returning the remaining positional args and the flag's value (or "").
func extractFlag(args []string, name string) (positional []string, value string) {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			value = args[i+1]
			positional = append(positional, args[:i]...)
			positional = append(positional, args[i+2:]...)
			return positional, value
		}
	}
	return args, ""
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

// loadSeeded loads instances.yaml, seeding a default "home" instance
// (matching the ports the old shipped docker-compose.yml used) on first
// run so a fresh install works out of the box.
func loadSeeded() (*state.State, error) {
	s, err := state.Load()
	if err != nil {
		return nil, err
	}
	if len(s.Instances) == 0 {
		s.Instances["home"] = &state.Instance{
			MCPPort:       3579,
			ArtifactsPort: 3580,
			Image:         imageRef(defaultImageTag),
		}
		if err := state.Save(s); err != nil {
			return nil, err
		}
	}
	return s, nil
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

// --- commands ---

func cmdStart(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	for _, name := range names {
		inst := s.Instances[name]
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
	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	for _, name := range names {
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
	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := docker.Restart(state.ContainerName(name)); err != nil {
			ui.Err("%s: %s", name, err.Error())
		}
	}
	ui.Ok("Restarted.")
	return nil
}

func cmdPs(_ []string) error {
	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names := sortedNames(s)
	if len(names) == 0 {
		ui.Info("No instances defined.")
		return nil
	}

	ui.Bold("\nBrain instances:\n")
	for _, name := range names {
		inst := s.Instances[name]
		h := health.Fetch(inst.MCPPort)
		status := ui.DimStr("offline")
		sessions := ""
		if h != nil && h.OK {
			status = ui.GreenStr("running")
			plural := "s"
			if h.Sessions == 1 {
				plural = ""
			}
			sessions = fmt.Sprintf(" · %d session%s", h.Sessions, plural)
		}
		mcpURL := ui.DimStr(fmt.Sprintf("mcp: http://127.0.0.1:%d/mcp", inst.MCPPort))
		uiURL := ui.DimStr(fmt.Sprintf("ui: http://127.0.0.1:%d/ui", inst.MCPPort))
		artifactsURL := ui.DimStr(fmt.Sprintf("artifacts: http://127.0.0.1:%d/artifacts", inst.ArtifactsPort))
		fmt.Printf("  %-16s %s%s\n      %s  %s  %s\n", ui.Magenta(name), status, sessions, mcpURL, uiURL, artifactsURL)
	}
	fmt.Println()
	return nil
}

func cmdLogs(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	follow := false
	filtered := args[:0:0]
	for _, a := range args {
		if a == "-f" || a == "--follow" {
			follow = true
		} else {
			filtered = append(filtered, a)
		}
	}

	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, filtered)
	if err != nil {
		return err
	}

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

func cmdAdd(args []string) error {
	args, tag := extractFlag(args, "--tag")
	args, image := extractFlag(args, "--image")
	args, artifactsHost := extractFlag(args, "--artifacts-host")

	if len(args) < 3 {
		return fmt.Errorf("usage: brain add <name> <mcp-port> <artifacts-port> [--tag T | --image I] [--artifacts-host HOST]\n       e.g.  brain add work 3589 3590\n       LAN artifact URLs are auto-detected; --artifacts-host only needed to override (e.g. for Tailscale):\n       e.g.  brain add remote 3589 3590 --artifacts-host 100.x.y.z\n       (already have \"home\"? use `brain update home --artifacts-host ...` instead)")
	}
	name, mcpStr, artStr := args[0], args[1], args[2]

	mcpPort, err1 := strconv.Atoi(mcpStr)
	artPort, err2 := strconv.Atoi(artStr)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("ports must be integers")
	}

	s, err := loadSeeded()
	if err != nil {
		return err
	}
	if _, exists := s.Instances[name]; exists {
		return fmt.Errorf(`instance "%s" already exists`, name)
	}
	if conflict := s.PortConflict(mcpPort, artPort, ""); conflict != "" {
		return fmt.Errorf("port conflict with existing instance %q", conflict)
	}

	s.Instances[name] = &state.Instance{
		MCPPort:       mcpPort,
		ArtifactsPort: artPort,
		Image:         resolveImage(tag, image),
		ArtifactsHost: artifactsHost,
	}
	if err := state.Save(s); err != nil {
		return err
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

	s, err := loadSeeded()
	if err != nil {
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
	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}
	for _, name := range names {
		inst := s.Instances[name]
		h := health.Fetch(inst.MCPPort)
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
	s, err := loadSeeded()
	if err != nil {
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
	s, err := loadSeeded()
	if err != nil {
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

func cmdUpdate(args []string) error {
	if err := docker.CheckAvailable(); err != nil {
		return err
	}
	args, tag := extractFlag(args, "--tag")
	args, image := extractFlag(args, "--image")
	args, artifactsHost := extractFlag(args, "--artifacts-host")

	s, err := loadSeeded()
	if err != nil {
		return err
	}
	names, err := targets(s, args)
	if err != nil {
		return err
	}

	changed := false
	for _, name := range names {
		inst := s.Instances[name]
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
		{"add <name> <mcp> <art>", "Add a new instance (--tag/--image, --artifacts-host)"},
		{"remove <name>", "Remove an instance (data volume preserved)"},
		{"update [name]", "Pull the latest image and recreate instance(s)"},
		{"health [name]", "Hit health endpoint(s) directly"},
		{"open [name]", "Open Web UI in browser"},
		{"config", "Print MCP config for ~/.claude/settings.json"},
		{"version", "Show CLI version and default server image"},
		{"help", "Show this help"},
	}
	for _, row := range rows {
		fmt.Printf("  %sbrain %-26s%s%s%s\n", "\x1b[36m", row[0], "\x1b[0m", "\x1b[2m", row[1]+"\x1b[0m")
	}
	fmt.Println()
}
