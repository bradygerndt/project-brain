// Package docker wraps plain `docker` CLI subcommands via os/exec — no
// docker-compose, no Docker Engine SDK. Docker itself is already a hard
// requirement to run project-brain, and the CLI ships with every Docker
// install, so shelling out adds no new dependency; it also matches what
// bin/brain.js already did (spawnSync('docker', ...)) for the one piece
// that IS new here (compose -> raw docker commands), keeping this a
// faithful-in-spirit port rather than compounding two changes at once.
package docker

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const LabelKey = "com.project-brain.instance"

// CheckAvailable does a cheap preflight so callers can print a friendly
// install pointer instead of a raw "executable not found" error.
func CheckAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI not found in PATH — install Docker (https://docs.docker.com/get-docker/) and try again")
	}
	return nil
}

func run(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("docker %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// runInherit runs docker with stdio connected directly to the terminal —
// used for `logs -f` and anything meant to stream live.
func runInherit(args ...string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// ContainerExists reports whether a container with this name exists
// (running or stopped).
func ContainerExists(name string) bool {
	_, err := run("inspect", "--type=container", name)
	return err == nil
}

// ImageExistsLocally reports whether the image is already pulled.
func ImageExistsLocally(image string) bool {
	_, err := run("image", "inspect", image)
	return err == nil
}

// Pull pulls an image, streaming progress to the terminal.
func Pull(image string) error {
	return runInherit("pull", image)
}

// VolumeEnsure creates a volume if it doesn't already exist. `docker volume
// create` is itself idempotent (re-running it on an existing volume just
// returns its name), so no existence check is needed first.
func VolumeEnsure(name string) error {
	_, err := run("volume", "create", name)
	return err
}

// VolumeRemove removes a named volume.
func VolumeRemove(name string) error {
	_, err := run("volume", "rm", name)
	return err
}

// CreateOpts describes the one container shape project-brain instances use:
// one server, one data volume, one shared embedding-model cache volume.
type CreateOpts struct {
	ContainerName string
	Image         string
	MCPPort       int
	ArtifactsPort int
	DataVolume    string
	CacheVolume   string
	InstanceName  string // BRAIN_NAME env var
	AnthropicKey  string // may be empty — only needed for memory_extract
	ArtifactsHost string // may be empty — overrides the host in artifact URLs
}

// Create makes (but does not start) a container for an instance.
func Create(opts CreateOpts) error {
	args := []string{
		"create",
		"--name", opts.ContainerName,
		"--restart", "unless-stopped",
		"--label", LabelKey + "=" + opts.InstanceName,
		"-p", fmt.Sprintf("%d:%d", opts.MCPPort, opts.MCPPort),
		"-p", fmt.Sprintf("%d:%d", opts.ArtifactsPort, opts.ArtifactsPort),
		"-v", opts.DataVolume + ":/app/data",
		"-v", opts.CacheVolume + ":/root/.cache/huggingface",
		"-e", "BRAIN_NAME=" + opts.InstanceName,
		"-e", fmt.Sprintf("MCP_PORT=%d", opts.MCPPort),
		"-e", fmt.Sprintf("ARTIFACTS_PORT=%d", opts.ArtifactsPort),
	}
	if opts.AnthropicKey != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY="+opts.AnthropicKey)
	}
	if opts.ArtifactsHost != "" {
		args = append(args, "-e", "ARTIFACTS_HOST="+opts.ArtifactsHost)
	}
	args = append(args, opts.Image)
	_, err := run(args...)
	return err
}

func Start(containerName string) error {
	_, err := run("start", containerName)
	return err
}

func Stop(containerName string) error {
	_, err := run("stop", containerName)
	return err
}

func Restart(containerName string) error {
	_, err := run("restart", containerName)
	return err
}

// RemoveContainer force-removes a container, ignoring "doesn't exist"
// errors — mirrors composeMaybe()'s best-effort semantics in the old CLI.
func RemoveContainer(containerName string) {
	_, _ = run("rm", "-f", containerName)
}

// Logs streams a single container's logs to stdout/stderr directly.
func Logs(containerName string, follow bool) error {
	args := []string{"logs", "--tail=50"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, containerName)
	return runInherit(args...)
}

// ContainerRunning reports whether a container currently exists AND is
// running — distinct from ContainerExists, which is also true for a
// stopped-but-not-removed container. Used by `restore` to refuse touching a
// volume that's currently in use.
func ContainerRunning(name string) bool {
	out, err := run("inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// BackupVolume tars a volume's entire contents to outFile inside outDir, via
// a throwaway alpine container:
//
//	docker run --rm -v <volume>:/data:ro -v <outDir>:/backup alpine \
//	  tar czf /backup/<outFile> -C /data .
//
// Deliberately whole-volume rather than SQLite/LanceDB-aware — future-proof
// against internal schema changes, and produces one portable file. outDir
// must already exist and be an absolute host path (it's a bind mount, not a
// named volume).
func BackupVolume(volume, outDir, outFile string) error {
	_, err := run(
		"run", "--rm",
		"-v", volume+":/data:ro",
		"-v", outDir+":/backup",
		"alpine",
		"tar", "czf", "/backup/"+outFile, "-C", "/data", ".",
	)
	return err
}

// RestoreVolume extracts a tar.gz backup (backupFile inside backupDir) into
// volume, via the same throwaway-alpine-container approach as BackupVolume.
// backupDir must already exist and be an absolute host path.
func RestoreVolume(volume, backupDir, backupFile string) error {
	_, err := run(
		"run", "--rm",
		"-v", volume+":/data",
		"-v", backupDir+":/backup:ro",
		"alpine",
		"tar", "xzf", "/backup/"+backupFile, "-C", "/data",
	)
	return err
}

// VolumeEmpty reports whether a volume has no contents. A volume that
// doesn't exist yet reads as empty too — `docker run` auto-creates named
// volumes on first mount, so there's nothing to lose either way.
func VolumeEmpty(volume string) (bool, error) {
	out, err := run("run", "--rm", "-v", volume+":/data:ro", "alpine", "sh", "-c", "ls -A /data")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// LogsPrefixed streams one container's logs to w, prefixing every line
// with "[name] " — used to merge multiple instances' logs onto one
// stream, since plain `docker logs` (unlike `docker compose logs`) can't
// multiplex several containers itself.
func LogsPrefixed(containerName, prefix string, follow bool, w io.Writer) error {
	args := []string{"logs", "--tail=50"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, containerName)

	cmd := exec.Command("docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}

	buf := make([]byte, 4096)
	var line bytes.Buffer
	flush := func() {
		if line.Len() > 0 {
			fmt.Fprintf(w, "[%s] %s\n", prefix, line.String())
			line.Reset()
		}
	}
	for {
		n, readErr := stdout.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				flush()
			} else {
				line.WriteByte(b)
			}
		}
		if readErr != nil {
			break
		}
	}
	flush()
	return cmd.Wait()
}
