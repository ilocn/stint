package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/stint/internal/gitutil"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/workspace"
)

// TestFilteredEnvRemovesKey verifies that filteredEnv strips the named key.
func TestFilteredEnvRemovesKey(t *testing.T) {
	t.Parallel()
	env := filteredEnv("CLAUDECODE")
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("filteredEnv should have removed CLAUDECODE, got %q", e)
		}
	}
}

// TestFilteredEnvPreservesOthers verifies other env vars are not removed.
func TestFilteredEnvPreservesOthers(t *testing.T) {
	t.Parallel()
	env := filteredEnv("CLAUDECODE")
	// PATH must always be in the environment.
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			return
		}
	}
	t.Error("filteredEnv should preserve PATH but it was missing")
}

// TestFilteredEnvMultipleKeys verifies multiple keys can be removed at once.
func TestFilteredEnvMultipleKeys(t *testing.T) {
	t.Parallel()
	env := filteredEnv("CLAUDECODE", "CLAUDE_SESSION_KEY")
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") || strings.HasPrefix(e, "CLAUDE_SESSION_KEY=") {
			t.Errorf("filteredEnv should have removed the key, got %q", e)
		}
	}
}

// TestBuildPlannerInteractiveArgsUsesDashP verifies that the interactive planner
// uses -p (not --system-prompt) so that Claude starts working immediately
// instead of opening a blank interactive session.
func TestBuildPlannerInteractiveArgsUsesDashP(t *testing.T) {
	t.Parallel()
	const testPrompt = "test planner prompt"
	args := buildPlannerInteractiveArgs(testPrompt)

	// First two args must be "-p" followed by the prompt.
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %d", len(args))
	}
	if args[0] != "-p" {
		t.Errorf("first arg: got %q, want %q", args[0], "-p")
	}
	if args[1] != testPrompt {
		t.Errorf("second arg (prompt): got %q, want %q", args[1], testPrompt)
	}

	// Must not use --system-prompt (which leaves Claude waiting for user input).
	for _, a := range args {
		if a == "--system-prompt" {
			t.Error("args must not contain --system-prompt; use -p instead")
		}
	}
}

// TestBuildPlannerInteractiveArgsIncludesMaxTurns verifies --max-turns is set
// so the interactive planner can run long enough to complete planning.
func TestBuildPlannerInteractiveArgsIncludesMaxTurns(t *testing.T) {
	t.Parallel()
	args := buildPlannerInteractiveArgs("prompt")

	for i, a := range args {
		if a == "--max-turns" {
			if i+1 >= len(args) {
				t.Fatal("--max-turns flag present but no value follows")
			}
			return // found and value present
		}
	}
	t.Error("args must contain --max-turns")
}

// TestBuildPlannerInteractiveArgsNoStreamJSON verifies that interactive mode
// does not add --output-format stream-json (which is for machine-readable mode).
func TestBuildPlannerInteractiveArgsNoStreamJSON(t *testing.T) {
	t.Parallel()
	args := buildPlannerInteractiveArgs("prompt")
	for _, a := range args {
		if a == "stream-json" {
			t.Error("interactive planner args must not include stream-json output format")
		}
	}
}

// spawnWS creates a workspace for spawn tests.
func spawnWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Init(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws
}

// withFakeClaude installs a fake 'claude' binary in a temp dir and prepends it
// to PATH using t.Setenv. Tests calling this must NOT call t.Parallel().
func withFakeClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	fakePath := filepath.Join(dir, "claude")
	// Fake claude exits immediately with success; it ignores all input.
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(fakePath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// TestSpawnWorkerClaudeNotFound verifies that SpawnWorker returns a "starting
// claude" error when the claude binary is not in PATH, covering the cmd.Start()
// failure path (logFile.Close + error return).
func TestSpawnWorkerClaudeNotFound(t *testing.T) {
	t.Setenv("PATH", "/does-not-exist-in-filesystem")
	ws := spawnWS(t)

	tk := &task.Task{
		ID:     "t-nofound",
		GoalID: "g-001",
		Title:  "no claude binary",
		Agent:  "default",
		Prompt: "do work",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err := SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail when claude is not in PATH")
	}
	if !strings.Contains(err.Error(), "starting claude") {
		t.Errorf("expected 'starting claude' error, got: %v", err)
	}
}

// TestSpawnPlannerBackgroundClaudeNotFound verifies SpawnPlannerBackground
// returns a "starting planner" error when the claude binary is absent, covering
// the cmd.Start() failure path.
func TestSpawnPlannerBackgroundClaudeNotFound(t *testing.T) {
	t.Setenv("PATH", "/does-not-exist-in-filesystem")
	ws := spawnWS(t)

	_, err := SpawnPlannerBackground(ws, "g-999", "test goal", nil, "w-999")
	if err == nil {
		t.Fatal("SpawnPlannerBackground should fail when claude is not in PATH")
	}
	if !strings.Contains(err.Error(), "starting planner") {
		t.Errorf("expected 'starting planner' error, got: %v", err)
	}
}

// TestSpawnWorkerUnknownRepo verifies that SpawnWorker returns an error when the
// task references a repo not registered in the workspace config.
func TestSpawnWorkerUnknownRepo(t *testing.T) {
	t.Parallel()
	ws := spawnWS(t)

	tk := &task.Task{
		ID:     "t-unknownrepo",
		GoalID: "g-001",
		Title:  "test",
		Agent:  "default",
		Prompt: "do work",
		Repo:   "does-not-exist",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err := SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail for unknown repo")
	}
	if !strings.Contains(err.Error(), "unknown repo") {
		t.Errorf("expected 'unknown repo' error, got: %v", err)
	}
}

// TestSpawnWorkerNoRepo verifies SpawnWorker succeeds (with fake claude) for a
// task that has no repo — it creates the worktree dir and starts the process.
func TestSpawnWorkerNoRepo(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	tk := &task.Task{
		ID:     "t-norepo",
		GoalID: "g-001",
		Title:  "test no repo",
		Agent:  "default",
		Prompt: "do work without a repo",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	proc, err := SpawnWorker(ws, tk, "w-001")
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnWorkerWithRepo verifies SpawnWorker with a real git worktree set up
// using a registered repo and a fake claude binary.
func TestSpawnWorkerWithRepo(t *testing.T) {
	withFakeClaude(t)

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	ws, err := workspace.Init(t.TempDir(), map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	tk := &task.Task{
		ID:         "t-withrepo",
		GoalID:     "g-001",
		Title:      "task with repo",
		Agent:      "default",
		Prompt:     "do work in repo",
		Repo:       "myrepo",
		BranchName: "st/tasks/t-withrepo",
		GoalBranch: "main",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	proc, err := SpawnWorker(ws, tk, "w-001")
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnWorkerWithRepoNoGoalBranch verifies SpawnWorker when GoalBranch is
// empty — it should call DefaultBranch to determine the base.
func TestSpawnWorkerWithRepoNoGoalBranch(t *testing.T) {
	withFakeClaude(t)

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	ws, err := workspace.Init(t.TempDir(), map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	tk := &task.Task{
		ID:         "t-nogoalbranch",
		GoalID:     "g-001",
		Title:      "task without goal branch",
		Agent:      "default",
		Prompt:     "do work",
		Repo:       "myrepo",
		BranchName: "st/tasks/t-nogoalbranch",
		GoalBranch: "", // empty — should fall back to DefaultBranch
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	proc, err := SpawnWorker(ws, tk, "w-001")
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnWorkerWithToolsAgent verifies SpawnWorker correctly builds args for
// an agent that has tools and disallowedTools configured.
func TestSpawnWorkerWithToolsAgent(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	// Write a custom agent with tools and disallowedTools.
	if err := Write(ws, "tooled", "desc", "You are tooled.", []string{"Read", "Bash"}, "sonnet", 20); err != nil {
		t.Fatalf("Write agent: %v", err)
	}
	// Add disallowedTools by writing raw markdown file.
	rawContent := "---\nname: restricted\ndescription: restricted agent\ntools: Read\ndisallowedTools: Write, Edit\nmodel: inherit\npermissionMode: acceptEdits\n---\n\nYou are restricted.\n"
	agentPath := filepath.Join(ws.AgentsDir(), "restricted.md")
	if err := os.WriteFile(agentPath, []byte(rawContent), 0644); err != nil {
		t.Fatalf("WriteFile agent: %v", err)
	}

	tk := &task.Task{
		ID:     "t-restricted",
		GoalID: "g-001",
		Title:  "restricted task",
		Agent:  "restricted",
		Prompt: "do restricted work",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	proc, err := SpawnWorker(ws, tk, "w-001")
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnWorkerPlanModeAgent verifies SpawnWorker builds correct args for an
// agent with permissionMode: plan (no --dangerously-skip-permissions flag).
func TestSpawnWorkerPlanModeAgent(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	tk := &task.Task{
		ID:     "t-plan",
		GoalID: "g-001",
		Title:  "plan task",
		Agent:  "review", // review agent has permissionMode: plan
		Prompt: "review the code",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	proc, err := SpawnWorker(ws, tk, "w-001")
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnPlannerBackground verifies that SpawnPlannerBackground starts a
// process and returns a non-nil process handle when claude is available.
func TestSpawnPlannerBackground(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	proc, err := SpawnPlannerBackground(ws, "g-001", "build a REST API", []string{"use Go"}, "w-001")
	if err != nil {
		t.Fatalf("SpawnPlannerBackground: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnPlannerBackgroundNoHints verifies SpawnPlannerBackground works when
// no hints are provided.
func TestSpawnPlannerBackgroundNoHints(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	proc, err := SpawnPlannerBackground(ws, "g-002", "simple goal", nil, "w-002")
	if err != nil {
		t.Fatalf("SpawnPlannerBackground: %v", err)
	}
	if proc == nil {
		t.Fatal("expected non-nil process")
	}
	proc.Wait() //nolint:errcheck
}

// TestSpawnPlannerInteractive verifies that SpawnPlannerInteractive runs and
// returns the process exit status when claude exits immediately.
func TestSpawnPlannerInteractive(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	// Fake claude exits 0 — SpawnPlannerInteractive should return nil.
	err := SpawnPlannerInteractive(ws, "g-001", "build something", []string{"use Go"})
	if err != nil {
		t.Fatalf("SpawnPlannerInteractive with exit-0 fake claude: %v", err)
	}
}

// TestSpawnPlannerInteractiveNoHints verifies SpawnPlannerInteractive works
// with no hints passed.
func TestSpawnPlannerInteractiveNoHints(t *testing.T) {
	withFakeClaude(t)
	ws := spawnWS(t)

	err := SpawnPlannerInteractive(ws, "g-003", "a simple goal", nil)
	if err != nil {
		t.Fatalf("SpawnPlannerInteractive no hints: %v", err)
	}
}

// TestSpawnPlannerInteractiveNonZeroExit verifies SpawnPlannerInteractive
// returns an error when claude exits non-zero.
func TestSpawnPlannerInteractiveNonZeroExit(t *testing.T) {
	// Must not call t.Parallel() — uses t.Setenv.
	dir := t.TempDir()
	fakePath := filepath.Join(dir, "claude")
	// Fake claude that exits with code 1.
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(fakePath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	ws := spawnWS(t)
	err := SpawnPlannerInteractive(ws, "g-exit1", "goal text", nil)
	if err == nil {
		t.Fatal("SpawnPlannerInteractive should return error when claude exits non-zero")
	}
}

// TestSpawnWorkerLoadAgentError verifies that SpawnWorker returns a
// "loading agent" error when the agent definition file cannot be read.
func TestSpawnWorkerLoadAgentError(t *testing.T) {
	t.Parallel()
	ws := spawnWS(t)

	// Create a directory at the agent file path so Get("broken-agent") fails.
	agentPath := filepath.Join(ws.AgentsDir(), "broken-agent.md")
	if err := os.MkdirAll(agentPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	tk := &task.Task{
		ID:     "t-badagent",
		GoalID: "g-001",
		Title:  "bad agent",
		Agent:  "broken-agent",
		Prompt: "do work",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err := SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail when agent cannot be loaded")
	}
	if !strings.Contains(err.Error(), "loading agent") {
		t.Errorf("expected 'loading agent' error, got: %v", err)
	}
}

// TestSpawnWorkerWorktreeError verifies that SpawnWorker returns a
// "creating worktree" error when WorktreeAdd fails (base branch doesn't exist).
func TestSpawnWorkerWorktreeError(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	ws, err := workspace.Init(t.TempDir(), map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	tk := &task.Task{
		ID:         "t-badworktree",
		GoalID:     "g-001",
		Title:      "bad worktree",
		Agent:      "default",
		Prompt:     "do work",
		Repo:       "myrepo",
		BranchName: "st/tasks/t-badworktree",
		GoalBranch: "nonexistent-base-branch", // does not exist → WorktreeAdd fails
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err = SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail when worktree creation fails")
	}
	if !strings.Contains(err.Error(), "creating worktree") {
		t.Errorf("expected 'creating worktree' error, got: %v", err)
	}
}

// TestSpawnWorkerMkdirError verifies that SpawnWorker returns an error when
// os.MkdirAll fails for the worktree path (task has no repo, worktrees dir blocked).
func TestSpawnWorkerMkdirError(t *testing.T) {
	t.Parallel()
	ws := spawnWS(t)

	// Replace the worktrees dir with a file so os.MkdirAll fails.
	wtsDir := ws.WorktreesDir()
	if err := os.RemoveAll(wtsDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(wtsDir, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tk := &task.Task{
		ID:     "t-mkdirfail",
		GoalID: "g-001",
		Title:  "mkdir fail",
		Agent:  "default",
		Prompt: "do work",
		// No Repo — takes the os.MkdirAll path.
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err := SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail when worktree dir cannot be created")
	}
}

// TestSpawnWorkerLogFileError verifies that SpawnWorker returns a
// "creating log file" error when the logs directory is replaced with a file.
func TestSpawnWorkerLogFileError(t *testing.T) {
	t.Parallel()
	ws := spawnWS(t)

	// Replace the logs dir with a file to cause os.Create to fail.
	logsDir := ws.LogsDir()
	if err := os.RemoveAll(logsDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(logsDir, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tk := &task.Task{
		ID:     "t-logfail",
		GoalID: "g-001",
		Title:  "log fail",
		Agent:  "default",
		Prompt: "do work",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	_, err := SpawnWorker(ws, tk, "w-001")
	if err == nil {
		t.Fatal("SpawnWorker should fail when log file cannot be created")
	}
	if !strings.Contains(err.Error(), "creating log file") {
		t.Errorf("expected 'creating log file' error, got: %v", err)
	}
}

// TestWorkerEnvInjectsBinaryDir verifies that workerEnv prepends the running
// binary's directory as the first PATH component.
// Must NOT call t.Parallel() — reads os.Executable() which is process-global.
func TestWorkerEnvInjectsBinaryDir(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	wantDir := filepath.Dir(exe)

	env := workerEnv()

	for _, e := range env {
		if !strings.HasPrefix(e, "PATH=") {
			continue
		}
		val := e[len("PATH="):]
		parts := strings.Split(val, string(os.PathListSeparator))
		if len(parts) == 0 || parts[0] == "" {
			t.Fatalf("PATH empty after injection: %q", val)
		}
		if parts[0] != wantDir {
			t.Errorf("first PATH component = %q, want %q", parts[0], wantDir)
		}
		return
	}
	t.Error("PATH entry not found in workerEnv output")
}

// TestWorkerEnvRemovesClaudeCode verifies CLAUDECODE is stripped by workerEnv.
// Must NOT call t.Parallel() — uses t.Setenv.
func TestWorkerEnvRemovesClaudeCode(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	env := workerEnv()
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Errorf("workerEnv should have removed CLAUDECODE, got %q", e)
		}
	}
}

// TestSpawnPlannerBackgroundLogFileError verifies SpawnPlannerBackground
// returns an error when the logs directory is replaced with a file.
func TestSpawnPlannerBackgroundLogFileError(t *testing.T) {
	t.Parallel()
	ws := spawnWS(t)

	// Replace the logs dir with a file to cause os.Create to fail.
	logsDir := ws.LogsDir()
	if err := os.RemoveAll(logsDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.WriteFile(logsDir, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := SpawnPlannerBackground(ws, "g-logfail", "test goal", nil, "w-001")
	if err == nil {
		t.Fatal("SpawnPlannerBackground should fail when log file cannot be created")
	}
}
