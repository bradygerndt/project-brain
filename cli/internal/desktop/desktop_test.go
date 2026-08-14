package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMerge_MissingFile_CreatesConfigWithNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("expected no .bak file for a fresh config, got err=%v", err)
	}

	doc := readDoc(t, path)
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or wrong type: %#v", doc["mcpServers"])
	}
	entry, ok := servers["project-brain-home"].(map[string]any)
	if !ok {
		t.Fatalf("project-brain-home entry missing or wrong type: %#v", servers["project-brain-home"])
	}
	if entry["command"] != "/usr/local/bin/brain" {
		t.Errorf("command = %v, want /usr/local/bin/brain", entry["command"])
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "mcp-bridge" || args[1] != "home" {
		t.Errorf("args = %#v, want [mcp-bridge home]", entry["args"])
	}
}

func TestMerge_PreservesUnknownKeysAndOtherServers_AndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")

	original := `{
  "someOtherTopLevelKey": {"nested": true},
  "mcpServers": {
    "some-other-tool": {
      "command": "npx",
      "args": ["some-other-tool"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("reading .bak: %v", err)
	}
	if string(backup) != original {
		t.Errorf(".bak contents = %q, want original untouched: %q", backup, original)
	}

	doc := readDoc(t, path)
	if other, ok := doc["someOtherTopLevelKey"].(map[string]any); !ok || other["nested"] != true {
		t.Errorf("someOtherTopLevelKey not preserved: %#v", doc["someOtherTopLevelKey"])
	}
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["some-other-tool"]; !ok {
		t.Errorf("some-other-tool entry was clobbered: %#v", servers)
	}
	if _, ok := servers["project-brain-home"]; !ok {
		t.Errorf("project-brain-home entry missing: %#v", servers)
	}
}

func TestMerge_MultipleInstancesInOneRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
		"project-brain-work": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "work"}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	doc := readDoc(t, path)
	servers := doc["mcpServers"].(map[string]any)
	if len(servers) != 2 {
		t.Fatalf("expected 2 entries, got %d: %#v", len(servers), servers)
	}
	for _, key := range []string{"project-brain-home", "project-brain-work"} {
		if _, ok := servers[key]; !ok {
			t.Errorf("missing entry %q", key)
		}
	}
}

func TestMerge_SecondRunOverwritesOnlySameKeyedEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")

	if err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/old/brain", Args: []string{"mcp-bridge", "home"}},
	}); err != nil {
		t.Fatalf("first Merge: %v", err)
	}
	if err := Merge(path, map[string]Entry{
		"project-brain-work": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "work"}},
	}); err != nil {
		t.Fatalf("second Merge: %v", err)
	}

	doc := readDoc(t, path)
	servers := doc["mcpServers"].(map[string]any)
	if len(servers) != 2 {
		t.Fatalf("expected both entries to coexist, got %d: %#v", len(servers), servers)
	}
	home := servers["project-brain-home"].(map[string]any)
	if home["command"] != "/old/brain" {
		t.Errorf("unrelated entry project-brain-home was modified: %#v", home)
	}
}

func TestMerge_EmptyFile_TreatedAsEmptyObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("writing empty seed file: %v", err)
	}

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// An empty pre-existing file has nothing worth preserving, but Merge
	// still can't tell "empty" from "about to be created" without stat'ing
	// first — either behavior (backup or not) is acceptable here, this test
	// just confirms Merge doesn't error out on it.
	doc := readDoc(t, path)
	if _, ok := doc["mcpServers"]; !ok {
		t.Errorf("mcpServers missing after merging into an empty file: %#v", doc)
	}
}

func TestMerge_InvalidJSON_RefusesAndDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	original := `{ this is not valid json`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
	})
	if err == nil {
		t.Fatal("expected Merge to refuse on invalid JSON, got nil error")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading file after failed Merge: %v", readErr)
	}
	if string(got) != original {
		t.Errorf("file was modified despite Merge failing: %q", got)
	}
	if _, statErr := os.Stat(path + ".bak"); !os.IsNotExist(statErr) {
		t.Errorf("expected no .bak file to be written on a failed Merge, got err=%v", statErr)
	}
}

func TestMerge_NonObjectMcpServers_Refuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	original := `{"mcpServers": "not an object"}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	err := Merge(path, map[string]Entry{
		"project-brain-home": {Command: "/usr/local/bin/brain", Args: []string{"mcp-bridge", "home"}},
	})
	if err == nil {
		t.Fatal("expected Merge to refuse when mcpServers isn't an object, got nil error")
	}
}

func TestWindowsConfigPath_PrefersMSIXDirWhenItExists(t *testing.T) {
	localAppData := t.TempDir()
	appData := t.TempDir()
	// Deliberately not today's real "Claude_pzs8sxrjxfjjc" — proves the
	// match is a glob, not a hardcoded name, so a future publisher-hash
	// change wouldn't silently break this.
	msixDir := filepath.Join(localAppData, "Packages", "Claude_somefuturehash", "LocalCache", "Roaming", "Claude")
	if err := os.MkdirAll(msixDir, 0o755); err != nil {
		t.Fatalf("seeding msix dir: %v", err)
	}

	got := windowsConfigPath(localAppData, appData)
	want := filepath.Join(msixDir, "claude_desktop_config.json")
	if got != want {
		t.Errorf("windowsConfigPath = %q, want %q", got, want)
	}
}

func TestWindowsConfigPath_FallsBackToClassicWhenNoMSIXDir(t *testing.T) {
	localAppData := t.TempDir()
	appData := t.TempDir()

	got := windowsConfigPath(localAppData, appData)
	want := filepath.Join(appData, "Claude", "claude_desktop_config.json")
	if got != want {
		t.Errorf("windowsConfigPath = %q, want %q", got, want)
	}
}

func TestWindowsConfigPath_IgnoresUnrelatedPackagesFolder(t *testing.T) {
	localAppData := t.TempDir()
	appData := t.TempDir()
	// A Packages dir exists (as it would on any real Windows machine with
	// other Store apps installed), but nothing matching "Claude_*" — must
	// not false-positive on unrelated packages.
	other := filepath.Join(localAppData, "Packages", "SomeOtherApp_abcdef123456")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("seeding unrelated package dir: %v", err)
	}

	got := windowsConfigPath(localAppData, appData)
	want := filepath.Join(appData, "Claude", "claude_desktop_config.json")
	if got != want {
		t.Errorf("windowsConfigPath = %q, want %q (should ignore unrelated package)", got, want)
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("resulting file isn't valid JSON: %v (%s)", err, raw)
	}
	return doc
}
