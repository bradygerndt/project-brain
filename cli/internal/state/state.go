// Package state owns brain's instance registry: ~/.config/brain/instances.yaml.
// This replaces docker-compose.yml — the CLI fully owns this schema on both
// read and write, so there's exactly one shape to parse (no round-tripping
// hand-edited files, no compose's polymorphic port/volume shapes).
package state

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/bradygerndt/project-brain/cli/internal/paths"
)

// Instance is one managed project-brain server.
type Instance struct {
	MCPPort       int    `yaml:"mcpPort"`
	ArtifactsPort int    `yaml:"artifactsPort"`
	Image         string `yaml:"image"`
	// ArtifactsHost overrides the host used in artifact URLs the server
	// returns (e.g. a Tailscale hostname or LAN IP). Without it, the
	// server falls back to the container's own network interface — which
	// on Docker is an internal bridge IP unreachable from anywhere but
	// the host itself.
	ArtifactsHost string `yaml:"artifactsHost,omitempty"`
	// Host distinguishes a Docker-managed instance from a remote-reference
	// one:
	//   - Empty (default): Docker-managed on this host. Every URL uses
	//     127.0.0.1, exactly today's behavior — nothing changes for
	//     existing instances.
	//   - Set: reference-only. This is just a remembered pointer to an
	//     instance managed elsewhere (e.g. a laptop reaching a home
	//     server over Tailscale). Every URL uses this host instead of
	//     127.0.0.1, and `brain` never shells out to `docker` for it —
	//     Image/ArtifactsHost are left unused. See RequireManaged.
	Host string `yaml:"host,omitempty"`
}

// State is the full instance registry.
type State struct {
	Instances map[string]*Instance `yaml:"instances"`
}

func fileName() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "instances.yaml"), nil
}

// Load reads instances.yaml. A missing file is not an error — it returns an
// empty State so callers can decide whether to seed a default instance.
func Load() (*State, error) {
	file, err := fileName()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return &State{Instances: map[string]*Instance{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", file, err)
	}
	if s.Instances == nil {
		s.Instances = map[string]*Instance{}
	}
	return &s, nil
}

// Save writes instances.yaml, creating the config directory if needed.
func Save(s *State) error {
	dir, err := paths.EnsureConfigDir()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "instances.yaml"), data, 0o644)
}

// ContainerName returns the docker container name for an instance.
func ContainerName(name string) string {
	return "brain-" + name
}

// DataVolume returns the per-instance data volume name. Unlike the old
// compose-managed volume, this has no "project-brain_" project prefix.
func DataVolume(name string) string {
	return "brain-" + name + "-data"
}

// CacheVolume is the shared HF embedding-model cache volume, shared across
// all instances. Renamed from "hf-cache" (compose's project-name prefix
// used to prevent collisions with unrelated projects on the same host;
// without compose, the name itself needs to be collision-resistant).
const CacheVolume = "brain-hf-cache"

// RequireManaged returns an error if inst is a remote-reference instance
// (Host set) rather than one this brain manages via Docker. Every command
// that shells out to `docker` for a specific instance — start/stop/
// restart/logs/update, and backup/restore — must call this before running
// any docker command, so a remote entry fails with a clear, specific error
// instead of either a raw docker failure or (worse, for `update`) silently
// standing up a brand-new local container under a name the user only meant
// as a pointer elsewhere.
//
// verb is the action being refused, used in the trailing "nothing to
// <verb>" clause (e.g. "start", "update", "show logs for").
func RequireManaged(name string, inst *Instance, verb string) error {
	if inst.Host != "" {
		return fmt.Errorf("instance %q is remote (host: %s) — not managed by this brain, nothing to %s", name, inst.Host, verb)
	}
	return nil
}

// PortConflict returns the instance name already using mcpPort or
// artifactsPort, if any, excluding `except`.
func (s *State) PortConflict(mcpPort, artifactsPort int, except string) string {
	for name, inst := range s.Instances {
		if name == except {
			continue
		}
		if inst.MCPPort == mcpPort || inst.ArtifactsPort == mcpPort ||
			inst.MCPPort == artifactsPort || inst.ArtifactsPort == artifactsPort {
			return name
		}
	}
	return ""
}
