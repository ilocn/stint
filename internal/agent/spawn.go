package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/user/stint/internal/gitutil"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/workspace"
)

// filteredEnv returns os.Environ() with the named keys removed.
// This is used to prevent environment variables like CLAUDECODE from being
// inherited by child claude processes, which would cause them to refuse to
// start when the supervisor itself is running inside Claude Code.
func filteredEnv(remove ...string) []string {
	skip := make(map[string]bool, len(remove))
	for _, k := range remove {
		skip[k] = true
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 && skip[e[:idx]] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// workerEnv returns the environment for worker (non-interactive) child processes.
// It calls filteredEnv to strip CLAUDECODE, then prepends the directory of the
// currently-running 'st' binary to PATH so that workers can run 'st heartbeat'
// and 'st task-done' without requiring st to be in the system PATH.
func workerEnv() []string {
	env := filteredEnv("CLAUDECODE")
	exe, err := os.Executable()
	if err != nil {
		return env // graceful fallback: PATH unchanged
	}
	stDir := filepath.Dir(exe)
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + stDir + string(os.PathListSeparator) + e[len("PATH="):]
			return env
		}
	}
	// PATH absent from env (unusual) — append it
	return append(env, "PATH="+stDir)
}

// SpawnWorker spawns a claude -p process for the given task.
// It sets up the git worktree, builds the prompt, and starts the process.
// Returns the started process or an error.
func SpawnWorker(ws *workspace.Workspace, t *task.Task, workerID string) (*os.Process, error) {
	def, err := Get(ws, t.Agent)
	if err != nil {
		return nil, fmt.Errorf("loading agent %s: %w", t.Agent, err)
	}

	// Set up git worktree if repo is configured.
	worktreePath := ws.WorktreePath(t.ID)
	if t.Repo != "" {
		repoPath, ok := ws.Config.Repos[t.Repo]
		if !ok {
			return nil, fmt.Errorf("unknown repo: %s", t.Repo)
		}
		base := t.GoalBranch
		if base == "" {
			base = gitutil.DefaultBranch(repoPath)
		}
		if err := gitutil.WorktreeAdd(repoPath, worktreePath, t.BranchName, base); err != nil {
			return nil, fmt.Errorf("creating worktree: %w", err)
		}
	} else {
		if err := os.MkdirAll(worktreePath, 0755); err != nil {
			return nil, err
		}
	}

	// Build the full prompt.
	prompt, err := BuildPrompt(ws, t, def)
	if err != nil {
		return nil, fmt.Errorf("building prompt: %w", err)
	}

	// Build claude command args.
	// --verbose is required when using --print with --output-format=stream-json
	// (claude 2.1+ enforces this).
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", fmt.Sprintf("%d", def.MaxTurns),
	}
	// Map permissionMode to CLI flags.
	switch def.PermissionMode {
	case "bypassPermissions":
		args = append(args, "--dangerously-skip-permissions")
	case "acceptEdits":
		// No dedicated flag; acceptEdits is the default interactive behavior.
	case "plan":
		// plan mode: no write operations.
	}
	if len(def.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(def.Tools, ","))
	}
	if len(def.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(def.DisallowedTools, ","))
	}

	// Open log file.
	logFile, err := os.Create(ws.LogPath(t.ID))
	if err != nil {
		return nil, fmt.Errorf("creating log file: %w", err)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = worktreePath
	cmd.Env = workerEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	// Close log file in parent (child has it via fork).
	logFile.Close()

	return cmd.Process, nil
}

// SpawnPlannerBackground spawns a headless claude -p planner process.
func SpawnPlannerBackground(ws *workspace.Workspace, goalID, goalText string, hints []string, workerID string) (*os.Process, error) {
	prompt, err := BuildPlannerPrompt(ws, goalID, goalText, hints)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-p", prompt,
		"--model", "opusplan",
		"--dangerously-skip-permissions", // planners need bypass to use st task add etc.
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "100",
	}

	logFile, err := os.Create(ws.LogPath("planner-" + goalID))
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = ws.Root
	cmd.Env = workerEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("starting planner: %w", err)
	}
	logFile.Close()
	return cmd.Process, nil
}

// SpawnRemediationPlannerBackground spawns a headless claude -p remediation planner process.
// It is triggered by the supervisor when a review task's verdict is "fail".
// The planner creates fix tasks and exits immediately (single-pass).
func SpawnRemediationPlannerBackground(ws *workspace.Workspace, goalID, remediationContext, workerID string) (*os.Process, error) {
	prompt, err := BuildRemediationPlannerPrompt(ws, goalID, remediationContext)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-p", prompt,
		"--model", "opusplan",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "30", // remediation planners are single-pass: explore → create tasks → exit
	}

	logFile, err := os.Create(ws.LogPath("remediation-planner-" + goalID))
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = ws.Root
	cmd.Env = workerEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("starting remediation planner: %w", err)
	}
	logFile.Close()
	return cmd.Process, nil
}

// buildPlannerInteractiveArgs returns the claude CLI args for interactive planner mode.
// Extracted for testability.
func buildPlannerInteractiveArgs(prompt string) []string {
	return []string{
		"-p", prompt,
		"--model", "opusplan",
		"--dangerously-skip-permissions",
		"--max-turns", "100",
	}
}

// SpawnPlannerInteractive runs claude interactively in the foreground.
// This blocks until the planner exits.
func SpawnPlannerInteractive(ws *workspace.Workspace, goalID, goalText string, hints []string) error {
	prompt, err := BuildPlannerPrompt(ws, goalID, goalText, hints)
	if err != nil {
		return err
	}

	args := buildPlannerInteractiveArgs(prompt)

	cmd := exec.Command("claude", args...)
	cmd.Dir = ws.Root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
