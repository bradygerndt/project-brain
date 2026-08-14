// Package desktop generates and merges Claude Desktop's
// claude_desktop_config.json so `brain connect desktop` can wire up
// project-brain instances without the user hand-editing JSON.
//
// Path resolution (ConfigPath) and the actual read/merge/write (Merge) are
// deliberately separate: Desktop simply doesn't exist on Linux/WSL, so
// ConfigPath refuses early there, before anything touches disk. Merge takes
// an explicit path and has no OS-specific behavior itself, which also makes
// it exercisable in tests on any platform (see desktop_test.go), unlike
// ConfigPath's darwin/windows branches.
package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ConfigPath resolves Claude Desktop's config file location for the
// current OS. There is no Linux build of Claude Desktop (WSL included —
// it reports GOOS=linux like any other Linux host, no separate check
// needed), so there's nowhere to write on that OS family; ConfigPath
// returns an error rather than guessing a path nothing will ever read,
// the same "don't guess" precedent as the WSL LAN auto-detect in
// docs/lan.md.
func ConfigPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return windowsConfigPath(localAppData, appData), nil
	default:
		return "", fmt.Errorf("Claude Desktop has no Linux build (this includes WSL) — there's no config file here for brain to write.\nRun `brain connect desktop` on the macOS or Windows machine that actually runs Claude Desktop instead")
	}
}

// windowsConfigPath picks between Claude Desktop's two possible Windows
// config locations. The classic installer writes/reads
// %APPDATA%\Claude\claude_desktop_config.json, but the Microsoft Store/MSIX
// build runs inside an AppContainer that transparently redirects %APPDATA%
// to a per-package virtualized folder under
// %LOCALAPPDATA%\Packages\<PackageFamilyName>\LocalCache\Roaming\ — so a
// tool writing to the classic path never reaches the file an
// MSIX-installed Desktop actually reads, and the app just looks like it's
// ignoring the config entirely.
//
// The package family name's publisher-hash suffix is deterministic for a
// given signing identity, but that's not a permanent guarantee — a future
// re-sign (cert rotation, publisher change) could shift it. Rather than
// hardcoding today's exact name, glob for any "Claude_*" package and only
// trust a match that actually contains LocalCache\Roaming\Claude, so this
// keeps working without a code change if the suffix ever does change, at
// the (very unlikely) cost of trusting an unrelated package that happens
// to be named "Claude_..." AND coincidentally has that exact subfolder
// structure.
func windowsConfigPath(localAppData, appData string) string {
	matches, _ := filepath.Glob(filepath.Join(localAppData, "Packages", "Claude_*", "LocalCache", "Roaming", "Claude"))
	for _, dir := range matches {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return filepath.Join(dir, "claude_desktop_config.json")
		}
	}
	return filepath.Join(appData, "Claude", "claude_desktop_config.json")
}

// Entry is one mcpServers block: the local command Desktop should spawn to
// reach one project-brain instance.
type Entry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Merge reads the config file at path (a missing or empty file is fine —
// treated as `{}`), backs up the existing file to path+".bak" if one
// exists, merges the given mcpServers entries in, and writes the result
// back.
//
// Parsing is deliberately generic (map[string]any) rather than a fixed
// struct — Desktop's config schema isn't ours to own, and desktop-mobile.md
// cites a real GitHub issue about Desktop corrupting this file when a tool
// writes something it doesn't expect. Unknown top-level keys and any other
// mcpServers entries not created by brain are preserved untouched; only the
// keys in entries are added or overwritten.
func Merge(path string, entries map[string]Entry) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	doc := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parsing existing %s: %w (not touching it — fix or remove it by hand first)", path, err)
		}
		if err := os.WriteFile(path+".bak", raw, 0o644); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}

	var mcpServers map[string]any
	if raw, exists := doc["mcpServers"]; exists {
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf(`%s has a non-object "mcpServers" key — not touching it; fix or remove it by hand first`, path)
		}
		mcpServers = m
	} else {
		mcpServers = map[string]any{}
	}
	for key, entry := range entries {
		mcpServers[key] = entry
	}
	doc["mcpServers"] = mcpServers

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, out, 0o644)
}
