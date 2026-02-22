package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the .st/workspace.json content.
type Config struct {
	Version int               `json:"version"`
	Repos   map[string]string `json:"repos"`
}

// Workspace holds the root path and validated config.
type Workspace struct {
	Root   string
	Config Config
}

// configPath returns the path to .st/workspace.json for the given root.
func configPath(root string) string {
	return filepath.Join(root, ".st", "workspace.json")
}

// Open reads .st/workspace.json and returns a Workspace.
func Open(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configPath(abs))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is not a stint workspace (.st/workspace.json not found)", abs)
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf(".st/workspace.json is malformed: %w", err)
	}
	return &Workspace{Root: abs, Config: cfg}, nil
}

// FindRoot walks up from dir until a .st/workspace.json is found.
func FindRoot(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		if _, err := os.Stat(configPath(abs)); err == nil {
			return Open(abs)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil, fmt.Errorf("no st workspace found (.st/workspace.json not found in %s or any parent)", dir)
		}
		abs = parent
	}
}

// Path helpers — all data lives under <root>/.st/

func (ws *Workspace) StDir() string      { return filepath.Join(ws.Root, ".st") }
func (ws *Workspace) AgentsDir() string  { return filepath.Join(ws.Root, ".st", "agents") }
func (ws *Workspace) GoalsDir() string   { return filepath.Join(ws.Root, ".st", "goals") }
func (ws *Workspace) TasksDir() string   { return filepath.Join(ws.Root, ".st", "tasks") }
func (ws *Workspace) PendingDir() string { return filepath.Join(ws.Root, ".st", "tasks", "pending") }
func (ws *Workspace) RunningDir() string { return filepath.Join(ws.Root, ".st", "tasks", "running") }
func (ws *Workspace) DoneDir() string    { return filepath.Join(ws.Root, ".st", "tasks", "done") }
func (ws *Workspace) FailedDir() string  { return filepath.Join(ws.Root, ".st", "tasks", "failed") }
func (ws *Workspace) CancelledDir() string {
	return filepath.Join(ws.Root, ".st", "tasks", "cancelled")
}
func (ws *Workspace) BlockedDir() string    { return filepath.Join(ws.Root, ".st", "tasks", "blocked") }
func (ws *Workspace) WorkersDir() string    { return filepath.Join(ws.Root, ".st", "workers") }
func (ws *Workspace) WorktreesDir() string  { return filepath.Join(ws.Root, ".st", "worktrees") }
func (ws *Workspace) LogsDir() string       { return filepath.Join(ws.Root, ".st", "logs") }
func (ws *Workspace) HeartbeatsDir() string { return filepath.Join(ws.Root, ".st", "heartbeats") }

func (ws *Workspace) GoalPath(id string) string    { return filepath.Join(ws.GoalsDir(), id+".json") }
func (ws *Workspace) PendingPath(id string) string { return filepath.Join(ws.PendingDir(), id+".json") }
func (ws *Workspace) RunningPath(id string) string { return filepath.Join(ws.RunningDir(), id+".json") }
func (ws *Workspace) DonePath(id string) string    { return filepath.Join(ws.DoneDir(), id+".json") }
func (ws *Workspace) FailedPath(id string) string  { return filepath.Join(ws.FailedDir(), id+".json") }
func (ws *Workspace) BlockedPath(id string) string { return filepath.Join(ws.BlockedDir(), id+".json") }
func (ws *Workspace) CancelledPath(id string) string {
	return filepath.Join(ws.CancelledDir(), id+".json")
}
func (ws *Workspace) WorkerPath(id string) string    { return filepath.Join(ws.WorkersDir(), id+".json") }
func (ws *Workspace) HeartbeatPath(id string) string { return filepath.Join(ws.HeartbeatsDir(), id) }
func (ws *Workspace) SupervisorPIDPath() string {
	return filepath.Join(ws.Root, ".st", "supervisor.pid")
}
func (ws *Workspace) LogPath(id string) string      { return filepath.Join(ws.LogsDir(), id+".log") }
func (ws *Workspace) WorktreePath(id string) string { return filepath.Join(ws.WorktreesDir(), id) }

// StatusDir returns the task directory for a given status string.
func (ws *Workspace) StatusDir(status string) string {
	switch status {
	case "pending":
		return ws.PendingDir()
	case "running":
		return ws.RunningDir()
	case "done":
		return ws.DoneDir()
	case "failed":
		return ws.FailedDir()
	case "cancelled":
		return ws.CancelledDir()
	case "blocked":
		return ws.BlockedDir()
	default:
		return ""
	}
}

// SaveConfig writes the config back to .st/workspace.json.
func (ws *Workspace) SaveConfig() error {
	data, err := json.MarshalIndent(ws.Config, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(configPath(ws.Root), data)
}

// atomicWrite writes data to path atomically via temp file + rename.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
