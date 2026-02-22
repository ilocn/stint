package task

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/user/stint/internal/gitutil"
	"github.com/user/stint/internal/workspace"
)

// Done moves a running task to done with the given summary,
// then unblocks any tasks whose dependencies are now all met.
func Done(ws *workspace.Workspace, id, summary, branch string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	if status != StatusRunning {
		return fmt.Errorf("task %s is %s, not running", id, status)
	}
	now := time.Now().UnixNano()
	t.CompletedAt = now
	t.UpdatedAt = now
	t.Result = &Result{Summary: summary, Branch: branch}

	// Pre-compute diff at completion time so BuildPrompt can use it without
	// re-running git at spawn time (which would be after worktree cleanup).
	branchToUse := branch
	if branchToUse == "" {
		branchToUse = t.BranchName
	}
	if branchToUse != "" && t.GoalBranch != "" && t.Repo != "" {
		if repoPath, ok := ws.Config.Repos[t.Repo]; ok {
			if diff, diffErr := gitutil.Diff(repoPath, branchToUse, t.GoalBranch); diffErr == nil {
				t.Result.Diff = diff
			}
		}
	}

	if err := writeToDir(ws.DoneDir(), t); err != nil {
		return err
	}
	os.Remove(ws.RunningPath(id))
	os.Remove(ws.HeartbeatPath(id))
	// Move any blocked tasks that are now unblocked into pending.
	if err := UnblockReady(ws); err != nil {
		slog.Error("unblock tasks failed", slog.String("task_id", id), slog.Any("error", err))
	}
	return nil
}

// Fail moves a running task to failed with the given reason.
func Fail(ws *workspace.Workspace, id, reason string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	if status != StatusRunning {
		return fmt.Errorf("task %s is %s, not running", id, status)
	}
	now := time.Now().UnixNano()
	t.CompletedAt = now
	t.UpdatedAt = now
	t.ErrorMsg = reason

	// Capture in-progress diff so RetryWithContext can show what was attempted.
	if t.BranchName != "" && t.GoalBranch != "" && t.Repo != "" {
		if repoPath, ok := ws.Config.Repos[t.Repo]; ok {
			if diff, diffErr := gitutil.Diff(repoPath, t.BranchName, t.GoalBranch); diffErr == nil {
				t.AttemptedDiff = diff
			}
		}
	}

	if err := writeToDir(ws.FailedDir(), t); err != nil {
		return err
	}
	os.Remove(ws.RunningPath(id))
	os.Remove(ws.HeartbeatPath(id))
	return nil
}

// Block moves a running task to blocked with the given reason.
func Block(ws *workspace.Workspace, id, reason string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	if status != StatusRunning {
		return fmt.Errorf("task %s is %s, not running", id, status)
	}
	t.UpdatedAt = time.Now().UnixNano()
	t.ErrorMsg = reason

	if err := writeToDir(ws.BlockedDir(), t); err != nil {
		return err
	}
	os.Remove(ws.RunningPath(id))
	os.Remove(ws.HeartbeatPath(id))
	return nil
}

// Cancel moves a pending or blocked task to cancelled.
func Cancel(ws *workspace.Workspace, id string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	switch status {
	case StatusPending, StatusBlocked:
		// ok
	default:
		return fmt.Errorf("task %s is %s; can only cancel pending or blocked tasks", id, status)
	}
	t.UpdatedAt = time.Now().UnixNano()

	if err := writeToDir(ws.CancelledDir(), t); err != nil {
		return err
	}
	switch status {
	case StatusPending:
		os.Remove(ws.PendingPath(id))
	case StatusBlocked:
		os.Remove(ws.BlockedPath(id))
	}
	return nil
}

// Retry moves a failed or blocked task back to pending (or blocked if deps
// still unmet) for re-execution.
func Retry(ws *workspace.Workspace, id string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	switch status {
	case StatusFailed, StatusBlocked:
		// ok
	default:
		return fmt.Errorf("task %s is %s; can only retry failed or blocked tasks", id, status)
	}
	// Reset execution fields including RetryCount so manual retries start fresh.
	t.WorkerID = ""
	t.ClaimedAt = 0
	t.CompletedAt = 0
	t.ErrorMsg = ""
	t.RetryCount = 0
	t.UpdatedAt = time.Now().UnixNano()

	// Re-check deps: go to blocked if deps still unmet, pending if ready.
	var destDir string
	if len(t.DepTaskIDs) > 0 && !allDepsDone(ws, t.DepTaskIDs) {
		destDir = ws.BlockedDir()
	} else {
		destDir = ws.PendingDir()
	}
	if err := writeToDir(destDir, t); err != nil {
		return err
	}
	// Only remove the source file when it moves to a different directory.
	// If status==blocked and destDir==blocked, writeToDir already updated it
	// in place; removing it would lose the task.
	switch status {
	case StatusFailed:
		os.Remove(ws.FailedPath(id))
	case StatusBlocked:
		if destDir != ws.BlockedDir() {
			os.Remove(ws.BlockedPath(id))
		}
	}
	return nil
}

// RetryWithContext retries a failed (or running) task, injecting the previous error
// as context into the prompt so the agent can self-correct.
// It increments ExplicitFailCount and appends the error to FailureHistory.
// If ExplicitFailCount >= MaxExplicitFailRetries, it returns an error and does NOT retry.
func RetryWithContext(ws *workspace.Workspace, id, previousError string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	switch status {
	case StatusFailed, StatusRunning:
		// ok — supervisor may call this on a running task (instead of Fail)
	default:
		return fmt.Errorf("task %s cannot be retried with context (status=%s, must be failed or running)", id, status)
	}
	if t.ExplicitFailCount >= MaxExplicitFailRetries {
		return fmt.Errorf("task %s has exceeded max explicit fail retries (%d)", id, MaxExplicitFailRetries)
	}
	t.ExplicitFailCount++
	t.FailureHistory = append(t.FailureHistory, previousError)
	// Prepend failure context to prompt so agent can self-correct.
	// Include attempted diff if available so agent can see what code it tried.
	var header string
	if t.AttemptedDiff != "" {
		diff := t.AttemptedDiff
		if len(diff) > 4000 {
			diff = diff[:4000] + "\n... (truncated)"
		}
		header = fmt.Sprintf("## Previous Attempt Failed (attempt %d)\nError: %s\nAttempted changes:\n```diff\n%s\n```\n\n", t.ExplicitFailCount, previousError, diff)
	} else {
		header = fmt.Sprintf("## Previous Attempt Failed (attempt %d)\nError: %s\n\n", t.ExplicitFailCount, previousError)
	}
	t.Prompt = header + t.Prompt
	t.AttemptedDiff = "" // clear after injection — it's now embedded in the prompt
	// Reset execution fields.
	t.WorkerID = ""
	t.ClaimedAt = 0
	t.CompletedAt = 0
	t.ErrorMsg = ""
	t.UpdatedAt = time.Now().UnixNano()

	// Re-check deps: go to blocked if unmet, else pending.
	var destDir string
	if len(t.DepTaskIDs) > 0 && !allDepsDone(ws, t.DepTaskIDs) {
		destDir = ws.BlockedDir()
	} else {
		destDir = ws.PendingDir()
	}
	if err := writeToDir(destDir, t); err != nil {
		return err
	}
	// Remove from source directory.
	switch status {
	case StatusRunning:
		_ = os.Remove(ws.RunningPath(id))
	case StatusFailed:
		_ = os.Remove(ws.FailedPath(id))
	}
	_ = os.Remove(ws.HeartbeatPath(id))
	slog.Info("task retried with context", slog.String("task_id", id), slog.Int("attempt", t.ExplicitFailCount), slog.Int("max_attempts", MaxExplicitFailRetries))
	return nil
}

// Skip moves a failed or blocked task to done, allowing downstream dependents to proceed.
// Result summary is set to "[SKIPPED] " + reason.
func Skip(ws *workspace.Workspace, id, reason string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	switch status {
	case StatusFailed, StatusBlocked:
		// ok
	default:
		return fmt.Errorf("task %s cannot be skipped (status=%s, must be failed or blocked)", id, status)
	}
	now := time.Now().UnixNano()
	t.Result = &Result{Summary: "[SKIPPED] " + reason}
	t.CompletedAt = now
	t.UpdatedAt = now
	if err := writeToDir(ws.DoneDir(), t); err != nil {
		return err
	}
	switch status {
	case StatusFailed:
		_ = os.Remove(ws.FailedPath(id))
	case StatusBlocked:
		_ = os.Remove(ws.BlockedPath(id))
	}
	_ = os.Remove(ws.HeartbeatPath(id))
	slog.Info("task skipped", slog.String("task_id", id), slog.String("reason", reason))
	return UnblockReady(ws)
}

// ResetToQueue moves a running task back to pending or blocked (used by recovery).
// Re-checks dependencies: if deps are unmet the task goes to blocked/, otherwise pending/.
// Increments RetryCount on each call; if it reaches MaxResetRetries the task is
// permanently moved to failed/ instead of being re-queued.
func ResetToQueue(ws *workspace.Workspace, id string) error {
	t, status, err := Get(ws, id)
	if err != nil {
		return err
	}
	if status != StatusRunning {
		return fmt.Errorf("task %s is %s, not running", id, status)
	}
	t.WorkerID = ""
	t.ClaimedAt = 0
	t.UpdatedAt = time.Now().UnixNano()
	t.RetryCount++

	if t.RetryCount >= MaxResetRetries {
		// Exceeded retry limit — permanently fail the task.
		t.ErrorMsg = fmt.Sprintf("exceeded max retries (%d): worker killed by health check each time", MaxResetRetries)
		t.CompletedAt = time.Now().UnixNano()
		if err := writeToDir(ws.FailedDir(), t); err != nil {
			return err
		}
		os.Remove(ws.RunningPath(id))
		os.Remove(ws.HeartbeatPath(id))
		return nil
	}

	var destDir string
	if len(t.DepTaskIDs) > 0 && !allDepsDone(ws, t.DepTaskIDs) {
		destDir = ws.BlockedDir()
	} else {
		destDir = ws.PendingDir()
	}
	if err := writeToDir(destDir, t); err != nil {
		return err
	}
	os.Remove(ws.RunningPath(id))
	os.Remove(ws.HeartbeatPath(id))
	return nil
}
