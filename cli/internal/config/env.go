// Package config loads ~/.config/brain/.env. Compose used to auto-load a
// .env file next to docker-compose.yml for ${ANTHROPIC_API_KEY}
// interpolation; with compose gone, the CLI does that itself before every
// container create.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/bradygerndt/project-brain/cli/internal/paths"
)

const envTemplate = "ANTHROPIC_API_KEY=\n"

// EnvFilePath returns the path to ~/.config/brain/.env.
func EnvFilePath() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".env"), nil
}

// EnsureEnvFile writes a blank template .env if one doesn't exist yet.
// Returns the path and whether it was just created.
func EnsureEnvFile() (path string, created bool, err error) {
	dir, err := paths.EnsureConfigDir()
	if err != nil {
		return "", false, err
	}
	path = filepath.Join(dir, ".env")
	if _, statErr := os.Stat(path); statErr == nil {
		return path, false, nil
	}
	if err := os.WriteFile(path, []byte(envTemplate), 0o600); err != nil {
		return "", false, err
	}
	return path, true, nil
}

// loadEnvFile parses a simple KEY=VALUE .env file. Blank lines and lines
// starting with # are ignored. Not a full dotenv implementation — this
// project's .env has never needed more than flat key=value pairs.
func loadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vars := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		vars[key] = value
	}
	return vars, scanner.Err()
}

// AnthropicKey resolves ANTHROPIC_API_KEY, preferring an already-exported
// shell env var over the value stored in ~/.config/brain/.env.
func AnthropicKey() (string, error) {
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		return v, nil
	}
	path, err := EnvFilePath()
	if err != nil {
		return "", err
	}
	vars, err := loadEnvFile(path)
	if err != nil {
		return "", err
	}
	return vars["ANTHROPIC_API_KEY"], nil
}
