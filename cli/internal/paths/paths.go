// Package paths resolves the directory that holds brain's persistent
// state: instances.yaml and .env. No local repo checkout exists on an
// end user's machine anymore, so this can't live next to the binary.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// ConfigDir returns the directory holding instances.yaml and .env.
// Override with BRAIN_CONFIG_DIR; otherwise %APPDATA%\brain on native
// Windows (WSL reports GOOS=linux, so this doesn't affect it — WSL keeps
// the Unix path below), or $XDG_CONFIG_HOME/brain falling back to
// ~/.config/brain everywhere else.
func ConfigDir() (string, error) {
	if dir := os.Getenv("BRAIN_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "brain"), nil
		}
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
