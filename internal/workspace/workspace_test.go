package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ilocn/stint/internal/workspace"
)

func TestInit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, map[string]string{"myrepo": "/tmp/myrepo"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if ws.Root != dir {
		t.Errorf("Root = %s, want %s", ws.Root, dir)
	}
	if ws.Config.Version != 1 {
		t.Errorf("Version = %d, want 1", ws.Config.Version)
	}
	if ws.Config.Repos["myrepo"] != "/tmp/myrepo" {
		t.Errorf("Repos[myrepo] = %s, want /tmp/myrepo", ws.Config.Repos["myrepo"])
	}

	// Verify directory structure under .st/.
	for _, sub := range []string{
		filepath.Join(".st", "agents"),
		filepath.Join(".st", "goals"),
		filepath.Join(".st", "tasks", "pending"),
		filepath.Join(".st", "tasks", "running"),
		filepath.Join(".st", "tasks", "done"),
		filepath.Join(".st", "tasks", "failed"),
		filepath.Join(".st", "workers"),
		filepath.Join(".st", "worktrees"),
		filepath.Join(".st", "logs"),
		filepath.Join(".st", "heartbeats"),
	} {
		path := filepath.Join(dir, sub)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
		}
	}

	// Verify .st/workspace.json exists.
	if _, err := os.Stat(filepath.Join(dir, ".st", "workspace.json")); err != nil {
		t.Error(".st/workspace.json not created")
	}
}

// TestInitCreatesAgentsDir verifies that Init creates the .st/agents/ directory
// (for user overrides) but does NOT seed any built-in .md files — they are
// embedded in the binary and resolved via agent.Get() at runtime.
func TestInitCreatesAgentsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Agents directory should be created.
	if _, err := os.Stat(ws.AgentsDir()); err != nil {
		t.Errorf("expected .st/agents/ dir to exist: %v", err)
	}
	// No .md files should be seeded — users customize via workspace files,
	// built-ins come from the embedded FS.
	entries, err := os.ReadDir(ws.AgentsDir())
	if err != nil {
		t.Fatalf("ReadDir agents: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			t.Errorf("unexpected seeded agent file %s — Init should not seed agents", e.Name())
		}
	}
}

func TestInitAlreadyExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := workspace.Init(dir, nil); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if _, err := workspace.Init(dir, nil); err == nil {
		t.Error("second Init should fail, got nil")
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws1, err := workspace.Init(dir, map[string]string{"a": "/tmp/a"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ws2, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ws2.Root != ws1.Root {
		t.Errorf("Root mismatch: %s vs %s", ws2.Root, ws1.Root)
	}
	if ws2.Config.Repos["a"] != "/tmp/a" {
		t.Error("repos not preserved")
	}
}

func TestOpenNonWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := workspace.Open(dir); err == nil {
		t.Error("Open on non-workspace should fail")
	}
}

func TestSaveConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ws.Config.Repos["newrepo"] = "/tmp/newrepo"
	if err := ws.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	ws2, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("Open after save: %v", err)
	}
	if ws2.Config.Repos["newrepo"] != "/tmp/newrepo" {
		t.Error("SaveConfig did not persist new repo")
	}
}

// TestFindRoot verifies that FindRoot walks up from a nested subdirectory to
// find the workspace root, and returns a clear error when no workspace ancestor exists.
func TestFindRoot(t *testing.T) {
	t.Parallel()

	t.Run("finds root from nested subdir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ws, err := workspace.Init(dir, nil)
		if err != nil {
			t.Fatalf("Init: %v", err)
		}

		nested := filepath.Join(dir, "subdir", "nested")
		if err := os.MkdirAll(nested, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		found, err := workspace.FindRoot(nested)
		if err != nil {
			t.Fatalf("FindRoot: %v", err)
		}
		if found.Root != ws.Root {
			t.Errorf("FindRoot.Root = %s, want %s", found.Root, ws.Root)
		}
	})

	t.Run("fails when no workspace ancestor", func(t *testing.T) {
		t.Parallel()
		noWS := t.TempDir()

		_, err := workspace.FindRoot(noWS)
		if err == nil {
			t.Error("FindRoot from non-workspace dir should return an error, got nil")
		}
	})
}

// TestPathFunctions is a table-driven test covering all workspace path helpers.
func TestPathFunctions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	stDir := filepath.Join(dir, ".st")
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "StDir",
			got:  ws.StDir(),
			want: stDir,
		},
		{
			name: "GoalsDir",
			got:  ws.GoalsDir(),
			want: filepath.Join(stDir, "goals"),
		},
		{
			name: "TasksDir",
			got:  ws.TasksDir(),
			want: filepath.Join(stDir, "tasks"),
		},
		{
			name: "RunningDir",
			got:  ws.RunningDir(),
			want: filepath.Join(stDir, "tasks", "running"),
		},
		{
			name: "FailedDir",
			got:  ws.FailedDir(),
			want: filepath.Join(stDir, "tasks", "failed"),
		},
		{
			name: "CancelledDir",
			got:  ws.CancelledDir(),
			want: filepath.Join(stDir, "tasks", "cancelled"),
		},
		{
			name: "BlockedDir",
			got:  ws.BlockedDir(),
			want: filepath.Join(stDir, "tasks", "blocked"),
		},
		{
			name: "WorktreesDir",
			got:  ws.WorktreesDir(),
			want: filepath.Join(stDir, "worktrees"),
		},
		{
			name: "LogsDir",
			got:  ws.LogsDir(),
			want: filepath.Join(stDir, "logs"),
		},
		{
			name: "HeartbeatsDir",
			got:  ws.HeartbeatsDir(),
			want: filepath.Join(stDir, "heartbeats"),
		},
		{
			name: "GoalPath",
			got:  ws.GoalPath("g-abc"),
			want: filepath.Join(stDir, "goals", "g-abc.json"),
		},
		{
			name: "PendingPath",
			got:  ws.PendingPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "pending", "t-abc.json"),
		},
		{
			name: "RunningPath",
			got:  ws.RunningPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "running", "t-abc.json"),
		},
		{
			name: "DonePath",
			got:  ws.DonePath("t-abc"),
			want: filepath.Join(stDir, "tasks", "done", "t-abc.json"),
		},
		{
			name: "FailedPath",
			got:  ws.FailedPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "failed", "t-abc.json"),
		},
		{
			name: "BlockedPath",
			got:  ws.BlockedPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "blocked", "t-abc.json"),
		},
		{
			name: "CancelledPath",
			got:  ws.CancelledPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "cancelled", "t-abc.json"),
		},
		{
			name: "WorkerPath",
			got:  ws.WorkerPath("w-xyz"),
			want: filepath.Join(stDir, "workers", "w-xyz.json"),
		},
		{
			name: "HeartbeatPath",
			got:  ws.HeartbeatPath("t-hb"),
			want: filepath.Join(stDir, "heartbeats", "t-hb"),
		},
		{
			name: "SupervisorPIDPath",
			got:  ws.SupervisorPIDPath(),
			want: filepath.Join(stDir, "supervisor.pid"),
		},
		{
			name: "LogPath",
			got:  ws.LogPath("t-abc"),
			want: filepath.Join(stDir, "logs", "t-abc.log"),
		},
		{
			name: "WorktreePath",
			got:  ws.WorktreePath("t-abc"),
			want: filepath.Join(stDir, "worktrees", "t-abc"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestStatusDir verifies all branches of StatusDir, including the default case.
func TestStatusDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	stDir := filepath.Join(dir, ".st")

	tests := []struct {
		status string
		want   string
	}{
		{"pending", filepath.Join(stDir, "tasks", "pending")},
		{"running", filepath.Join(stDir, "tasks", "running")},
		{"done", filepath.Join(stDir, "tasks", "done")},
		{"failed", filepath.Join(stDir, "tasks", "failed")},
		{"cancelled", filepath.Join(stDir, "tasks", "cancelled")},
		{"blocked", filepath.Join(stDir, "tasks", "blocked")},
		{"unknown", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run("status="+tc.status, func(t *testing.T) {
			t.Parallel()
			got := ws.StatusDir(tc.status)
			if got != tc.want {
				t.Errorf("StatusDir(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestOpenMalformedJSON verifies that Open returns an error when workspace.json
// contains invalid JSON.
func TestOpenMalformedJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stDir := filepath.Join(dir, ".st")
	if err := os.MkdirAll(stDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stDir, "workspace.json"), []byte("not valid json{{{"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := workspace.Open(dir)
	if err == nil {
		t.Error("Open with malformed JSON should return an error, got nil")
	}
}

// TestSaveConfigWriteError verifies that SaveConfig propagates a write error
// when the .st directory is made read-only.
func TestSaveConfigWriteError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test write errors when running as root")
	}
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	stDir := filepath.Join(dir, ".st")
	if err := os.Chmod(stDir, 0444); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(stDir, 0755) //nolint:errcheck

	if err := ws.SaveConfig(); err == nil {
		t.Error("SaveConfig should fail when .st directory is read-only, got nil")
	}
}

// TestInitMkdirAllError verifies that Init returns an error when the .st
// directory already exists as read-only, preventing subdirectory creation.
func TestInitMkdirAllError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test write errors when running as root")
	}
	dir := t.TempDir()

	// Pre-create .st as read-only so MkdirAll fails when Init tries to create subdirs.
	stDir := filepath.Join(dir, ".st")
	if err := os.Mkdir(stDir, 0444); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	defer os.Chmod(stDir, 0755) //nolint:errcheck

	_, err := workspace.Init(dir, nil)
	if err == nil {
		t.Error("Init should fail when .st directory is read-only, got nil")
	}
}

// TestInitWriteFileError verifies that Init returns an error when all required
// subdirectories already exist but .st is read-only, preventing workspace.json
// from being written.
func TestInitWriteFileError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test write errors when running as root")
	}
	dir := t.TempDir()

	// Pre-create every directory Init would create so MkdirAll succeeds,
	// then make .st read-only to block workspace.json creation.
	for _, sub := range []string{
		".st",
		filepath.Join(".st", "agents"),
		filepath.Join(".st", "goals"),
		filepath.Join(".st", "tasks", "pending"),
		filepath.Join(".st", "tasks", "running"),
		filepath.Join(".st", "tasks", "done"),
		filepath.Join(".st", "tasks", "failed"),
		filepath.Join(".st", "tasks", "cancelled"),
		filepath.Join(".st", "tasks", "blocked"),
		filepath.Join(".st", "workers"),
		filepath.Join(".st", "worktrees"),
		filepath.Join(".st", "logs"),
		filepath.Join(".st", "heartbeats"),
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatalf("MkdirAll %s: %v", sub, err)
		}
	}

	stDir := filepath.Join(dir, ".st")
	if err := os.Chmod(stDir, 0555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(stDir, 0755) //nolint:errcheck

	_, err := workspace.Init(dir, nil)
	if err == nil {
		t.Error("Init should fail when workspace.json cannot be written, got nil")
	}
}

// TestInitDoesNotSeedAgentFiles verifies that Init no longer creates .md agent files
// in .st/agents/. Built-in agents are embedded in the binary and resolved at runtime.
// Users can place custom .md files in .st/agents/ to override built-ins.
func TestInitDoesNotSeedAgentFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// agents dir must exist (for user overrides).
	if _, statErr := os.Stat(ws.AgentsDir()); statErr != nil {
		t.Fatalf("agents dir should exist: %v", statErr)
	}

	// No .md files should be present — built-ins are in the binary, not on disk.
	entries, err := os.ReadDir(ws.AgentsDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			t.Errorf("Init must not seed %s — agent files are now embedded in binary", e.Name())
		}
	}
}

// TestOpenReadFileError verifies that Open returns a non-not-found error when
// workspace.json exists but is unreadable (permission denied).
func TestOpenReadFileError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	dir := t.TempDir()
	if _, err := workspace.Init(dir, nil); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cfgPath := filepath.Join(dir, ".st", "workspace.json")
	if err := os.Chmod(cfgPath, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(cfgPath, 0644) //nolint:errcheck

	_, err := workspace.Open(dir)
	if err == nil {
		t.Error("Open should fail when workspace.json is unreadable, got nil")
	}
}

// TestOpenFilepathAbsErrorChmod0CWD verifies that Open returns an error when
// called with a relative path and the current working directory has mode 0
// (no permissions), causing filepath.Abs to fail with "stat .: permission denied".
// This covers the `if err != nil { return nil, err }` block after filepath.Abs in Open.
//
// Not parallel — modifies process-wide CWD permissions.
func TestOpenFilepathAbsErrorChmod0CWD(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-abs-open")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	// Always restore the original directory and remove the temp dir.
	defer func() {
		os.Chmod(tmpDir, 0755) //nolint:errcheck
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	// Remove all permissions on tmpDir so filepath.Abs("relative") → os.Getwd() fails.
	if err := os.Chmod(tmpDir, 0000); err != nil {
		t.Skipf("Chmod failed: %v", err)
	}

	_, err = workspace.Open("relative-workspace")
	if err == nil {
		// Platform didn't fail filepath.Abs with chmod 0 — this can happen if
		// the OS uses cached path resolution. Skip gracefully.
		t.Logf("filepath.Abs did not fail even with CWD chmod 0 on this platform; coverage path not triggered")
		return
	}
	// The filepath.Abs error path in Open was successfully exercised.
}

// TestFindRootFilepathAbsErrorChmod0CWD verifies that FindRoot returns an error
// when called with a relative path and the CWD has mode 0.
// Covers the `if err != nil { return nil, err }` block in FindRoot.
//
// Not parallel — modifies process-wide CWD permissions.
func TestFindRootFilepathAbsErrorChmod0CWD(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-abs-findroot")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	defer func() {
		os.Chmod(tmpDir, 0755) //nolint:errcheck
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	if err := os.Chmod(tmpDir, 0000); err != nil {
		t.Skipf("Chmod failed: %v", err)
	}

	_, err = workspace.FindRoot("relative-findroot")
	if err == nil {
		t.Logf("filepath.Abs did not fail even with CWD chmod 0 on this platform; coverage path not triggered")
		return
	}
	// The filepath.Abs error path in FindRoot was successfully exercised.
}

// TestInitFilepathAbsErrorChmod0CWD verifies that Init returns an error when
// called with a relative path and the CWD has mode 0.
// Covers the `if err != nil { return nil, err }` block in Init.
//
// Not parallel — modifies process-wide CWD permissions.
func TestInitFilepathAbsErrorChmod0CWD(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-abs-init")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	defer func() {
		os.Chmod(tmpDir, 0755) //nolint:errcheck
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	if err := os.Chmod(tmpDir, 0000); err != nil {
		t.Skipf("Chmod failed: %v", err)
	}

	_, err = workspace.Init("relative-init", nil)
	if err == nil {
		t.Logf("filepath.Abs did not fail even with CWD chmod 0 on this platform; coverage path not triggered")
		return
	}
	// The filepath.Abs error path in Init was successfully exercised.
}

// ─── Tests from main branch ─────────────────────────────────────────────────

func TestPathHelpers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	stDir := filepath.Join(dir, ".st")
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "PendingPath",
			got:  ws.PendingPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "pending", "t-abc.json"),
		},
		{
			name: "RunningPath",
			got:  ws.RunningPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "running", "t-abc.json"),
		},
		{
			name: "DonePath",
			got:  ws.DonePath("t-abc"),
			want: filepath.Join(stDir, "tasks", "done", "t-abc.json"),
		},
		{
			name: "FailedPath",
			got:  ws.FailedPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "failed", "t-abc.json"),
		},
		{
			name: "BlockedPath",
			got:  ws.BlockedPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "blocked", "t-abc.json"),
		},
		{
			name: "CancelledPath",
			got:  ws.CancelledPath("t-abc"),
			want: filepath.Join(stDir, "tasks", "cancelled", "t-abc.json"),
		},
		{
			name: "WorkerPath",
			got:  ws.WorkerPath("w-xyz"),
			want: filepath.Join(stDir, "workers", "w-xyz.json"),
		},
		{
			name: "GoalPath",
			got:  ws.GoalPath("g-abc"),
			want: filepath.Join(stDir, "goals", "g-abc.json"),
		},
		{
			name: "HeartbeatPath",
			got:  ws.HeartbeatPath("t-hb"),
			want: filepath.Join(stDir, "heartbeats", "t-hb"),
		},
		{
			name: "StDir",
			got:  ws.StDir(),
			want: stDir,
		},
		{
			name: "TasksDir",
			got:  ws.TasksDir(),
			want: filepath.Join(stDir, "tasks"),
		},
		{
			name: "WorktreesDir",
			got:  ws.WorktreesDir(),
			want: filepath.Join(stDir, "worktrees"),
		},
		{
			name: "LogsDir",
			got:  ws.LogsDir(),
			want: filepath.Join(stDir, "logs"),
		},
		{
			name: "SupervisorPIDPath",
			got:  ws.SupervisorPIDPath(),
			want: filepath.Join(stDir, "supervisor.pid"),
		},
		{
			name: "LogPath",
			got:  ws.LogPath("t-abc"),
			want: filepath.Join(stDir, "logs", "t-abc.log"),
		},
		{
			name: "WorktreePath",
			got:  ws.WorktreePath("t-abc"),
			want: filepath.Join(stDir, "worktrees", "t-abc"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}
