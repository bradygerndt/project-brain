// Package paths resolves the directory that holds brain's persistent
// state: instances.yaml and .env. No local repo checkout exists on an
// end user's machine anymore, so this can't live next to the binary.
package paths

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the directory holding instances.yaml and .env.
// Override with BRAIN_CONFIG_DIR; otherwise $XDG_CONFIG_HOME/brain,
// falling back to ~/.config/brain.
func ConfigDir() (string, error) {
	if dir := os.Getenv("BRAIN_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "brain"), nil
}

// EnsureConfigDir returns ConfigDir(), creating it if necessary.
func EnsureConfigDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
