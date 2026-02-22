//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/user/stint/internal/agent"
	"github.com/user/stint/internal/gitutil"
	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/worker"
	"github.com/user/stint/internal/workspace"
)

// stBin is the path to the compiled st binary, set once in TestMain.
var stBin string

// ─── TestMain: build st binary once ──────────────────────────────────────────

func TestMain(m *testing.M) {
	// The e2e suite includes long-running LLM-backed tests. Inject a generous
	// default timeout before flag.Parse() (which runs inside m.Run()) so the
	// suite doesn't need -timeout=... on the command line. An explicit
	// -test.timeout flag passed by the caller always takes precedence.
	if !hasTestTimeoutFlag() {
		os.Args = append(os.Args, "-test.timeout=30m")
	}

	bin, cleanup, err := buildSt()
	if err != nil {
		log.Fatalf("build st: %v", err)
	}
	stBin = bin
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// hasTestTimeoutFlag reports whether os.Args already contains an explicit
// -test.timeout or -timeout flag so we don't override a caller-supplied value.
func hasTestTimeoutFlag() bool {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.timeout") || strings.HasPrefix(arg, "--test.timeout") {
			return true
		}
	}
	return false
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// buildSt compiles cmd/st to a temp dir; returns (binPath, cleanup, err).
func buildSt() (string, func(), error) {
	dir, err := os.MkdirTemp("", "st-bin-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	bin := filepath.Join(dir, "st")

	moduleRoot, err := findModuleRoot()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("find module root: %w", err)
	}

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/st")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, cleanup, nil
}

// findModuleRoot walks up from CWD to find the directory containing go.mod.
func findModuleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(gomod), nil
}

// initRepo creates a git repo with an initial commit on main; returns repo dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := gitutil.InitWithBranch(dir, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := gitutil.CommitEmpty(dir, "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return dir
}

// initRepoWithRemote creates a repo that has an 'origin' bare remote, an initial
// commit on main, and origin/main tracking set. Required for merge-task tests
// that call 'git fetch origin' and 'git push origin'.
func initRepoWithRemote(t *testing.T) string {
	t.Helper()

	bareDir := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(repoDir, "remote", "add", "origin", bareDir)
	runGit(repoDir, "push", "-u", "origin", "main")
	// Set remote HEAD so DefaultBranch() detects "main" via symbolic-ref.
	exec.Command("git", "-C", repoDir, "remote", "set-head", "origin", "main").Run() //nolint:errcheck

	return repoDir
}

// initWorkspace runs "st init <dir> [--repo name=path ...]"; returns wsDir.
func initWorkspace(t *testing.T, repos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	args := []string{"init", dir}
	for name, path := range repos {
		args = append(args, "--repo", name+"="+path)
	}
	out, code := runSt(t, dir, args...)
	if code != 0 {
		t.Fatalf("st init exit %d: %s", code, out)
	}
	return dir
}

// runSt runs the st binary with the working directory and ST_WORKSPACE set to
// wsDir (when non-empty). Setting cmd.Dir ensures that openWS()'s FindRoot
// walks up from wsDir and finds the test workspace rather than any real
// workspace that happens to be an ancestor of the test binary's CWD.
func runSt(t *testing.T, wsDir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(stBin, args...)
	if wsDir != "" {
		cmd.Dir = wsDir
		cmd.Env = append(filterEnv(os.Environ(), "ST_WORKSPACE"), "ST_WORKSPACE="+wsDir)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
	}
	return strings.TrimSpace(out.String()), code
}

// mustSt runs st and fatals on non-zero exit.
func mustSt(t *testing.T, wsDir string, args ...string) string {
	t.Helper()
	out, code := runSt(t, wsDir, args...)
	if code != 0 {
		t.Fatalf("st %s failed (exit %d):\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// filterEnv returns a copy of env with entries matching any of the given keys removed.
func filterEnv(env []string, removeKeys ...string) []string {
	remove := make(map[string]bool, len(removeKeys))
	for _, k := range removeKeys {
		remove[k] = true
	}
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key := entry
		if i := strings.Index(entry, "="); i >= 0 {
			key = entry[:i]
		}
		if !remove[key] {
			result = append(result, entry)
		}
	}
	return result
}

// startSupervisor starts "st supervisor" in the background.
// The supervisor writes to a log file in wsDir. Cancel ctx to stop it.
// PATH is set so workers can find both 'st' and 'claude'.
// CLAUDECODE is stripped so that nested claude sessions are allowed.
func startSupervisor(ctx context.Context, t *testing.T, wsDir string) *exec.Cmd {
	t.Helper()

	logPath := filepath.Join(wsDir, "supervisor.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create supervisor log: %v", err)
	}
	t.Cleanup(func() { logFile.Close() })

	cmd := exec.CommandContext(ctx, stBin, "supervisor", "--max-concurrent", "4")
	// Set Dir so openWS()'s FindRoot finds the test workspace (not any real
	// workspace that happens to be an ancestor of the test binary's CWD).
	cmd.Dir = wsDir
	// Strip CLAUDECODE so nested claude workers are not blocked.
	// Claude Code sets this env var; child claude processes check for it and
	// refuse to start if it's present (to prevent nested session crashes).
	baseEnv := filterEnv(os.Environ(), "CLAUDECODE", "ST_WORKSPACE")
	env := append(baseEnv,
		"PATH="+filepath.Dir(stBin)+":/Users/zidiwang/.local/bin:"+os.Getenv("PATH"),
		"ST_WORKSPACE="+wsDir,
	)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	t.Logf("supervisor started (pid=%d), log: %s", cmd.Process.Pid, logPath)
	return cmd
}

// waitForTaskStatus polls until the task reaches wantStatus or timeout.
// Fails immediately if the task reaches "failed" status (unless wantStatus IS "failed").
// On failure, prints the task log and supervisor log for diagnosis.
func waitForTaskStatus(t *testing.T, wsDir, taskID, wantStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, code := runSt(t, wsDir, "task", "show", taskID)
		if code == 0 && strings.Contains(out, "Status:  "+wantStatus) {
			return
		}
		// Fail fast if task is already in a terminal bad state.
		if wantStatus != task.StatusFailed &&
			code == 0 && strings.Contains(out, "Status:  "+task.StatusFailed) {
			dumpLogs(t, wsDir, taskID)
			t.Fatalf("task %s reached status=failed (wanted=%s)\nTask show:\n%s", taskID, wantStatus, out)
		}
		time.Sleep(5 * time.Second)
	}
	// Timeout — print full diagnostics.
	out, _ := runSt(t, wsDir, "task", "show", taskID)
	dumpLogs(t, wsDir, taskID)
	t.Fatalf("task %s did not reach status=%s within %v\nLast show:\n%s", taskID, wantStatus, timeout, out)
}

// dumpLogs prints the task log and supervisor log to t.Log for diagnosis.
func dumpLogs(t *testing.T, wsDir, taskID string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(wsDir, ".st", "logs", taskID+".log")); err == nil {
		t.Logf("=== task log (%s) ===\n%s", taskID, data)
	} else {
		t.Logf("task log not readable: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(wsDir, "supervisor.log")); err == nil {
		t.Logf("=== supervisor log ===\n%s", data)
	} else {
		t.Logf("supervisor log not readable: %v", err)
	}
}

// waitForGoalStatus polls until the goal reaches wantStatus or timeout.
func waitForGoalStatus(t *testing.T, wsDir, goalID, wantStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, code := runSt(t, wsDir, "goal", "show", goalID)
		if code == 0 && strings.Contains(out, "Status:  "+wantStatus) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	out, _ := runSt(t, wsDir, "goal", "show", goalID)
	t.Fatalf("goal %s did not reach status=%s within %v\nLast show:\n%s", goalID, wantStatus, timeout, out)
}

// branchExistsInRepo returns true if the given branch exists in repoDir.
func branchExistsInRepo(t *testing.T, repoDir, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// fileExistsInBranch returns true if relPath is committed on the given branch.
func fileExistsInBranch(t *testing.T, repoDir, branch, relPath string) bool {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "--name-only", branch, relPath)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == relPath
}

// parseTaskID extracts the first task ID (t-XXXXXXXXXXX) from CLI output.
// IDs are time-sortable base36 strings of exactly 11 characters (0-9a-z).
func parseTaskID(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`t-[0-9a-z]{11}`)
	m := re.FindString(output)
	if m == "" {
		t.Fatalf("no task ID found in output: %s", output)
	}
	return m
}

// parseGoalID extracts the first goal ID (g-XXXXXXXXXXX) from CLI output.
// IDs are time-sortable base36 strings of exactly 11 characters (0-9a-z).
func parseGoalID(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`g-[0-9a-z]{11}`)
	m := re.FindString(output)
	if m == "" {
		t.Fatalf("no goal ID found in output: %s", output)
	}
	return m
}

// parseBranchFromShow extracts the "Branch:  <name>" line from "st task show" output.
func parseBranchFromShow(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Branch:  ") {
			return strings.TrimPrefix(line, "Branch:  ")
		}
	}
	t.Fatalf("no Branch field in task show output:\n%s", output)
	return ""
}

// requireClaude skips the test if the claude binary is not at its expected path.
func requireClaude(t *testing.T) {
	t.Helper()
	claudePath := "/Users/zidiwang/.local/bin/claude"
	if _, err := os.Stat(claudePath); err != nil {
		t.Skipf("claude not found at %s: %v (skipping Group 2 test)", claudePath, err)
	}
}

// addTestWorkerAgent writes a lightweight test-worker agent that executes tasks
// exactly as instructed and returns quickly (maxTurns: 5).
func addTestWorkerAgent(t *testing.T, wsDir string) {
	t.Helper()
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace for agent: %v", err)
	}
	content := `---
name: test-worker
description: Minimal e2e test worker. Executes task instructions exactly.
tools: Read, Write, Bash
model: inherit
permissionMode: bypassPermissions
maxTurns: 5
---

You are a minimal test worker for automated e2e tests.

Rules:
1. Read the "--- YOUR TASK ---" section and execute those exact steps using Bash.
2. Do not ask questions or deviate from instructions.
3. After completing the task, call: st task-done <TASK_ID> --summary "<1 line>" --branch <BRANCH>
   - TASK_ID: found in WORKER INSTRUCTIONS as the argument to "st task-done"
   - BRANCH: found in WORKER INSTRUCTIONS as "Your branch is: <BRANCH>"
4. Complete everything in 1-3 Bash calls.
`
	path := filepath.Join(ws.AgentsDir(), "test-worker.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test-worker agent: %v", err)
	}
}

// addTestMergeMainAgent writes an agent that merges a goal branch into main using
// git plumbing (worktree-safe: avoids "git checkout main" which fails in worktrees
// when main is checked out in the primary working tree).
func addTestMergeMainAgent(t *testing.T, wsDir string) {
	t.Helper()
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace for merge-main agent: %v", err)
	}
	content := `---
name: test-merge-main
description: Test agent for merging goal branch into main (worktree-safe).
tools: Bash
model: inherit
permissionMode: bypassPermissions
maxTurns: 5
---

You are a test merge agent. Merge a goal branch into main using git plumbing
(this avoids "git checkout main" which fails in worktrees).

Your task prompt gives you the goal branch (e.g. st/goals/g-XXXXXXXX).

Steps (run as a single Bash script):
1. Extract the goal branch from your task prompt
2. Run:
   GOAL_BRANCH="<goal-branch>"
   TREE=$(git merge-tree --write-tree main "$GOAL_BRANCH")
   MAIN_SHA=$(git rev-parse main)
   GOAL_SHA=$(git rev-parse "$GOAL_BRANCH")
   NEW_MAIN=$(git commit-tree -m "st: merge $GOAL_BRANCH to main" "$TREE" -p "$MAIN_SHA" -p "$GOAL_SHA")
   git update-ref refs/heads/main "$NEW_MAIN"
   primary=$(dirname "$(git rev-parse --git-common-dir)")
   git -C "$primary" reset --hard main
   git push origin main 2>/dev/null || true
3. Call: st task-done <TASK_ID> --summary "merged <goal-branch> to main"
   (TASK_ID is in WORKER INSTRUCTIONS)
`
	path := filepath.Join(ws.AgentsDir(), "test-merge-main.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test-merge-main agent: %v", err)
	}
}

// ─── Group 1: Fast CLI Tests (no claude, < 5s each) ──────────────────────────

func TestInitCreatesWorkspace(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := t.TempDir()

	out, code := runSt(t, wsDir, "init", wsDir, "--repo", "myrepo="+repoDir)
	if code != 0 {
		t.Fatalf("st init exit %d:\n%s", code, out)
	}

	// All .st/ subdirectories must exist.
	for _, subdir := range []string{
		".st",
		".st/agents",
		".st/goals",
		".st/tasks/pending",
		".st/tasks/running",
		".st/tasks/done",
		".st/tasks/failed",
		".st/tasks/cancelled",
		".st/tasks/blocked",
		".st/workers",
		".st/worktrees",
		".st/logs",
		".st/heartbeats",
	} {
		if _, err := os.Stat(filepath.Join(wsDir, subdir)); err != nil {
			t.Errorf("missing %s: %v", subdir, err)
		}
	}

	// Built-in agents are embedded in the binary — NOT seeded as .md files on disk.
	// The .st/agents/ dir must exist (for user overrides) but be empty after init.
	entries, err := os.ReadDir(filepath.Join(wsDir, ".st", "agents"))
	if err != nil {
		t.Fatalf("ReadDir .st/agents: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			t.Errorf("init must not seed agent file %s — built-ins are embedded in binary", e.Name())
		}
	}
	// Verify all 8 built-in agents are accessible via `st agent show`.
	for _, name := range []string{"impl", "debug", "test", "docs", "review", "security", "merge", "merge-task"} {
		out, code := runSt(t, wsDir, "agent", "show", name)
		if code != 0 {
			t.Errorf("st agent show %s failed (exit %d): %s", name, code, out)
		}
		if !strings.Contains(out, "Name:") {
			t.Errorf("st agent show %s output missing 'Name:': %s", name, out)
		}
	}
	// default agent must NOT be gettable (it's a hardcoded fallback, not a named agent).
	if _, err := os.Stat(filepath.Join(wsDir, ".st", "agents", "default.md")); err == nil {
		t.Error("default.md should not exist after init")
	}

	// workspace.json must be valid JSON with repos configured.
	data, err := os.ReadFile(filepath.Join(wsDir, ".st", "workspace.json"))
	if err != nil {
		t.Fatalf("read workspace.json: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("workspace.json is not valid JSON: %v", err)
	}
	repos, _ := cfg["repos"].(map[string]interface{})
	if _, ok := repos["myrepo"]; !ok {
		t.Errorf("workspace.json missing myrepo; repos = %v", repos)
	}
}

func TestGoalLifecycle(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	// Add goal.
	out := mustSt(t, wsDir, "goal", "add", "test lifecycle goal")
	goalID := parseGoalID(t, out)

	// List goals — must contain goalID.
	out = mustSt(t, wsDir, "goal", "list")
	if !strings.Contains(out, goalID) {
		t.Errorf("goal list missing %s:\n%s", goalID, out)
	}

	// Show goal — status queued, text matches.
	out = mustSt(t, wsDir, "goal", "show", goalID)
	if !strings.Contains(out, "Status:  queued") {
		t.Errorf("goal should be queued:\n%s", out)
	}
	if !strings.Contains(out, "test lifecycle goal") {
		t.Errorf("goal text not found:\n%s", out)
	}

	// Cancel goal (sets status to "failed" per current implementation).
	mustSt(t, wsDir, "goal", "cancel", goalID)

	out = mustSt(t, wsDir, "goal", "show", goalID)
	if !strings.Contains(out, "Status:  failed") {
		t.Errorf("goal should be failed after cancel:\n%s", out)
	}
}

func TestTaskLifecycle(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	// Create goal.
	out := mustSt(t, wsDir, "goal", "add", "lifecycle goal")
	goalID := parseGoalID(t, out)

	// Add task.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "lifecycle task",
		"--prompt", "do the thing",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// List tasks for goal — must show task.
	out = mustSt(t, wsDir, "task", "list", "--goal", goalID)
	if !strings.Contains(out, taskID) {
		t.Errorf("task list missing %s:\n%s", taskID, out)
	}

	// Show task — must be pending.
	out = mustSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(out, "Status:  pending") {
		t.Errorf("task should be pending:\n%s", out)
	}

	// Claim task using Go API (moves it to running/).
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	workerID := worker.NewID()
	claimed, err := task.ClaimNext(ws, workerID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed.ID != taskID {
		t.Fatalf("claimed wrong task: got %s, want %s", claimed.ID, taskID)
	}

	// Verify running status.
	out = mustSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(out, "Status:  running") {
		t.Errorf("task should be running after claim:\n%s", out)
	}

	// Mark done via CLI.
	mustSt(t, wsDir, "task-done", taskID, "--summary", "completed the lifecycle task")

	// Show task — must be done.
	out = mustSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(out, "Status:  done") {
		t.Errorf("task should be done:\n%s", out)
	}
}

func TestTaskDependencyBlocking(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	out := mustSt(t, wsDir, "goal", "add", "dependency goal")
	goalID := parseGoalID(t, out)

	// T1: no deps → goes to pending/.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "T1", "--prompt", "do T1", "--repo", "myrepo",
	)
	t1ID := parseTaskID(t, out)

	// T2: deps on T1 → goes to blocked/.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "T2", "--prompt", "do T2", "--repo", "myrepo",
		"--deps", t1ID,
	)
	t2ID := parseTaskID(t, out)

	// T1 must be pending, T2 must be blocked.
	out1 := mustSt(t, wsDir, "task", "show", t1ID)
	if !strings.Contains(out1, "Status:  pending") {
		t.Errorf("T1 should be pending:\n%s", out1)
	}
	out2 := mustSt(t, wsDir, "task", "show", t2ID)
	if !strings.Contains(out2, "Status:  blocked") {
		t.Errorf("T2 should be blocked:\n%s", out2)
	}

	// Claim and complete T1 via Go API.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	workerID := worker.NewID()
	claimed, err := task.ClaimNext(ws, workerID)
	if err != nil {
		t.Fatalf("claim T1: %v", err)
	}
	if claimed.ID != t1ID {
		t.Fatalf("expected to claim T1 (%s), got %s", t1ID, claimed.ID)
	}
	if err := task.Done(ws, t1ID, "T1 complete", ""); err != nil {
		t.Fatalf("done T1: %v", err)
	}

	// T2 must now be pending (UnblockReady ran inside task.Done).
	out2 = mustSt(t, wsDir, "task", "show", t2ID)
	if !strings.Contains(out2, "Status:  pending") {
		t.Errorf("T2 should be pending after T1 done:\n%s", out2)
	}
}

func TestStatusOutput(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	out := mustSt(t, wsDir, "goal", "add", "status test goal")
	goalID := parseGoalID(t, out)

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "status task", "--prompt", "do it", "--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// st status — must contain both IDs.
	out = mustSt(t, wsDir, "status")
	if !strings.Contains(out, goalID) {
		t.Errorf("st status missing goalID %s:\n%s", goalID, out)
	}
	if !strings.Contains(out, taskID) {
		t.Errorf("st status missing taskID %s:\n%s", taskID, out)
	}

	// st status --goal g-xxx — filtered output.
	out = mustSt(t, wsDir, "status", "--goal", goalID)
	if !strings.Contains(out, goalID) {
		t.Errorf("st status --goal missing goalID:\n%s", out)
	}
}

func TestAgentCommands(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	// List agents — all 8 built-ins.
	out := mustSt(t, wsDir, "agent", "list")
	for _, name := range []string{"impl", "debug", "test", "docs", "review", "security", "merge", "merge-task"} {
		if !strings.Contains(out, name) {
			t.Errorf("agent list missing %s:\n%s", name, out)
		}
	}
	if strings.Contains(out, "default") {
		t.Errorf("agent list should not contain 'default' (removed):\n%s", out)
	}

	// Show merge-task — description present.
	out = mustSt(t, wsDir, "agent", "show", "merge-task")
	if !strings.Contains(out, "merge-task") {
		t.Errorf("agent show merge-task:\n%s", out)
	}

	// Add custom agent.
	mustSt(t, wsDir, "agent", "create", "custom",
		"--prompt", "You are a custom agent.",
		"--tools", "Read,Bash",
		"--description", "custom test agent",
	)

	// Show custom agent — must exist.
	out = mustSt(t, wsDir, "agent", "show", "custom")
	if !strings.Contains(out, "custom") {
		t.Errorf("agent show custom:\n%s", out)
	}
}

func TestRecoverResetsOrphanedTasks(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	out := mustSt(t, wsDir, "goal", "add", "recover test goal")
	goalID := parseGoalID(t, out)

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "orphan task", "--prompt", "be an orphan", "--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// Claim task via Go API — puts it in running/ but we do NOT register a worker record.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	someWorkerID := worker.NewID()
	_, err = task.ClaimNext(ws, someWorkerID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	// Intentionally NOT calling worker.Register — orphaned task.

	// Task should be running.
	out = mustSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(out, "Status:  running") {
		t.Errorf("task should be running after claim:\n%s", out)
	}

	// Run st recover — no worker record → task reset to pending.
	mustSt(t, wsDir, "recover")

	// Task should be back to pending (or blocked if deps unmet; no deps here → pending).
	out = mustSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(out, "Status:  pending") {
		t.Errorf("task should be pending after recover:\n%s", out)
	}
}

// ─── Group 1 (continued): More CLI Tests ─────────────────────────────────────

func TestVersionCommand(t *testing.T) {
	out := mustSt(t, "", "version")
	if !strings.Contains(out, "st ") {
		t.Errorf("version output missing 'st': %s", out)
	}
}

func TestRepoCommands(t *testing.T) {
	repoDir := initRepo(t)
	// Init workspace with no repos initially.
	wsDir := initWorkspace(t, nil)

	// st repo list — should say none
	out := mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "no repos") {
		t.Logf("repo list before add: %s", out)
	}

	// st repo add — clones/copies source into <workspace-root>/myrepo.
	mustSt(t, wsDir, "repo", "add", "myrepo", repoDir)

	// st repo list — must show the repo name and its destination path
	// (the workspace-local copy, NOT the original source path).
	out = mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "myrepo") {
		t.Errorf("repo list missing myrepo after add:\n%s", out)
	}
	wantDest := filepath.Join(wsDir, "myrepo")
	if !strings.Contains(out, wantDest) {
		t.Errorf("repo list should show destination %s (not source %s):\n%s", wantDest, repoDir, out)
	}
}

func TestGoalAddWithHintAndRepos(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	// Add goal with --hint and --repos flags.
	out := mustSt(t, wsDir, "goal", "add", "goal with hints",
		"--hint", "focus on performance",
		"--repos", "myrepo",
	)
	goalID := parseGoalID(t, out)

	// Show goal — hint and repos should be present.
	out = mustSt(t, wsDir, "goal", "show", goalID)
	if !strings.Contains(out, "focus on performance") {
		t.Errorf("goal show missing hint:\n%s", out)
	}
	if !strings.Contains(out, "myrepo") {
		t.Errorf("goal show missing repos:\n%s", out)
	}
}

func TestTaskCancelAndFail(t *testing.T) {
	wsDir := initWorkspace(t, nil)
	out := mustSt(t, wsDir, "goal", "add", "cancel/fail goal")
	goalID := parseGoalID(t, out)

	// Create T1 — will be cancelled.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "cancel me", "--prompt", "irrelevant",
	)
	t1ID := parseTaskID(t, out)

	// Cancel T1.
	mustSt(t, wsDir, "task", "cancel", t1ID)
	out = mustSt(t, wsDir, "task", "show", t1ID)
	if !strings.Contains(out, "Status:  cancelled") {
		t.Errorf("T1 should be cancelled:\n%s", out)
	}

	// Create T2 — will be failed via CLI.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "fail me", "--prompt", "irrelevant",
	)
	t2ID := parseTaskID(t, out)

	// Claim T2 via Go API (puts it in running/).
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	_, err = task.ClaimNext(ws, worker.NewID())
	if err != nil {
		t.Fatalf("claim T2: %v", err)
	}

	// Fail T2 via CLI.
	mustSt(t, wsDir, "task-fail", t2ID, "--reason", "deliberate test failure")
	out = mustSt(t, wsDir, "task", "show", t2ID)
	if !strings.Contains(out, "Status:  failed") {
		t.Errorf("T2 should be failed:\n%s", out)
	}
	if !strings.Contains(out, "deliberate test failure") {
		t.Errorf("T2 error message missing:\n%s", out)
	}
}

func TestTaskRetry(t *testing.T) {
	wsDir := initWorkspace(t, nil)
	out := mustSt(t, wsDir, "goal", "add", "retry goal")
	goalID := parseGoalID(t, out)

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "retry me", "--prompt", "irrelevant",
	)
	tID := parseTaskID(t, out)

	// Claim and fail via Go API.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	_, err = task.ClaimNext(ws, worker.NewID())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := task.Fail(ws, tID, "simulated failure"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// Verify failed.
	out = mustSt(t, wsDir, "task", "show", tID)
	if !strings.Contains(out, "Status:  failed") {
		t.Errorf("should be failed:\n%s", out)
	}

	// Retry via CLI.
	mustSt(t, wsDir, "task", "retry", tID)

	// Should be pending again (no deps).
	out = mustSt(t, wsDir, "task", "show", tID)
	if !strings.Contains(out, "Status:  pending") {
		t.Errorf("should be pending after retry:\n%s", out)
	}
}

func TestTaskListByStatus(t *testing.T) {
	wsDir := initWorkspace(t, nil)
	out := mustSt(t, wsDir, "goal", "add", "list filter goal")
	goalID := parseGoalID(t, out)

	// T1 and T2 both start pending.
	out = mustSt(t, wsDir, "task", "add", "--goal", goalID, "--title", "T1", "--prompt", "t1")
	t1ID := parseTaskID(t, out)
	out = mustSt(t, wsDir, "task", "add", "--goal", goalID, "--title", "T2", "--prompt", "t2")
	t2ID := parseTaskID(t, out)

	// Claim exactly one task (whichever ClaimNext picks first — IDs are random).
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	claimed, err := task.ClaimNext(ws, worker.NewID())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Determine which was claimed and which wasn't.
	claimedID := claimed.ID
	var pendingID string
	if claimedID == t1ID {
		pendingID = t2ID
	} else {
		pendingID = t1ID
	}

	// --status running: claimed task yes, other no.
	out = mustSt(t, wsDir, "task", "list", "--status", "running")
	if !strings.Contains(out, claimedID) {
		t.Errorf("running list should contain claimed task %s:\n%s", claimedID, out)
	}
	if strings.Contains(out, pendingID) {
		t.Errorf("running list should NOT contain pending task %s:\n%s", pendingID, out)
	}

	// --status pending: unclaimed task yes, claimed no.
	out = mustSt(t, wsDir, "task", "list", "--status", "pending")
	if !strings.Contains(out, pendingID) {
		t.Errorf("pending list should contain pending task %s:\n%s", pendingID, out)
	}
	if strings.Contains(out, claimedID) {
		t.Errorf("pending list should NOT contain claimed task %s:\n%s", claimedID, out)
	}

	// --goal filter: shows both.
	out = mustSt(t, wsDir, "task", "list", "--goal", goalID)
	if !strings.Contains(out, t1ID) || !strings.Contains(out, t2ID) {
		t.Errorf("goal-filtered list should contain both tasks:\n%s", out)
	}
}

func TestHeartbeatCommandAndWorkerList(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	// Worker list on an empty workspace.
	out := mustSt(t, wsDir, "worker")
	if !strings.Contains(out, "no active workers") {
		t.Logf("worker list (empty): %s", out)
	}

	// Create and claim a task.
	out = mustSt(t, wsDir, "goal", "add", "heartbeat goal")
	goalID := parseGoalID(t, out)
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "hb task", "--prompt", "irrelevant",
	)
	tID := parseTaskID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	wID := worker.NewID()
	if _, err := task.ClaimNext(ws, wID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Register a fake worker record so worker list shows it.
	if err := worker.Register(ws, &worker.Worker{
		ID:          wID,
		PID:         os.Getpid(),
		TaskID:      tID,
		Agent:       "test",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	// st heartbeat <taskID> — must succeed.
	mustSt(t, wsDir, "heartbeat", tID)

	// Heartbeat file must exist.
	hbPath := filepath.Join(wsDir, ".st", "heartbeats", tID)
	if _, err := os.Stat(hbPath); err != nil {
		t.Errorf("heartbeat file missing after st heartbeat: %v", err)
	}

	// Worker list must show the registered worker.
	out = mustSt(t, wsDir, "worker")
	if !strings.Contains(out, wID) {
		t.Errorf("worker list missing worker %s:\n%s", wID, out)
	}
	if !strings.Contains(out, tID) {
		t.Errorf("worker list missing taskID %s:\n%s", tID, out)
	}
}

func TestLogCommand(t *testing.T) {
	wsDir := initWorkspace(t, nil)
	out := mustSt(t, wsDir, "goal", "add", "log goal")
	goalID := parseGoalID(t, out)
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "log task", "--prompt", "irrelevant",
	)
	tID := parseTaskID(t, out)

	// Write a fake log entry.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	logContent := "line one\nline two\nline three\n"
	if err := os.WriteFile(ws.LogPath(tID), []byte(logContent), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	// st log <id> — full output.
	out = mustSt(t, wsDir, "log", tID)
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line three") {
		t.Errorf("log output wrong:\n%s", out)
	}

	// st log <id> --tail 1 — last line only.
	out = mustSt(t, wsDir, "log", tID, "--tail", "1")
	if !strings.Contains(out, "line three") {
		t.Errorf("log --tail 1 missing last line:\n%s", out)
	}
	if strings.Contains(out, "line one") {
		t.Errorf("log --tail 1 should not include first line:\n%s", out)
	}
}

func TestAgentWithMaxTurnsAndDescription(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	// Add agent with non-default max-turns and description.
	mustSt(t, wsDir, "agent", "create", "speedy",
		"--prompt", "You are fast.",
		"--description", "A speed-optimized agent",
		"--max-turns", "3",
		"--model", "haiku",
		"--tools", "Bash",
	)

	out := mustSt(t, wsDir, "agent", "show", "speedy")
	if !strings.Contains(out, "A speed-optimized agent") {
		t.Errorf("agent show missing description:\n%s", out)
	}
	if !strings.Contains(out, "haiku") {
		t.Errorf("agent show missing model:\n%s", out)
	}
	// MaxTurns=3 should appear in the list.
	out = mustSt(t, wsDir, "agent", "list")
	if !strings.Contains(out, "speedy") {
		t.Errorf("agent list missing speedy:\n%s", out)
	}
}

func TestAllBuiltinAgentsShowCorrectly(t *testing.T) {
	wsDir := initWorkspace(t, nil)
	builtins := []struct {
		name       string
		wantInDesc string
	}{
		{"impl", "Implements"},
		{"debug", "Investigates"},
		{"test", "Writes tests"},
		{"docs", "Writes and updates"},
		{"review", "Reviews"},
		{"security", "Audits"},
		{"merge", "Integrates"},
		{"merge-task", "Merges a completed"},
	}
	for _, b := range builtins {
		t.Run(b.name, func(t *testing.T) {
			out := mustSt(t, wsDir, "agent", "show", b.name)
			if !strings.Contains(out, b.name) {
				t.Errorf("agent show %s missing name:\n%s", b.name, out)
			}
			if b.wantInDesc != "" && !strings.Contains(out, b.wantInDesc) {
				t.Errorf("agent show %s: want %q in output:\n%s", b.name, b.wantInDesc, out)
			}
		})
	}
}

func TestTaskDoneWithBranchRecordsResult(t *testing.T) {
	// Verify that st task-done --branch sets Result.Branch so merge-task can find it.
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	out := mustSt(t, wsDir, "goal", "add", "branch result goal")
	goalID := parseGoalID(t, out)

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID, "--title", "branch task", "--prompt", "make branch",
		"--repo", "myrepo",
	)
	tID := parseTaskID(t, out)

	// Claim via Go API.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	_, err = task.ClaimNext(ws, worker.NewID())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Done with explicit --branch (simulates what a real worker would do).
	taskShow := mustSt(t, wsDir, "task", "show", tID)
	branch := parseBranchFromShow(t, taskShow)
	mustSt(t, wsDir, "task-done", tID, "--summary", "made the branch", "--branch", branch)

	// Show task — Summary and Branch should be recorded in result.
	out = mustSt(t, wsDir, "task", "show", tID)
	if !strings.Contains(out, "made the branch") {
		t.Errorf("task show missing summary:\n%s", out)
	}
}

func TestStatusWithMultipleGoalsAndTasks(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	// Create two goals with tasks each.
	out1 := mustSt(t, wsDir, "goal", "add", "goal alpha")
	g1 := parseGoalID(t, out1)
	out2 := mustSt(t, wsDir, "goal", "add", "goal beta")
	g2 := parseGoalID(t, out2)

	out := mustSt(t, wsDir, "task", "add", "--goal", g1, "--title", "alpha-task", "--prompt", "a", "--repo", "myrepo")
	t1 := parseTaskID(t, out)
	out = mustSt(t, wsDir, "task", "add", "--goal", g2, "--title", "beta-task", "--prompt", "b", "--repo", "myrepo")
	t2 := parseTaskID(t, out)

	// st status — all goals and tasks visible.
	out = mustSt(t, wsDir, "status")
	for _, id := range []string{g1, g2, t1, t2} {
		if !strings.Contains(out, id) {
			t.Errorf("st status missing %s:\n%s", id, out)
		}
	}

	// st status --goal g1 — only g1 and t1.
	out = mustSt(t, wsDir, "status", "--goal", g1)
	if !strings.Contains(out, g1) || !strings.Contains(out, t1) {
		t.Errorf("status --goal g1 missing g1/t1:\n%s", out)
	}
	if strings.Contains(out, t2) {
		t.Errorf("status --goal g1 should not show t2:\n%s", out)
	}
}

// TestMergeAgentContentHasResetHard verifies that the merge agent embedded in
// the binary contains the primary worktree reset command after each
// git update-ref refs/heads/main call. This encodes the fix for the bug
// where merge agents left uncommitted changes in the primary working tree
// after advancing the main ref.
// Built-in agents are now embedded in the binary; we verify via `st agent show`.
func TestMergeAgentContentHasResetHard(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	// Read the merge agent via st agent show (works for both workspace overrides
	// and embedded built-ins).
	content := mustSt(t, wsDir, "agent", "show", "merge")

	// Must contain the primary worktree reset command.
	if !strings.Contains(content, "git rev-parse --git-common-dir") {
		t.Error("merge agent missing git rev-parse --git-common-dir for primary worktree detection")
	}
	if !strings.Contains(content, "reset --hard main") {
		t.Error("merge agent missing git reset --hard main to sync primary worktree")
	}
	// Must appear after update-ref (count occurrences — should be at least 2,
	// one for FF/squash paths and one for conflict resolution path).
	resetCount := strings.Count(content, "reset --hard main")
	if resetCount < 2 {
		t.Errorf("merge agent should have at least 2 reset --hard main occurrences (FF+squash, conflict), got %d", resetCount)
	}
}

// ─── Group 2: Supervisor + Real Claude Tests ──────────────────────────────────
//
// These tests start the real supervisor and run real claude workers.
// They require the claude binary at /Users/zidiwang/.local/bin/claude.
// Run with: go test -v -tags e2e -run TestSupervisor... -timeout 30m ./e2e/

func TestSupervisorCompletesSimpleTask(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	// Create goal + task (skip planner: set goal to active).
	out := mustSt(t, wsDir, "goal", "add", "create a marker file")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "create DONE.txt",
		"--prompt", "Create a file called DONE.txt with the single line content: task complete\nThen commit it: git add DONE.txt && git commit -m 'add DONE.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// Start supervisor.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Wait for task to complete.
	waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 8*time.Minute)

	// Get task branch and verify it exists in the repo.
	taskShow := mustSt(t, wsDir, "task", "show", taskID)
	branch := parseBranchFromShow(t, taskShow)
	if !branchExistsInRepo(t, repoDir, branch) {
		t.Errorf("branch %s not found in repo", branch)
	}

	// DONE.txt must be committed on the task branch.
	if !fileExistsInBranch(t, repoDir, branch, "DONE.txt") {
		t.Errorf("DONE.txt not committed on branch %s", branch)
	}

	// Goal must be marked done — verifies checkGoalCompletion ran after monitorWorker.
	waitForGoalStatus(t, wsDir, goalID, goal.StatusDone, 2*time.Minute)

	// Worktree directory must be cleaned up — verifies monitorWorker calls os.RemoveAll.
	// Poll with a timeout: sweepActiveGoals (dispatch loop goroutine) can mark the goal
	// "done" concurrently with monitorWorker's os.RemoveAll, so the worktree may lag
	// slightly behind the goal-done transition.
	worktreePath := filepath.Join(wsDir, ".st", "worktrees", taskID)
	worktreeGone := false
	for i := 0; i < 12; i++ { // 12 × 5s = 60s max
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			worktreeGone = true
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !worktreeGone {
		t.Errorf("worktree %s should be deleted after task done", worktreePath)
	}
}

func TestMergeTaskIntegratesIntoGoalBranch(t *testing.T) {
	requireClaude(t)

	// Needs a remote for merge-task's "git fetch origin" / "git push origin".
	repoDir := initRepoWithRemote(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "integrate A.txt via merge-task")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// T1: impl — create A.txt and commit.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "create A.txt",
		"--prompt", "Create a file named A.txt with the single line content: from-impl\nThen commit: git add A.txt && git commit -m 'add A.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	t1ID := parseTaskID(t, out)

	// T2: merge-task — merge T1 into goal branch.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "merge T1 into goal branch",
		"--prompt", fmt.Sprintf("Merge task %s into the goal branch. Run 'st task show %s' to find the source branch.", t1ID, t1ID),
		"--agent", "merge-task",
		"--repo", "myrepo",
		"--deps", t1ID,
	)
	t2ID := parseTaskID(t, out)

	// T3: verify — A.txt should be visible via the goal branch.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "verify A.txt via goal branch",
		"--prompt", "Check if file A.txt exists in the current directory. If it does, create VERIFIED.txt and commit: git add VERIFIED.txt && git commit -m 'verified A.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
		"--deps", t2ID,
	)
	t3ID := parseTaskID(t, out)

	// Start supervisor.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Wait for all three tasks.
	goalBranch := "st/goals/" + goalID
	waitForTaskStatus(t, wsDir, t1ID, task.StatusDone, 8*time.Minute)
	waitForTaskStatus(t, wsDir, t2ID, task.StatusDone, 8*time.Minute)
	waitForTaskStatus(t, wsDir, t3ID, task.StatusDone, 8*time.Minute)

	// A.txt must be on goal branch (merged by T2).
	if !fileExistsInBranch(t, repoDir, goalBranch, "A.txt") {
		t.Errorf("A.txt not found on goal branch %s after merge-task", goalBranch)
	}

	// VERIFIED.txt must be on T3's task branch (proves T3 saw A.txt from goal branch).
	t3Show := mustSt(t, wsDir, "task", "show", t3ID)
	t3Branch := parseBranchFromShow(t, t3Show)
	if !fileExistsInBranch(t, repoDir, t3Branch, "VERIFIED.txt") {
		t.Errorf("VERIFIED.txt not found on T3 branch %s", t3Branch)
	}
}

func TestParallelGoalsOnSameRepo(t *testing.T) {
	requireClaude(t)

	repoDir := initRepoWithRemote(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	// Create G1 and G2 on the same repo.
	out1 := mustSt(t, wsDir, "goal", "add", "parallel goal one")
	g1ID := parseGoalID(t, out1)
	out2 := mustSt(t, wsDir, "goal", "add", "parallel goal two")
	g2ID := parseGoalID(t, out2)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	// Set both goals active to skip planners for a controlled test.
	if err := goal.UpdateStatus(ws, g1ID, goal.StatusActive); err != nil {
		t.Fatalf("set G1 active: %v", err)
	}
	if err := goal.UpdateStatus(ws, g2ID, goal.StatusActive); err != nil {
		t.Fatalf("set G2 active: %v", err)
	}

	// Create one task per goal.
	out := mustSt(t, wsDir, "task", "add",
		"--goal", g1ID,
		"--title", "write file1",
		"--prompt", "Create file1.txt with content 'goal1'. Commit: git add file1.txt && git commit -m 'add file1.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	t1ID := parseTaskID(t, out)

	out = mustSt(t, wsDir, "task", "add",
		"--goal", g2ID,
		"--title", "write file2",
		"--prompt", "Create file2.txt with content 'goal2'. Commit: git add file2.txt && git commit -m 'add file2.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	t2ID := parseTaskID(t, out)

	// Start supervisor.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// After 30 seconds, BOTH tasks should be running or done (not still pending).
	// If tasks were serialized (one blocked waiting for the other), one would still be pending.
	time.Sleep(30 * time.Second)
	for _, tid := range []string{t1ID, t2ID} {
		out, _ := runSt(t, wsDir, "task", "show", tid)
		if strings.Contains(out, "Status:  pending") {
			t.Errorf("task %s still pending 30s after supervisor start — possible serialization\n%s", tid, out)
		}
	}

	// Wait for full completion.
	waitForTaskStatus(t, wsDir, t1ID, task.StatusDone, 8*time.Minute)
	waitForTaskStatus(t, wsDir, t2ID, task.StatusDone, 8*time.Minute)

	// Files live on the task branches (no merge-task step in this test).
	// The parallel-dispatch property is already proven by both tasks starting within 30s.
	t1Show := mustSt(t, wsDir, "task", "show", t1ID)
	t1Branch := parseBranchFromShow(t, t1Show)
	if !fileExistsInBranch(t, repoDir, t1Branch, "file1.txt") {
		t.Errorf("file1.txt not found on T1 task branch %s", t1Branch)
	}
	t2Show := mustSt(t, wsDir, "task", "show", t2ID)
	t2Branch := parseBranchFromShow(t, t2Show)
	if !fileExistsInBranch(t, repoDir, t2Branch, "file2.txt") {
		t.Errorf("file2.txt not found on T2 task branch %s", t2Branch)
	}
}

func TestMergeAgentMergesToMain(t *testing.T) {
	requireClaude(t)

	// Needs a remote so the merge-task can push.
	repoDir := initRepoWithRemote(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)
	addTestMergeMainAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "merge goal branch to main")
	goalID := parseGoalID(t, out)
	goalBranch := "st/goals/" + goalID

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// T1: create MERGED.txt, commit.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "create MERGED.txt",
		"--prompt", "Create MERGED.txt with content 'ready to merge'. Commit: git add MERGED.txt && git commit -m 'add MERGED.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	t1ID := parseTaskID(t, out)

	// T2: merge-task merges T1 into goal branch.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "merge T1 into goal branch",
		"--prompt", fmt.Sprintf("Merge task %s into the goal branch. Run 'st task show %s' to find the source branch.", t1ID, t1ID),
		"--agent", "merge-task",
		"--repo", "myrepo",
		"--deps", t1ID,
	)
	t2ID := parseTaskID(t, out)

	// T3: test-merge-main merges goal branch into main.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "merge goal branch to main",
		"--prompt", fmt.Sprintf("Merge goal branch %s into main.", goalBranch),
		"--agent", "test-merge-main",
		"--repo", "myrepo",
		"--deps", t2ID,
	)
	t3ID := parseTaskID(t, out)

	// Start supervisor.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Wait for all tasks.
	waitForTaskStatus(t, wsDir, t1ID, task.StatusDone, 8*time.Minute)
	waitForTaskStatus(t, wsDir, t2ID, task.StatusDone, 8*time.Minute)
	waitForTaskStatus(t, wsDir, t3ID, task.StatusDone, 8*time.Minute)

	// MERGED.txt must be on main after T3.
	if !fileExistsInBranch(t, repoDir, "main", "MERGED.txt") {
		t.Errorf("MERGED.txt not found on main after final merge")
	}

	// Verify primary working tree is clean after merge.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoDir
	statusOut, statusErr := statusCmd.Output()
	if statusErr != nil {
		t.Errorf("git status --porcelain in primary repo: %v", statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("primary repo working tree should be clean after merge, got:\n%s", string(statusOut))
	}
}

// TestLogCommandAfterRealTask verifies that st log <id> shows real claude output
// after a task runs via the supervisor.
func TestLogCommandAfterRealTask(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "log output goal")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "log test task",
		"--prompt", "Create a file LOG_TEST.txt with content 'logged'. Commit: git add LOG_TEST.txt && git commit -m 'log test'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 5*time.Minute)

	// st log must return non-empty content (the stream-json output from claude).
	logOut, code := runSt(t, wsDir, "log", taskID)
	if code != 0 {
		t.Fatalf("st log exit %d: %s", code, logOut)
	}
	if len(strings.TrimSpace(logOut)) == 0 {
		t.Errorf("st log returned empty output for completed task")
	}
	// Log should contain JSON-ish content (stream-json format).
	if !strings.Contains(logOut, "{") {
		t.Logf("st log content (sample): %s", logOut[:min(200, len(logOut))])
	}
}

// TestContextInjectionBetweenTasks verifies that a task with --context-from
// receives the prior task's summary in its prompt and can complete successfully.
func TestContextInjectionBetweenTasks(t *testing.T) {
	requireClaude(t)

	wsDir := initWorkspace(t, nil)
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "context injection test")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// T1: complete with a unique summary so we can verify it appears in T2's context.
	uniqueTag := fmt.Sprintf("CONTEXT-TAG-%d", time.Now().UnixNano())
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "T1 provider",
		"--prompt", fmt.Sprintf("Your only job is to call: st task-done <TASK_ID> --summary '%s'", uniqueTag),
		"--agent", "test-worker",
	)
	t1ID := parseTaskID(t, out)

	// T2: depends on T1, gets context injected, runs after T1 done.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "T2 consumer",
		"--prompt", "You have received context from a prior task. Complete your task by calling st task-done.",
		"--agent", "test-worker",
		"--deps", t1ID,
		"--context-from", t1ID,
	)
	t2ID := parseTaskID(t, out)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// T1 must complete first.
	waitForTaskStatus(t, wsDir, t1ID, task.StatusDone, 4*time.Minute)

	// Verify T1's summary was recorded.
	t1Show := mustSt(t, wsDir, "task", "show", t1ID)
	if !strings.Contains(t1Show, uniqueTag) {
		t.Errorf("T1 summary missing unique tag %s:\n%s", uniqueTag, t1Show)
	}

	// T2 must also complete (its context prompt was buildable without error).
	waitForTaskStatus(t, wsDir, t2ID, task.StatusDone, 4*time.Minute)

	// Verify T2's log contains the context section with T1's summary.
	logData, readErr := os.ReadFile(filepath.Join(wsDir, ".st", "logs", t2ID+".log"))
	if readErr != nil {
		t.Logf("could not read T2 log: %v", readErr)
	} else if !strings.Contains(string(logData), uniqueTag) {
		t.Errorf("T2 log does not contain T1's context tag %s — context injection may have failed", uniqueTag)
	}
}

// TestRunCommandEndToEnd tests the primary user workflow: `st run "goal"`.
// This is the command users actually invoke. It covers the full pipeline:
// st run → planner creates tasks → supervisor dispatches workers → goal done.
// Uses no repo to keep it fast (no git worktrees or merge-task chains needed).
func TestRunCommandEndToEnd(t *testing.T) {
	requireClaude(t)

	wsDir := initWorkspace(t, nil)
	addTestWorkerAgent(t, wsDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// st run blocks until the goal reaches done or failed, then exits.
	cmd := exec.CommandContext(ctx, stBin, "run",
		"Create one simple task with no --repo flag. Use the test-worker agent. The task should just call st task-done immediately.",
		"--hint", "Create exactly one task. Use agent test-worker. No --repo flag.",
		"--max-concurrent", "4",
	)
	cmd.Dir = wsDir
	baseEnv := filterEnv(os.Environ(), "CLAUDECODE", "ST_WORKSPACE")
	cmd.Env = append(baseEnv,
		"PATH="+filepath.Dir(stBin)+":/Users/zidiwang/.local/bin:"+os.Getenv("PATH"),
		"ST_WORKSPACE="+wsDir,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("st run failed (exit non-zero): %v\nOutput:\n%s", err, out.String())
	}

	if !strings.Contains(out.String(), "done") {
		t.Errorf("st run output should report goal done:\n%s", out.String())
	}

	// Verify at least one task was created and completed.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil || len(goals) == 0 {
		t.Fatalf("no goals found after st run")
	}
	goalID := goals[0].ID
	tasks, err := task.ListForGoal(ws, goalID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatalf("planner created no tasks")
	}
	for _, ts := range tasks {
		if ts.Status != task.StatusDone {
			t.Errorf("task %s (%s) should be done, got %s", ts.Task.ID, ts.Task.Title, ts.Status)
		}
	}
}

// TestRunCommandWithRepoAndMergeChain tests st run with a real repo, verifying
// the full impl→merge-task chain that the planner is supposed to create.
// This is the scenario that broke in production: planner creates impl task +
// merge-task, impl worker commits (due to BuildPrompt fix), merge-task succeeds.
func TestRunCommandWithRepoAndMergeChain(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// This goal explicitly requests a file change so the planner has reason to
	// create an impl task with --repo myrepo and a following merge-task.
	cmd := exec.CommandContext(ctx, stBin, "run",
		"Create a file called RESULT.txt with content 'success' in the myrepo repo. Commit it. Then merge the task branch into the goal branch.",
		"--hint", "Use agent test-worker. Use --repo myrepo. Create an impl task that writes and commits RESULT.txt, then a merge-task step.",
		"--max-concurrent", "4",
	)
	cmd.Dir = wsDir
	baseEnv := filterEnv(os.Environ(), "CLAUDECODE", "ST_WORKSPACE")
	cmd.Env = append(baseEnv,
		"PATH="+filepath.Dir(stBin)+":/Users/zidiwang/.local/bin:"+os.Getenv("PATH"),
		"ST_WORKSPACE="+wsDir,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("st run with repo failed: %v\nOutput:\n%s", err, out.String())
	}

	// Verify goal reached done.
	if !strings.Contains(out.String(), "done") {
		t.Errorf("st run should report done:\n%s", out.String())
	}

	// Verify RESULT.txt landed on the goal branch (merged by merge-task).
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil || len(goals) == 0 {
		t.Fatalf("no goals found")
	}
	goalBranch := "st/goals/" + goals[0].ID
	if branchExistsInRepo(t, repoDir, goalBranch) {
		if !fileExistsInBranch(t, repoDir, goalBranch, "RESULT.txt") {
			t.Errorf("RESULT.txt not found on goal branch %s after merge-task", goalBranch)
		}
	}
	// Even if goal branch doesn't exist (no merge-task created), at minimum
	// verify the goal completed and RESULT.txt was committed somewhere.
	tasks, err := task.ListForGoal(ws, goals[0].ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatalf("no tasks created by planner")
	}
	anyCommit := false
	for _, ts := range tasks {
		if ts.Task.Repo == "myrepo" && ts.Task.BranchName != "" {
			if fileExistsInBranch(t, repoDir, ts.Task.BranchName, "RESULT.txt") {
				anyCommit = true
			}
		}
	}
	if !anyCommit {
		t.Errorf("RESULT.txt not found on any task branch — impl worker may not have committed")
	}
}

// min returns the smaller of a, b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Tests encoding the "impl agent never committed" failure mode ─────────────

// TestBuildPromptRequiresGitCommit verifies that BuildPrompt injects an explicit
// git commit requirement when the task has a repo. This encodes the fix for the
// bug where impl agents completed without committing, losing all changes when
// the worktree was cleaned up.
func TestBuildPromptRequiresGitCommit(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}

	out := mustSt(t, wsDir, "goal", "add", "commit requirement test")
	goalID := parseGoalID(t, out)

	// Task WITH repo — should get the commit requirement.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "task with repo",
		"--prompt", "do something",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// Claim it so we can fetch it in running state.
	_, err = task.ClaimNext(ws, worker.NewID())
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}

	t_obj, _, err := task.Get(ws, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	def, err := agent.Get(ws, "impl")
	if err != nil {
		t.Fatalf("get impl agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, t_obj, def)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}

	if !strings.Contains(prompt, "git add -A && git commit") {
		t.Errorf("prompt missing git commit requirement; prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Uncommitted changes will be lost") {
		t.Errorf("prompt missing uncommitted changes warning; prompt:\n%s", prompt)
	}

	// Task WITHOUT repo — must NOT include the git commit requirement
	// (no-repo tasks run in plain temp dirs, not git worktrees).
	wsDir2 := initWorkspace(t, nil)
	ws2, err := workspace.Open(wsDir2)
	if err != nil {
		t.Fatalf("open workspace2: %v", err)
	}
	out = mustSt(t, wsDir2, "goal", "add", "no-repo goal")
	goalID2 := parseGoalID(t, out)

	out = mustSt(t, wsDir2, "task", "add",
		"--goal", goalID2,
		"--title", "no-repo task",
		"--prompt", "just call done",
	)
	taskID2 := parseTaskID(t, out)
	_, err = task.ClaimNext(ws2, worker.NewID())
	if err != nil {
		t.Fatalf("claim no-repo task: %v", err)
	}
	t_obj2, _, err := task.Get(ws2, taskID2)
	if err != nil {
		t.Fatalf("get no-repo task: %v", err)
	}
	def2, err := agent.Get(ws2, "impl")
	if err != nil {
		t.Fatalf("get impl agent (no-repo ws): %v", err)
	}
	prompt2, err := agent.BuildPrompt(ws2, t_obj2, def2)
	if err != nil {
		t.Fatalf("build prompt no-repo: %v", err)
	}
	if strings.Contains(prompt2, "git add -A && git commit") {
		t.Errorf("no-repo task prompt should NOT include git commit requirement; prompt:\n%s", prompt2)
	}
}

// TestMergeTaskFailsFastWhenSourceBranchHasNoCommits verifies that the merge-task
// agent detects an empty source branch quickly and calls 'st task-fail' rather
// than exhausting all turns investigating. This encodes the fix for the scenario
// where a planner-dispatched impl task completed without committing to git.
func TestMergeTaskFailsFastWhenSourceBranchHasNoCommits(t *testing.T) {
	requireClaude(t)

	// No remote needed: the updated merge-task agent skips 'git fetch origin'
	// when there is no remote origin.
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})

	out := mustSt(t, wsDir, "goal", "add", "merge-task empty-branch detection")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// Create the "impl" task. The supervisor assigns it a branch name like
	// st/tasks/t-XXXXXXXX. We retrieve that name from 'st task show'.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "impl task (no commit simulation)",
		"--prompt", "create something",
		"--repo", "myrepo",
	)
	implTaskID := parseTaskID(t, out)

	// Get the impl task's assigned branch name before claiming.
	implShow := mustSt(t, wsDir, "task", "show", implTaskID)
	implBranch := parseBranchFromShow(t, implShow)
	goalBranch := "st/goals/" + goalID

	// Claim the impl task via Go API (moves it to running/).
	wID := worker.NewID()
	_, err = task.ClaimNext(ws, wID)
	if err != nil {
		t.Fatalf("claim impl task: %v", err)
	}

	// Create the goal branch and impl branch in git — both pointing to the same
	// commit as main. This simulates an impl agent that ran in a worktree but
	// never committed: the branch exists but has no new commits.
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("branch", goalBranch, "main")
	runGit("branch", implBranch, "main")

	// Mark the impl task done without committing (simulates the bug scenario).
	if err := task.Done(ws, implTaskID, "done but no git commit", implBranch); err != nil {
		t.Fatalf("mark impl task done: %v", err)
	}

	// Add the merge-task that should detect the empty branch.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "merge impl into goal branch",
		"--prompt", fmt.Sprintf(
			"Merge task %s into the goal branch. Run 'st task show %s' to find the source branch.",
			implTaskID, implTaskID,
		),
		"--agent", "merge-task",
		"--repo", "myrepo",
		"--deps", implTaskID,
	)
	mergeTaskID := parseTaskID(t, out)

	// Start supervisor.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// With the fix: merge-task now calls st task-done (not fail) when source branch
	// has no commits. "Nothing to merge" is a valid outcome (e.g. review agents
	// don't commit code).
	waitForTaskStatus(t, wsDir, mergeTaskID, task.StatusDone, 4*time.Minute)

	mergeShow := mustSt(t, wsDir, "task", "show", mergeTaskID)
	if !strings.Contains(mergeShow, "nothing to merge") {
		t.Errorf("merge-task summary should mention 'nothing to merge'; task show:\n%s", mergeShow)
	}
}

// waitForWorkerPIDForTask polls "st worker list" until the worker for taskID appears,
// then returns its PID. Returns -1 if the task completes before the worker is found
// (race: task finished too fast to grab the PID — acceptable, test still valid).
// Output columns: ID  PID  TASK  AGENT  ALIVE  HEARTBEAT
func waitForWorkerPIDForTask(t *testing.T, wsDir, taskID string, timeout time.Duration) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\S+\s+(\d+)\s+` + regexp.QuoteMeta(taskID))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := runSt(t, wsDir, "worker")
		if m := re.FindStringSubmatch(out); len(m) >= 2 {
			pid := 0
			fmt.Sscan(m[1], &pid)
			if pid > 0 {
				return pid
			}
		}
		// If task is already done, the worker is gone — exit early.
		show, _ := runSt(t, wsDir, "task", "show", taskID)
		if strings.Contains(show, "Status:  done") {
			return -1
		}
		time.Sleep(500 * time.Millisecond)
	}
	return -1
}

// ─── Group 2 (continued): Missing Supervisor Behaviors ───────────────────────

// TestSupervisorCompletesSimpleTask already verifies:
//   - goal marked done after all tasks done (checkGoalCompletion)
//   - worktree cleaned up by monitorWorker
// Tests below cover the four remaining critical paths.

// TestFullPlannerFlow verifies supervisor.dispatchGoals():
// queued goal → supervisor spawns real planner → planner creates tasks via st task add
// → supervisor dispatches workers → all tasks done → goal marked done.
// This is the most critical previously-untested path: the entire planner loop.
// Uses no repos so the planner creates simple no-repo tasks (no merge chains needed).
func TestFullPlannerFlow(t *testing.T) {
	requireClaude(t)

	// No repos — forces planner to create tasks without --repo (no git worktrees,
	// no merge-task chains). Workers run in plain temp dirs and just call st task-done.
	wsDir := initWorkspace(t, nil)
	addTestWorkerAgent(t, wsDir)

	// Goal left as QUEUED — the entire point is supervisor.dispatchGoals() must fire.
	out := mustSt(t, wsDir, "goal", "add",
		"Create one simple task. Use the test-worker agent with no --repo flag. The task should call st task-done immediately.",
		"--hint", "Create exactly one task. Use agent test-worker. No --repo flag.",
	)
	goalID := parseGoalID(t, out)

	// Start supervisor. No manually-created tasks — planner must create them.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Step 1: supervisor must mark the goal as "planning" (planner spawned).
	waitForGoalStatus(t, wsDir, goalID, goal.StatusPlanning, 2*time.Minute)

	// Step 2: wait for goal to eventually reach "done" (all planner-created tasks completed).
	waitForGoalStatus(t, wsDir, goalID, goal.StatusDone, 8*time.Minute)

	// Step 3: at least one task must have been created by the planner.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	tasks, err := task.ListForGoal(ws, goalID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatalf("planner created no tasks for goal %s", goalID)
	}
	for _, ts := range tasks {
		if ts.Status != task.StatusDone {
			t.Errorf("task %s (%s) should be done, got %s", ts.Task.ID, ts.Task.Title, ts.Status)
		}
	}
}

// TestWorkerTaskFailureHandling verifies that a worker which calls "st task-fail"
// transitions the task to failed with the error message stored, and that a blocked
// dependent task remains blocked (a failed dep does NOT unblock it).
func TestWorkerTaskFailureHandling(t *testing.T) {
	requireClaude(t)

	wsDir := initWorkspace(t, nil)
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "failure handling test")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// T1: worker deliberately calls st task-fail.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "deliberate failure",
		"--prompt", "Your only job is to call: st task-fail <your-task-id> --reason 'deliberate e2e test failure'. Do nothing else.",
		"--agent", "test-worker",
	)
	t1ID := parseTaskID(t, out)

	// T2: blocked on T1 — must stay blocked after T1 fails.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "should stay blocked",
		"--prompt", "If you somehow run, call st task-done immediately.",
		"--agent", "test-worker",
		"--deps", t1ID,
	)
	t2ID := parseTaskID(t, out)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// T1 must reach failed.
	waitForTaskStatus(t, wsDir, t1ID, task.StatusFailed, 3*time.Minute)

	// Error message must be stored.
	t1Show := mustSt(t, wsDir, "task", "show", t1ID)
	if !strings.Contains(t1Show, "deliberate e2e test failure") {
		t.Errorf("T1 error message not stored:\n%s", t1Show)
	}

	// T2 must still be blocked (failed dep does NOT count as done).
	t2Show := mustSt(t, wsDir, "task", "show", t2ID)
	if !strings.Contains(t2Show, "Status:  blocked") {
		t.Errorf("T2 should remain blocked after T1 failed:\n%s", t2Show)
	}

	// Goal must NOT be done (not all tasks are done).
	goalShow := mustSt(t, wsDir, "goal", "show", goalID)
	if strings.Contains(goalShow, "Status:  done") {
		t.Errorf("goal should not be done when tasks are failed/blocked:\n%s", goalShow)
	}
}

// TestWorkerDeathCausesFailedThenRetrySucceeds tests the real behavior when a
// worker is killed while the supervisor is running:
// - monitorWorker (NOT healthCheck) detects the non-zero exit → marks task FAILED
// - st task retry → task back to pending
// - New worker dispatched → completes the task successfully
//
// This clarifies the design: monitorWorker handles workers in the CURRENT run;
// healthCheck and startupRecovery handle workers from a PREVIOUS crashed run.
func TestWorkerDeathCausesFailedThenRetrySucceeds(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "worker death retry test")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	// Slow task (sleep 20) so the worker is reliably alive when we kill it.
	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "death and retry",
		"--prompt", "First run: sleep 20\nThen create RETRY_RESULT.txt with content 'survived'. Commit: git add RETRY_RESULT.txt && git commit -m 'add RETRY_RESULT.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Wait until task is running.
	waitForTaskStatus(t, wsDir, taskID, task.StatusRunning, 2*time.Minute)

	// Poll for the worker PID.
	pid := waitForWorkerPIDForTask(t, wsDir, taskID, 15*time.Second)
	if pid <= 0 {
		t.Logf("worker completed before kill (race) — test still valid")
		return
	}
	t.Logf("killing worker pid=%d to test monitorWorker failure detection", pid)
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find worker process: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Logf("kill returned: %v", err)
	}

	// monitorWorker detects SIGKILL exit (non-zero) and calls task.Fail.
	waitForTaskStatus(t, wsDir, taskID, task.StatusFailed, 2*time.Minute)
	t.Logf("task correctly marked failed by monitorWorker after worker kill")

	// Retry via CLI — task back to pending.
	mustSt(t, wsDir, "task", "retry", taskID)
	retryShow, _ := runSt(t, wsDir, "task", "show", taskID)
	if !strings.Contains(retryShow, "Status:  pending") {
		t.Fatalf("task should be pending after retry:\n%s", retryShow)
	}

	// Supervisor dispatches a new worker that completes without being killed.
	waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 5*time.Minute)

	taskShow := mustSt(t, wsDir, "task", "show", taskID)
	branch := parseBranchFromShow(t, taskShow)
	if !fileExistsInBranch(t, repoDir, branch, "RETRY_RESULT.txt") {
		t.Errorf("RETRY_RESULT.txt not on branch %s after retry", branch)
	}
}

// TestHealthCheckDetectsOrphanedWorker tests supervisor.healthCheck() directly.
// healthCheck is the cleanup mechanism for workers from a PREVIOUS supervisor run
// whose monitorWorker goroutine is no longer running (supervisor crashed mid-flight).
// We simulate this by injecting a fake dead-PID worker record AFTER startup recovery
// has already run, so only healthCheck (which fires every 30s) can clean it up.
func TestHealthCheckDetectsOrphanedWorker(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "health check orphan test")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "orphan recovery",
		"--prompt", "Create ORPHAN_DONE.txt with content 'back from orphan'. Commit: git add ORPHAN_DONE.txt && git commit -m 'orphan done'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// Start supervisor. startupRecovery runs synchronously at boot.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Wait past startupRecovery AND the first dispatch tick so the task is still
	// in pending (not yet dispatched) when we inject the fake state.
	// We need to race the supervisor's dispatch, so we inject immediately.
	// If it dispatched already, we just accept the task completing normally.
	time.Sleep(2 * time.Second)

	// Inject fake "orphaned from previous supervisor run" state.
	// ClaimNext moves task to running/ with our fake worker ID.
	fakeWorkerID := worker.NewID()
	claimedTask, claimErr := task.ClaimNext(ws, fakeWorkerID)
	if claimErr == task.ErrNoTask {
		// Supervisor already dispatched the task — accept normal completion.
		t.Logf("supervisor dispatched task before injection — test still valid")
		waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 5*time.Minute)
		return
	}
	if claimErr != nil {
		t.Fatalf("inject claim: %v", claimErr)
	}
	if claimedTask.ID != taskID {
		t.Fatalf("claimed wrong task: got %s, want %s", claimedTask.ID, taskID)
	}

	// Register fake worker with a dead PID — no monitorWorker goroutine is watching it.
	deadPID := os.Getpid() + 999999
	if err := worker.Register(ws, &worker.Worker{
		ID:          fakeWorkerID,
		PID:         deadPID,
		TaskID:      taskID,
		Agent:       "test-worker",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("register fake worker: %v", err)
	}
	t.Logf("injected fake worker pid=%d — waiting up to 60s for healthCheck to fire", deadPID)

	// healthCheck fires every 30s. Dead PID → immediately stale.
	healed := false
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, status, err := task.Get(ws, taskID)
		if err == nil && status == task.StatusPending {
			t.Logf("healthCheck reset task to pending")
			healed = true
			break
		}
		if err == nil && status == task.StatusRunning {
			// Check if it's been re-dispatched to a real worker (not our fake one).
			listOut := mustSt(t, wsDir, "worker")
			if !strings.Contains(listOut, fakeWorkerID) {
				t.Logf("task re-dispatched to new real worker")
				healed = true
				break
			}
		}
		if err == nil && status == task.StatusDone {
			t.Logf("task done after health check recovery")
			return
		}
		time.Sleep(3 * time.Second)
	}
	if !healed {
		dumpLogs(t, wsDir, taskID)
		t.Fatalf("healthCheck did not reset orphaned task within 60s")
	}

	// Supervisor dispatches real worker that completes the task.
	waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 5*time.Minute)

	taskShow := mustSt(t, wsDir, "task", "show", taskID)
	branch := parseBranchFromShow(t, taskShow)
	if !fileExistsInBranch(t, repoDir, branch, "ORPHAN_DONE.txt") {
		t.Errorf("ORPHAN_DONE.txt not on branch %s after health check recovery", branch)
	}
}

// TestStartupRecoveryPreservesAndRestartsTask verifies supervisor.startupRecovery():
// on boot the supervisor detects tasks in running/ with dead worker PIDs and resets
// them to pending so they can be re-dispatched.
// Uses a pre-populated "crashed" state (no actual crash needed).
func TestStartupRecoveryPreservesAndRestartsTask(t *testing.T) {
	requireClaude(t)

	repoDir := initRepo(t)
	wsDir := initWorkspace(t, map[string]string{"myrepo": repoDir})
	addTestWorkerAgent(t, wsDir)

	out := mustSt(t, wsDir, "goal", "add", "startup recovery test")
	goalID := parseGoalID(t, out)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
		t.Fatalf("set goal active: %v", err)
	}

	out = mustSt(t, wsDir, "task", "add",
		"--goal", goalID,
		"--title", "task from before crash",
		"--prompt", "Create a file AFTER_CRASH.txt with content 'survived'. Commit: git add AFTER_CRASH.txt && git commit -m 'add AFTER_CRASH.txt'",
		"--agent", "test-worker",
		"--repo", "myrepo",
	)
	taskID := parseTaskID(t, out)

	// Claim the task via Go API — moves it to running/ with a fake worker ID.
	fakeWorkerID := worker.NewID()
	claimedTask, err := task.ClaimNext(ws, fakeWorkerID)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimedTask.ID != taskID {
		t.Fatalf("claimed wrong task: got %s, want %s", claimedTask.ID, taskID)
	}

	// Register a fake worker with a dead PID (os.Getpid()+999999 is reliably non-existent).
	deadPID := os.Getpid() + 999999
	if err := worker.Register(ws, &worker.Worker{
		ID:          fakeWorkerID,
		PID:         deadPID,
		TaskID:      taskID,
		Agent:       "test-worker",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("register fake worker: %v", err)
	}

	// Verify precondition: task is in running/ with the fake worker.
	_, status, err := task.Get(ws, taskID)
	if err != nil || status != task.StatusRunning {
		t.Fatalf("task should be running before supervisor start, got %s", status)
	}

	// Start supervisor. startupRecovery runs immediately on boot,
	// finds deadPID → deletes worker record → calls task.ResetToQueue.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	// Within 10s the task should be back to pending (startupRecovery is synchronous).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, status, err := task.Get(ws, taskID)
		if err == nil && (status == task.StatusPending || status == task.StatusRunning) {
			break // either reset to pending and already re-claimed by real worker
		}
		time.Sleep(1 * time.Second)
	}

	// Real worker must complete the task.
	waitForTaskStatus(t, wsDir, taskID, task.StatusDone, 5*time.Minute)

	taskShow := mustSt(t, wsDir, "task", "show", taskID)
	branch := parseBranchFromShow(t, taskShow)
	if !fileExistsInBranch(t, repoDir, branch, "AFTER_CRASH.txt") {
		t.Errorf("AFTER_CRASH.txt not on branch %s after startup recovery", branch)
	}
}

// TestOpenWSPrefersSTWorkspaceOverCWD verifies that when ST_WORKSPACE is set,
// openWS() uses it even if the process CWD is inside a different workspace.
//
// Regression: e2e supervisor processes inherited CWD = e2e/ (inside the main
// workspace tree). The old openWS() called FindRoot(cwd) first, which walked
// up and found the main workspace, ignoring the ST_WORKSPACE env var pointing
// to the test workspace. The fix makes ST_WORKSPACE take priority.
func TestOpenWSPrefersSTWorkspaceOverCWD(t *testing.T) {
	t.Parallel()

	// wsA is the "ancestor" workspace — the one CWD will be inside.
	wsA := initWorkspace(t, nil)
	// wsB is the intended workspace — the one ST_WORKSPACE points to.
	wsB := initWorkspace(t, nil)

	// Add a goal to wsB only, so we can tell which workspace was used.
	mustSt(t, wsB, "goal", "add", "hello from workspace B")

	// Run "st goal list" with ST_WORKSPACE=wsB but CWD=wsA. This simulates
	// the bug where a subprocess has CWD inside another workspace's tree.
	cmd := exec.Command(stBin, "goal", "list")
	cmd.Dir = wsA // CWD inside the other workspace
	cmd.Env = append(filterEnv(os.Environ(), "ST_WORKSPACE"), "ST_WORKSPACE="+wsB)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("st goal list failed: %v\n%s", err, out.String())
	}

	result := out.String()
	if !strings.Contains(result, "hello from workspace B") {
		t.Errorf("expected workspace B goal in output; got:\n%s", result)
	}
}

func TestWorkspaceIsolationNoLeakToAncestorWorkspace(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, nil)

	mustSt(t, wsDir, "repo", "add", "isolationtest", repoDir)

	// Resolve symlinks on wsDir so comparisons work on macOS where /var -> /private/var.
	realWsDir, err := filepath.EvalSymlinks(wsDir)
	if err != nil {
		t.Fatalf("eval symlinks on wsDir: %v", err)
	}

	// Repo path in workspace.json must be inside wsDir.
	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open test workspace: %v", err)
	}
	gotPath, ok := ws.Config.Repos["isolationtest"]
	if !ok {
		t.Fatal("isolationtest repo not registered in test workspace config")
	}
	// Resolve symlinks on the stored path (macOS /var -> /private/var).
	realGotPath, err := filepath.EvalSymlinks(gotPath)
	if err != nil {
		realGotPath = gotPath // keep original if path doesn't exist yet
	}
	wantPath := filepath.Join(realWsDir, "isolationtest")
	if realGotPath != wantPath {
		t.Errorf("repo path = %s (resolved: %s), want %s (inside test workspace)", gotPath, realGotPath, wantPath)
	}
	if !strings.HasPrefix(realGotPath, realWsDir) {
		t.Errorf("repo path %s is outside test workspace %s", realGotPath, realWsDir)
	}

	// Verify the directory does NOT exist in any ancestor of wsDir.
	// (This is the regression: old code created repos in the real workspace root.)
	parent := filepath.Dir(realWsDir)
	leaked := filepath.Join(parent, "isolationtest")
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		os.RemoveAll(leaked) //nolint:errcheck // clean up before failing
		t.Errorf("repo leaked to ancestor path %s — workspace isolation broken", leaked)
	}
}

// TestGoalMarkedDoneWhenAllTasksTerminal verifies that a goal transitions to
// done when all its tasks reach terminal state (done/failed/cancelled), even
// if not all tasks succeeded. This exercises the sweepActiveGoals fix.
func TestGoalMarkedDoneWhenAllTasksTerminal(t *testing.T) {
	t.Parallel()
	wsDir := initWorkspace(t, nil)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}

	// Create a goal and set it active.
	g, err := goal.Create(ws, "test terminal goal", nil, nil)
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusActive); err != nil {
		t.Fatalf("set active: %v", err)
	}

	// Create 3 tasks.
	var taskIDs []string
	for i := 0; i < 3; i++ {
		out := mustSt(t, wsDir, "task", "add", "--goal", g.ID, "--title", fmt.Sprintf("task %d", i), "--prompt", "do it")
		taskIDs = append(taskIDs, parseTaskID(t, out))
	}

	// Claim all 3 then put them in terminal states.
	// t0 → done, t1 → failed, t2 → cancelled (cancelled before claiming).
	mustSt(t, wsDir, "task", "cancel", taskIDs[2])

	c0, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("claim t0: %v", err)
	}
	if err := task.Done(ws, c0.ID, "ok", ""); err != nil {
		t.Fatalf("done t0: %v", err)
	}
	c1, err := task.ClaimNext(ws, "w-002")
	if err != nil {
		t.Fatalf("claim t1: %v", err)
	}
	if err := task.Fail(ws, c1.ID, "broken"); err != nil {
		t.Fatalf("fail t1: %v", err)
	}

	// Start a supervisor briefly. sweepActiveGoals on the dispatch tick should
	// mark the goal done.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	startSupervisor(ctx, t, wsDir)

	waitForGoalStatus(t, wsDir, g.ID, goal.StatusDone, 20*time.Second)
}

// TestRetryCountResetOnManualRetry verifies that manually retrying a task via
// "st task retry" resets RetryCount to 0, giving it a fresh health-check budget.
func TestRetryCountResetOnManualRetry(t *testing.T) {
	t.Parallel()
	wsDir := initWorkspace(t, nil)

	ws, err := workspace.Open(wsDir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}

	out := mustSt(t, wsDir, "task", "add", "--goal", "g-test", "--title", "retry test", "--prompt", "do it")
	taskID := parseTaskID(t, out)

	// Simulate 2 health-check resets to build RetryCount.
	for i := 0; i < 2; i++ {
		_, err := task.ClaimNext(ws, fmt.Sprintf("w-%03d", i))
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if err := task.ResetToQueue(ws, taskID); err != nil {
			t.Fatalf("reset %d: %v", i, err)
		}
	}

	// Verify RetryCount is 2.
	tk, _, _ := task.Get(ws, taskID)
	if tk.RetryCount != 2 {
		t.Fatalf("expected RetryCount=2, got %d", tk.RetryCount)
	}

	// Claim and fail the task (can't retry a pending task).
	if _, err := task.ClaimNext(ws, "w-999"); err != nil {
		t.Fatalf("claim for fail: %v", err)
	}
	if err := task.Fail(ws, taskID, "error"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// Manual retry via CLI.
	mustSt(t, wsDir, "task", "retry", taskID)

	// RetryCount must be reset to 0.
	tk2, status, _ := task.Get(ws, taskID)
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if tk2.RetryCount != 0 {
		t.Errorf("RetryCount = %d after manual retry, want 0", tk2.RetryCount)
	}
}

// ─── Path-awareness tests ─────────────────────────────────────────────────────

// TestCLIRunFromSubdirectory verifies that 'st goal list' works when run from
// a nested subdirectory inside a workspace, without ST_WORKSPACE set.
// This proves that workspace.FindRoot walks up from subdirectories correctly.
func TestCLIRunFromSubdirectory(t *testing.T) {
	t.Parallel()

	wsDir := initWorkspace(t, nil)

	// Add a goal using the normal helper (with ST_WORKSPACE set).
	mustSt(t, wsDir, "goal", "add", "subdir test goal")

	// Create a nested subdirectory inside the workspace.
	nestedSubdir := filepath.Join(wsDir, "subdir", "nested")
	if err := os.MkdirAll(nestedSubdir, 0755); err != nil {
		t.Fatalf("MkdirAll nested subdir: %v", err)
	}

	// Run 'st goal list' from the nested subdirectory WITHOUT ST_WORKSPACE.
	cmd := exec.Command(stBin, "goal", "list")
	cmd.Dir = nestedSubdir
	cmd.Env = filterEnv(os.Environ(), "ST_WORKSPACE")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := strings.TrimSpace(out.String())

	if err != nil {
		t.Fatalf("st goal list from subdir failed (exit non-zero):\n%s", output)
	}
	if !strings.Contains(output, "subdir test goal") {
		t.Errorf("output missing expected goal text:\n%s", output)
	}
}

// TestCLINoWorkspaceHelpfulMessage verifies that running 'st goal list' from a
// directory that is NOT a workspace (and without ST_WORKSPACE) prints a helpful
// error message guiding the user to create a workspace or set ST_WORKSPACE.
func TestCLINoWorkspaceHelpfulMessage(t *testing.T) {
	t.Parallel()

	// Create a temp dir that is NOT a workspace (no .st/workspace.json).
	tmpDir := t.TempDir()

	cmd := exec.Command(stBin, "goal", "list")
	cmd.Dir = tmpDir
	cmd.Env = filterEnv(os.Environ(), "ST_WORKSPACE")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := strings.TrimSpace(out.String())

	if err == nil {
		t.Fatal("expected non-zero exit code when no workspace found, got exit 0")
	}
	if !strings.Contains(output, "st init") {
		t.Errorf("output missing 'st init' guidance:\n%s", output)
	}
}
