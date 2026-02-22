package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Init creates a new stint workspace at root with the given repos.
// Built-in agent definitions are no longer seeded to disk — they are embedded
// in the binary and loaded on demand by agent.Get(). The .st/agents/ directory
// is still created as a place for user overrides.
func Init(root string, repos map[string]string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	// Check if already a workspace.
	if _, err := os.Stat(filepath.Join(abs, ".st", "workspace.json")); err == nil {
		return nil, fmt.Errorf("%s is already a stint workspace", abs)
	}

	dirs := []string{
		abs,
		filepath.Join(abs, ".st"),
		filepath.Join(abs, ".st", "agents"),
		filepath.Join(abs, ".st", "goals"),
		filepath.Join(abs, ".st", "tasks", "pending"),
		filepath.Join(abs, ".st", "tasks", "running"),
		filepath.Join(abs, ".st", "tasks", "done"),
		filepath.Join(abs, ".st", "tasks", "failed"),
		filepath.Join(abs, ".st", "tasks", "cancelled"),
		filepath.Join(abs, ".st", "tasks", "blocked"),
		filepath.Join(abs, ".st", "workers"),
		filepath.Join(abs, ".st", "worktrees"),
		filepath.Join(abs, ".st", "logs"),
		filepath.Join(abs, ".st", "heartbeats"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", d, err)
		}
	}

	if repos == nil {
		repos = make(map[string]string)
	}
	cfg := Config{Version: 1, Repos: repos}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(abs, ".st", "workspace.json"), data, 0644); err != nil {
		return nil, err
	}

	ws := &Workspace{Root: abs, Config: cfg}
	return ws, nil
}
