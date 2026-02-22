package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/user/stint/internal/agent"
	"github.com/user/stint/internal/gitutil"
	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/logbuf"
	"github.com/user/stint/internal/logger"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/web"
	"github.com/user/stint/internal/worker"
	"github.com/user/stint/internal/workspace"
)

// isTTY returns true if stdout is a real terminal.
// Checks NO_COLOR env and TERM=dumb per clig.dev guidelines.
func isTTY() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGray   = "\033[90m"
)

type statusStyle struct{ symbol, color string }

var statusStyles = map[string]statusStyle{
	task.StatusDone:      {"✓", ansiGreen},
	task.StatusFailed:    {"✗", ansiRed},
	task.StatusBlocked:   {"⚠", ansiYellow},
	task.StatusRunning:   {"↻", ansiCyan},
	task.StatusPending:   {"○", ansiGray},
	task.StatusCancelled: {"⊘", ansiGray},
}

// statusSortOrder defines the display order: failed first, cancelled last.
var statusSortOrder = map[string]int{
	task.StatusFailed:    0,
	task.StatusBlocked:   1,
	task.StatusRunning:   2,
	task.StatusPending:   3,
	task.StatusDone:      4,
	task.StatusCancelled: 5,
}

// dispatchInterval and healthInterval are package-level variables (not
// constants) so tests can override them without waiting for real tick
// intervals to fire.
var (
	dispatchInterval = 5 * time.Second
	healthInterval   = 30 * time.Second
)

const workerTimeoutSec = 600 // 10 minutes — allows time for long-running test suites

// Run starts the supervisor loops. Blocks until ctx is cancelled or error.
// If webPort > 0, the web dashboard is started on that port.
// Prints initial status to stdout then streams log output.
func Run(ctx context.Context, ws *workspace.Workspace, maxConcurrent int, webPort int) error {
	slog.Info("supervisor starting", slog.Int("max_concurrent", maxConcurrent))

	lb := logbuf.New(500)

	if webPort > 0 {
		addr := fmt.Sprintf(":%d", webPort)
		slog.Info("web dashboard starting", slog.Int("port", webPort))
		go func() {
			if err := web.Serve(ctx, ws, addr, lb); err != nil {
				slog.Error("web server error", slog.Any("error", err))
			}
		}()
		fmt.Printf("Web UI: http://localhost:%d\n", webPort)
	}

	// Run startup recovery pass.
	if err := startupRecovery(ws); err != nil {
		slog.Warn("startup recovery error", slog.Any("error", err))
	}

	return runWithTUI(ctx, ws, "", maxConcurrent, lb)
}

// runWithTUI streams log output to stdout.
// goalID="" means all-goals mode; non-empty goalID means single-goal mode
// (exits when the goal reaches done or failed).
func runWithTUI(parentCtx context.Context, ws *workspace.Workspace, goalID string, maxConcurrent int, lb *logbuf.LogBuf) error {
	// Direct log output to logbuf (in addition to stderr) for the web dashboard.
	if lb != nil {
		logger.SetLogBuf(lb)
		defer logger.SetLogBuf(nil)
	}

	// Run the dispatch loop directly (blocking).
	if goalID == "" {
		return supervisorDispatchLoop(parentCtx, ws, maxConcurrent)
	}
	return goalDispatchLoop(parentCtx, ws, goalID, maxConcurrent)
}

// supervisorDispatchLoop is the all-goals dispatch/health loop used by runWithTUI.
// Exits when ctx is cancelled.
func supervisorDispatchLoop(ctx context.Context, ws *workspace.Workspace, maxConcurrent int) error {
	dispatchTick := time.NewTicker(dispatchInterval)
	healthTick := time.NewTicker(healthInterval)
	defer dispatchTick.Stop()
	defer healthTick.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("supervisor shutting down")
			return nil
		case <-dispatchTick.C:
			if err := dispatchGoals(ctx, ws, maxConcurrent); err != nil {
				slog.Error("goal dispatch failed", slog.Any("error", err))
			}
			if err := dispatchTasks(ctx, ws, maxConcurrent); err != nil {
				slog.Error("task dispatch failed", slog.Any("error", err))
			}
			sweepActiveGoals(ws)
		case <-healthTick.C:
			if err := healthCheck(ws); err != nil {
				slog.Error("health check failed", slog.Any("error", err))
			}
		}
	}
}

// goalDispatchLoop is the single-goal dispatch loop for RunGoal + TUI.
// Returns nil on goal done, non-nil on goal failed, or nil on ctx cancel.
func goalDispatchLoop(ctx context.Context, ws *workspace.Workspace, goalID string, maxConcurrent int) error {
	dispatchTick := time.NewTicker(dispatchInterval)
	healthTick := time.NewTicker(healthInterval)
	defer dispatchTick.Stop()
	defer healthTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-dispatchTick.C:
			if err := dispatchGoals(ctx, ws, maxConcurrent); err != nil {
				slog.Error("goal dispatch failed", slog.Any("error", err))
			}
			if err := dispatchTasks(ctx, ws, maxConcurrent); err != nil {
				slog.Error("task dispatch failed", slog.Any("error", err))
			}
			sweepActiveGoals(ws)
			g, err := goal.Get(ws, goalID)
			if err != nil {
				return err
			}
			switch g.Status {
			case goal.StatusDone:
				slog.Info("goal completed", slog.String("goal_id", goalID))
				return nil
			case goal.StatusFailed:
				return fmt.Errorf("goal %s failed", goalID)
			}
		case <-healthTick.C:
			if err := healthCheck(ws); err != nil {
				slog.Error("health check failed", slog.Any("error", err))
			}
		}
	}
}

// sweepActiveGoals calls checkGoalCompletion for every active goal. This
// catches goals whose final tasks completed/failed while the supervisor was
// not running, or where checkGoalCompletion was never triggered.
func sweepActiveGoals(ws *workspace.Workspace) {
	goals, err := goal.List(ws)
	if err != nil {
		slog.Error("listing goals failed", slog.Any("error", err))
		return
	}
	for _, g := range goals {
		if g.Status == goal.StatusActive {
			checkGoalCompletion(ws, g.ID)
		}
	}
}

// dispatchGoals finds queued goals and spawns background Planners for them,
// up to maxConcurrent simultaneous planners.
// Goals on the same repo run concurrently — each owns an isolated goal branch,
// and conflicts surface at merge time (handled by the merge agent).
func dispatchGoals(ctx context.Context, ws *workspace.Workspace, maxConcurrent int) error {
	planners, err := worker.CountPlanners(ws)
	if err != nil {
		return err
	}
	if planners >= maxConcurrent {
		return nil
	}

	goals, err := goal.List(ws)
	if err != nil {
		return err
	}

	for _, g := range goals {
		if planners >= maxConcurrent {
			break
		}
		if g.Status != goal.StatusQueued {
			continue
		}

		slog.Info("spawning planner", slog.String("goal_id", g.ID), slog.String("goal_text", g.Text))

		// Mark as planning before spawning so we don't double-dispatch.
		if err := goal.UpdateStatus(ws, g.ID, goal.StatusPlanning); err != nil {
			slog.Error("mark goal planning failed", slog.String("goal_id", g.ID), slog.Any("error", err))
			continue
		}

		workerID := worker.NewID()
		proc, err := agent.SpawnPlannerBackground(ws, g.ID, g.Text, g.Hints, workerID)
		if err != nil {
			slog.Error("spawn planner failed", slog.String("goal_id", g.ID), slog.Any("error", err))
			// Roll back to queued.
			if rbErr := goal.UpdateStatus(ws, g.ID, goal.StatusQueued); rbErr != nil {
				slog.Error("goal rollback failed", slog.String("goal_id", g.ID), slog.Any("error", rbErr))
			}
			continue
		}

		w := &worker.Worker{
			ID:          workerID,
			PID:         proc.Pid,
			TaskID:      "planner-" + g.ID,
			Agent:       "planner",
			StartedAt:   time.Now().Unix(),
			HeartbeatAt: time.Now().Unix(),
		}
		if err := worker.Register(ws, w); err != nil {
			slog.Error("register planner worker failed", slog.Any("error", err))
		}

		planners++

		// Monitor planner in background.
		go monitorPlanner(ctx, ws, g.ID, workerID, proc)
	}
	return nil
}

// waitResult holds the outcome of an os.Process.Wait call.
type waitResult struct {
	state *os.ProcessState
	err   error
}

// monitorPlanner waits for a planner process to exit and updates the goal status.
func monitorPlanner(ctx context.Context, ws *workspace.Workspace, goalID, workerID string, proc *os.Process) {
	ch := make(chan waitResult, 1)
	go func() {
		state, err := proc.Wait()
		ch <- waitResult{state, err}
	}()

	var result waitResult
	select {
	case result = <-ch:
		// normal exit
	case <-ctx.Done():
		_ = proc.Kill()
		result = <-ch // drain so the inner goroutine exits
	}

	if err := worker.Delete(ws, workerID); err != nil {
		slog.Error("delete planner worker failed", slog.String("worker_id", workerID), slog.Any("error", err))
	}

	// If we were shut down, reset goal back to queued so next supervisor run re-plans it.
	if ctx.Err() != nil {
		if err := goal.UpdateStatus(ws, goalID, goal.StatusQueued); err != nil {
			slog.Error("reset goal to queued failed", slog.String("goal_id", goalID), slog.Any("error", err))
		}
		return
	}

	if result.err != nil || (result.state != nil && !result.state.Success()) {
		exitCode := -1
		if result.state != nil {
			exitCode = result.state.ExitCode()
		}
		slog.Warn("planner exited with error", slog.String("goal_id", goalID), slog.Any("error", result.err), slog.Int("exit_code", exitCode))
		handlePlannerError(ws, goalID)
		return
	}
	slog.Info("planner completed", slog.String("goal_id", goalID))
	handlePlannerSuccess(ws, goalID)
}

// handlePlannerSuccess determines the goal transition when a planner exits 0.
// If the planner created tasks, the goal transitions to active so those tasks
// can be dispatched. If no tasks were created (nothing to do), the goal is
// marked done immediately.
func handlePlannerSuccess(ws *workspace.Workspace, goalID string) {
	tasks, listErr := task.ListForGoal(ws, goalID)
	if listErr == nil && len(tasks) > 0 {
		slog.Info("planner created tasks", slog.String("goal_id", goalID), slog.Int("task_count", len(tasks)))
		if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
			slog.Error("mark goal active failed", slog.String("goal_id", goalID), slog.Any("error", err))
		}
		checkGoalCompletion(ws, goalID) // in case tasks are already done
		return
	}
	// Planner exited 0 but created no tasks — nothing to do.
	if err := goal.UpdateStatus(ws, goalID, goal.StatusDone); err != nil {
		slog.Error("mark goal done failed", slog.String("goal_id", goalID), slog.Any("error", err))
	}
}

// handlePlannerError determines the goal transition when a planner exits with
// a non-zero exit code. If the planner created any tasks before crashing, those
// tasks can still be dispatched, so the goal transitions to active. If no tasks
// were created the goal has nothing to run and is marked failed.
func handlePlannerError(ws *workspace.Workspace, goalID string) {
	tasks, listErr := task.ListForGoal(ws, goalID)
	if listErr == nil && len(tasks) > 0 {
		slog.Warn("planner created tasks before error exit", slog.String("goal_id", goalID), slog.Int("task_count", len(tasks)))
		if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
			slog.Error("mark goal active failed", slog.String("goal_id", goalID), slog.Any("error", err))
		}
		return
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusFailed); err != nil {
		slog.Error("goal marked failed", slog.String("goal_id", goalID), slog.Any("error", err))
	}
}

// checkGoalCompletion marks a goal done when all its tasks have reached a
// terminal state (done, failed, or cancelled). A goal whose final merge task
// succeeded is considered complete even if intermediate steps failed (e.g., a
// review merge-task that found no commits to merge).
func checkGoalCompletion(ws *workspace.Workspace, goalID string) {
	tasks, err := task.ListForGoal(ws, goalID)
	if err != nil || len(tasks) == 0 {
		return
	}
	for _, ts := range tasks {
		switch ts.Status {
		case task.StatusDone, task.StatusFailed, task.StatusCancelled:
			// terminal — ok
		default:
			return // still in progress
		}
	}
	if err := goal.UpdateStatus(ws, goalID, goal.StatusDone); err != nil {
		slog.Error("mark goal done failed", slog.String("goal_id", goalID), slog.Any("error", err))
		return
	}
	slog.Info("goal completed", slog.String("goal_id", goalID))
}

// dispatchTasks claims pending tasks and spawns Workers for them.
// Only task workers (not planners) count against maxConcurrent.
func dispatchTasks(ctx context.Context, ws *workspace.Workspace, maxConcurrent int) error {
	current, err := worker.CountTaskWorkers(ws)
	if err != nil {
		return err
	}
	if current >= maxConcurrent {
		return nil
	}

	for {
		if current >= maxConcurrent {
			break
		}

		workerID := worker.NewID()
		t, err := task.ClaimNext(ws, workerID)
		if err == task.ErrNoTask {
			break
		}
		if err != nil {
			return fmt.Errorf("claiming task: %w", err)
		}

		slog.Info("task dispatched", slog.String("task_id", t.ID), slog.String("title", t.Title), slog.String("worker_id", workerID))

		// Set up goal branch if needed (idempotent: creates branch if missing,
		// preserves already-set GoalBranch/BranchName from st task add).
		if t.Repo != "" {
			if err := setupGoalBranch(ws, t); err != nil {
				slog.Error("setup goal branch failed", slog.String("task_id", t.ID), slog.Any("error", err))
			}
		}

		proc, err := agent.SpawnWorker(ws, t, workerID)
		if err != nil {
			slog.Error("spawn worker failed", slog.String("task_id", t.ID), slog.Any("error", err))
			// Reset task to pending.
			if rstErr := task.ResetToQueue(ws, t.ID); rstErr != nil {
				slog.Error("reset task to queue failed", slog.String("task_id", t.ID), slog.Any("error", rstErr))
			}
			continue
		}

		// Write heartbeat immediately so the health loop has a baseline.
		if err := task.WriteHeartbeat(ws, t.ID); err != nil {
			slog.Error("write heartbeat failed", slog.String("task_id", t.ID), slog.Any("error", err))
		}

		w := &worker.Worker{
			ID:          workerID,
			PID:         proc.Pid,
			TaskID:      t.ID,
			Agent:       t.Agent,
			StartedAt:   time.Now().Unix(),
			HeartbeatAt: time.Now().Unix(),
		}
		if err := worker.Register(ws, w); err != nil {
			slog.Error("register worker failed", slog.String("worker_id", workerID), slog.Any("error", err))
		}

		current++

		// Monitor in background.
		go monitorWorker(ctx, ws, t.ID, workerID, proc)
	}
	return nil
}

// setupGoalBranch ensures the goal integration branch exists in the repo and
// updates the running task file with branch names.
// Idempotent: creates the git branch only if missing; only updates task fields
// if they are not already populated (preserves values set by st task add or planner).
// Note: modifies t.GoalBranch and t.BranchName as a side effect.
func setupGoalBranch(ws *workspace.Workspace, t *task.Task) error {
	repoPath, ok := ws.Config.Repos[t.Repo]
	if !ok {
		return fmt.Errorf("unknown repo: %s", t.Repo)
	}
	// Use existing GoalBranch if set, otherwise derive from goal ID.
	goalBranch := t.GoalBranch
	if goalBranch == "" {
		goalBranch = "st/goals/" + t.GoalID
	}
	if !gitutil.BranchExists(repoPath, goalBranch) {
		base := gitutil.DefaultBranch(repoPath)
		if err := gitutil.CreateBranch(repoPath, goalBranch, base); err != nil {
			return err
		}
	}
	// Only set fields if not already populated (preserves planner-set values).
	if t.GoalBranch == "" {
		t.GoalBranch = goalBranch
	}
	if t.BranchName == "" {
		t.BranchName = "st/tasks/" + t.ID
	}
	// Write updated task directly to running/ (it was already claimed there).
	return updateRunningTask(ws, t)
}

// updateRunningTask overwrites the running task file atomically.
func updateRunningTask(ws *workspace.Workspace, t *task.Task) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := ws.RunningPath(t.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// monitorWorker waits for a worker process to exit and transitions the task.
func monitorWorker(ctx context.Context, ws *workspace.Workspace, taskID, workerID string, proc *os.Process) {
	ch := make(chan waitResult, 1)
	go func() {
		state, err := proc.Wait()
		ch <- waitResult{state, err}
	}()

	var result waitResult
	select {
	case result = <-ch:
		// normal exit
	case <-ctx.Done():
		_ = proc.Kill()
		result = <-ch // drain so the inner goroutine exits
	}

	if err := worker.Delete(ws, workerID); err != nil {
		slog.Error("delete worker failed", slog.String("worker_id", workerID), slog.Any("error", err))
	}

	// Clean up worktree.
	if err := os.RemoveAll(ws.WorktreePath(taskID)); err != nil {
		slog.Error("remove worktree failed", slog.String("task_id", taskID), slog.Any("error", err))
	}

	// If we were shut down, don't update task state.
	if ctx.Err() != nil {
		return
	}

	// Check if task was already transitioned by the worker itself.
	currentTask, status, lookupErr := task.Get(ws, taskID)
	if lookupErr != nil || status == task.StatusRunning {
		if result.err != nil || (result.state != nil && !result.state.Success()) {
			// Non-zero exit (crash/error): fail immediately, no retry.
			slog.Error("worker exited with error", slog.String("task_id", taskID))
			errMsg := fmt.Sprintf("worker process exited with error: %v", result.err)
			if err := task.Fail(ws, taskID, errMsg); err != nil {
				slog.Error("mark task failed", slog.String("task_id", taskID), slog.Any("error", err))
			}
		} else {
			// Zero exit without reporting (likely hit --max-turns): retry with context if retries remain.
			errMsg := "worker exited without calling 'st task-done' or 'st task-fail'"
			retried := false
			if currentTask != nil && currentTask.ExplicitFailCount < task.MaxExplicitFailRetries {
				if retryErr := task.RetryWithContext(ws, taskID, errMsg); retryErr == nil {
					slog.Info("task auto-retried after silent exit",
						slog.String("task_id", taskID),
						slog.Int("attempt", currentTask.ExplicitFailCount+1),
						slog.Int("max_attempts", task.MaxExplicitFailRetries))
					retried = true
				} else {
					slog.Error("task retry failed", slog.String("task_id", taskID), slog.Any("error", retryErr))
				}
			} else {
				slog.Error("worker exited without status", slog.String("task_id", taskID))
			}
			if !retried {
				if err := task.Fail(ws, taskID, errMsg); err != nil {
					slog.Error("mark task failed", slog.String("task_id", taskID), slog.Any("error", err))
				}
			}
		}
	} else {
		switch status {
		case task.StatusDone, task.StatusCancelled:
			slog.Info("task completed", slog.String("task_id", taskID), slog.String("status", status))
			// Event-driven remediation: if a review task fails, spawn a focused fix planner.
			if status == task.StatusDone && currentTask != nil {
				handleReviewCompletion(ctx, ws, currentTask)
			}
		case task.StatusFailed:
			// Worker explicitly called 'st task fail' — apply auto-retry with context
			// so the agent can self-correct on the next attempt.
			if currentTask != nil && currentTask.ExplicitFailCount < task.MaxExplicitFailRetries {
				retryErr := task.RetryWithContext(ws, taskID, currentTask.ErrorMsg)
				if retryErr == nil {
					slog.Info("task auto-retried", slog.String("task_id", taskID),
						slog.Int("attempt", currentTask.ExplicitFailCount+1),
						slog.Int("max_attempts", task.MaxExplicitFailRetries))
				} else {
					slog.Error("task retry failed", slog.String("task_id", taskID), slog.Any("error", retryErr))
				}
			} else {
				slog.Warn("task explicitly failed", slog.String("task_id", taskID))
			}
		default:
			// Health check reset the task back to pending/blocked while the worker was running.
			t2, _, _ := task.Get(ws, taskID)
			retryCount, maxRetries := 0, task.MaxResetRetries
			if t2 != nil {
				retryCount = t2.RetryCount
			}
			slog.Warn("task reset by health check", slog.String("task_id", taskID),
				slog.String("status", status),
				slog.Int("retry_count", retryCount),
				slog.Int("max_retries", maxRetries))
		}
	}

	// Check if the goal is now fully done.
	t, _, _ := task.Get(ws, taskID)
	if t != nil {
		checkGoalCompletion(ws, t.GoalID)
	}
}

// handleReviewCompletion is called when a review task completes successfully.
// If the review's Result.Summary contains a structured JSON verdict of "fail",
// a remediation planner is spawned to create a focused fix cycle.
// This replaces the old manual-intervention model with an event-driven loop.
func handleReviewCompletion(ctx context.Context, ws *workspace.Workspace, t *task.Task) {
	// Only process review agents (names starting with "review").
	if !strings.HasPrefix(t.Agent, "review") {
		return
	}
	if t.Result == nil || t.Result.Summary == "" {
		return
	}

	// Try to parse a structured JSON verdict from the last line of the summary.
	lines := strings.Split(strings.TrimSpace(t.Result.Summary), "\n")
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	var verdict struct {
		Verdict string   `json:"verdict"`
		Issues  []string `json:"issues"`
		Summary string   `json:"summary"`
	}
	if err := json.Unmarshal([]byte(lastLine), &verdict); err != nil {
		return // not structured JSON — skip
	}
	if verdict.Verdict != "fail" {
		return
	}

	issueText := strings.Join(verdict.Issues, "; ")
	if issueText == "" {
		issueText = verdict.Summary
	}
	remediationContext := fmt.Sprintf(
		"Review task %s (%s) failed.\nIssues found:\n%s\n\nCreate a fix cycle: fix-impl → merge-task → re-review → merge-task.\nThe fix-impl task must use --context-from %s so it sees the review findings.",
		t.ID, t.Title, issueText, t.ID,
	)

	workerID := worker.NewID()
	proc, err := agent.SpawnRemediationPlannerBackground(ws, t.GoalID, remediationContext, workerID)
	if err != nil {
		slog.Error("spawn remediation planner failed", slog.String("goal_id", t.GoalID), slog.Any("error", err))
		return
	}

	w := &worker.Worker{
		ID:          workerID,
		PID:         proc.Pid,
		TaskID:      "planner-" + t.GoalID,
		Agent:       "planner",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		slog.Error("register remediation planner worker failed", slog.Any("error", err))
	}

	go monitorPlanner(ctx, ws, t.GoalID, workerID, proc)
	slog.Info("remediation planner spawned",
		slog.String("goal_id", t.GoalID),
		slog.String("task_id", t.ID),
		slog.String("issues", issueText))
}

// healthCheck scans for stale workers and resets their tasks.
func healthCheck(ws *workspace.Workspace) error {
	stale, err := worker.ListStale(ws, workerTimeoutSec)
	if err != nil {
		return err
	}
	for _, w := range stale {
		slog.Warn("stale worker detected", slog.String("worker_id", w.ID), slog.Int("pid", w.PID), slog.String("task_id", w.TaskID))

		// Kill the process if still alive.
		if worker.IsAlive(w.PID) {
			proc, err := os.FindProcess(w.PID)
			if err == nil {
				if killErr := proc.Kill(); killErr != nil {
					slog.Error("kill stale worker failed", slog.String("worker_id", w.ID), slog.Int("pid", w.PID), slog.Any("error", killErr))
				}
			}
		}

		// Remove worker record.
		if err := worker.Delete(ws, w.ID); err != nil {
			slog.Error("delete stale worker failed", slog.String("worker_id", w.ID), slog.Any("error", err))
		}

		// Skip planner processes (task ID starts with "planner-").
		if strings.HasPrefix(w.TaskID, "planner-") {
			goalID := w.TaskID[len("planner-"):]
			slog.Warn("planner timed out", slog.String("goal_id", goalID))
			if err := goal.UpdateStatus(ws, goalID, goal.StatusFailed); err != nil {
				slog.Error("goal marked failed", slog.String("goal_id", goalID), slog.Any("error", err))
			}
			continue
		}

		// Reset task to pending (or permanently fail if max retries exceeded).
		if err := task.ResetToQueue(ws, w.TaskID); err != nil {
			slog.Error("reset task failed", slog.String("task_id", w.TaskID), slog.Any("error", err))
		} else {
			// Check whether it was permanently failed (exceeded max retries).
			_, postStatus, _ := task.Get(ws, w.TaskID)
			if postStatus == task.StatusFailed {
				slog.Warn("task exceeded max retries", slog.String("task_id", w.TaskID))
			}
		}

		// Remove stale worktree.
		if err := os.RemoveAll(ws.WorktreePath(w.TaskID)); err != nil {
			slog.Error("remove stale worktree failed", slog.String("task_id", w.TaskID), slog.Any("error", err))
		}
	}
	return nil
}

// RunGoal runs the supervisor until the specified goal reaches done or failed.
// If webPort > 0, the web dashboard is started on that port.
// Prints initial status to stdout then streams log output.
func RunGoal(ctx context.Context, ws *workspace.Workspace, goalID string, maxConcurrent int, webPort int) error {
	slog.Info("supervisor starting", slog.String("goal_id", goalID), slog.Int("max_concurrent", maxConcurrent))

	lb := logbuf.New(500)

	if webPort > 0 {
		addr := fmt.Sprintf(":%d", webPort)
		slog.Info("web dashboard starting", slog.Int("port", webPort))
		go func() {
			if err := web.Serve(ctx, ws, addr, lb); err != nil {
				slog.Error("web server error", slog.Any("error", err))
			}
		}()
		fmt.Printf("Web UI: http://localhost:%d\n", webPort)
	}

	if err := startupRecovery(ws); err != nil {
		slog.Warn("startup recovery error", slog.Any("error", err))
	}

	return runWithTUI(ctx, ws, goalID, maxConcurrent, lb)
}

func printAllStatus(ws *workspace.Workspace) {
	goals, err := goal.List(ws)
	if err != nil {
		return
	}

	ts := time.Now().Format("15:04:05")

	if len(goals) == 0 {
		fmt.Printf("[%s] no goals\n", ts)
		return
	}

	fmt.Printf("\n[%s] status\n", ts)

	// Goals table.
	fmt.Println()
	fmt.Printf("%-20s  %-10s  %s\n", "GOAL", "STATUS", "TEXT")
	for _, g := range goals {
		text := g.Text
		if len(text) > 55 {
			text = text[:52] + "..."
		}
		fmt.Printf("%-20s  %-10s  %s\n", g.ID, g.Status, text)
	}

	// Tasks table.
	allTasks, err := task.ListAll(ws)
	if err != nil || len(allTasks) == 0 {
		fmt.Println("\nno tasks")
	} else {
		// Summary line.
		counts := make(map[string]int)
		for _, t := range allTasks {
			counts[t.Status]++
		}
		total := len(allTasks)
		summaryParts := []string{}
		for _, s := range []string{task.StatusDone, task.StatusRunning, task.StatusBlocked, task.StatusFailed, task.StatusPending, task.StatusCancelled} {
			if n := counts[s]; n > 0 {
				summaryParts = append(summaryParts, fmt.Sprintf("%d %s", n, s))
			}
		}
		fmt.Printf("\nTasks: %s (%d total)\n", strings.Join(summaryParts, " · "), total)

		// Sort: failed → blocked → running → pending → done → cancelled.
		sorted := make([]*task.TaskWithStatus, len(allTasks))
		copy(sorted, allTasks)
		sort.Slice(sorted, func(i, j int) bool {
			oi := statusSortOrder[sorted[i].Status]
			oj := statusSortOrder[sorted[j].Status]
			if oi != oj {
				return oi < oj
			}
			return sorted[i].Task.ID < sorted[j].Task.ID
		})

		tty := isTTY()
		fmt.Println()
		for _, tw := range sorted {
			t := tw.Task
			style, ok := statusStyles[tw.Status]
			if !ok {
				style = statusStyle{"?", ""}
			}
			sym := style.symbol
			if tty && style.color != "" {
				sym = style.color + sym + ansiReset
			}
			title := t.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			reason := ""
			if tw.Status != task.StatusDone && tw.Status != task.StatusCancelled {
				reason = t.ErrorMsg
				if reason == "" && (tw.Status == task.StatusBlocked || tw.Status == task.StatusFailed) {
					reason = "(no reason recorded)"
				}
			}
			line := fmt.Sprintf("  %s  %-14s  %-9s  %-8s  %s", sym, t.ID, tw.Status, t.Agent, title)
			if reason != "" {
				line += "  " + reason
			}
			fmt.Println(line)
		}
	}

	// Workers table.
	workers, _ := worker.List(ws)
	if len(workers) > 0 {
		fmt.Println()
		fmt.Printf("%-20s  %-6s  %-14s  %-10s  %s\n", "WORKER", "PID", "TASK", "AGENT", "HEARTBEAT")
		for _, w := range workers {
			hb := "none"
			if w.HeartbeatAt > 0 {
				d := time.Since(time.Unix(w.HeartbeatAt, 0))
				switch {
				case d < time.Minute:
					hb = fmt.Sprintf("%ds ago", int(d.Seconds()))
				case d < time.Hour:
					hb = fmt.Sprintf("%dm ago", int(d.Minutes()))
				default:
					hb = fmt.Sprintf("%dh ago", int(d.Hours()))
				}
			}
			fmt.Printf("%-20s  %-6d  %-14s  %-10s  %s\n", w.ID, w.PID, w.TaskID, w.Agent, hb)
		}
	}
	fmt.Println()
}

func printGoalStatus(ws *workspace.Workspace, goalID string) {
	tasks, err := task.ListForGoal(ws, goalID)
	if err != nil || len(tasks) == 0 {
		return
	}
	counts := make(map[string]int)
	for _, ts := range tasks {
		counts[ts.Status]++
	}
	total := len(tasks)
	done := counts[task.StatusDone]
	running := counts[task.StatusRunning]
	blocked := counts[task.StatusBlocked]
	failed := counts[task.StatusFailed]

	tty := isTTY()

	// Header line.
	header := fmt.Sprintf("[%s] goal %s · %d/%d done · %d running · %d blocked · %d failed",
		time.Now().Format("15:04:05"), goalID, done, total, running, blocked, failed)
	fmt.Println(header)

	// Sort tasks: failed → blocked → running → pending → done → cancelled.
	sorted := make([]*task.TaskWithStatus, len(tasks))
	copy(sorted, tasks)
	sort.Slice(sorted, func(i, j int) bool {
		oi := statusSortOrder[sorted[i].Status]
		oj := statusSortOrder[sorted[j].Status]
		if oi != oj {
			return oi < oj
		}
		return sorted[i].Task.ID < sorted[j].Task.ID
	})

	// Print per-task rows.
	for _, ts := range sorted {
		t := ts.Task
		style, ok := statusStyles[ts.Status]
		if !ok {
			style = statusStyle{"?", ""}
		}

		sym := style.symbol
		if tty && style.color != "" {
			sym = style.color + sym + ansiReset
		}

		agent := t.Agent
		if agent == "" {
			agent = "--"
		}
		title := t.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}

		line := fmt.Sprintf("  %s  %-14s  %-9s  %-8s  %s",
			sym, t.ID, ts.Status, agent, title)

		// Show reason for non-done, non-cancelled statuses.
		if ts.Status != task.StatusDone && ts.Status != task.StatusCancelled {
			reason := t.ErrorMsg
			if reason == "" && (ts.Status == task.StatusBlocked || ts.Status == task.StatusFailed) {
				reason = "(no reason recorded)"
			}
			if reason != "" {
				line += "  " + reason
			}
		}

		fmt.Println(line)
	}
	fmt.Println()
}

// PrintAllStatus is the exported equivalent of printAllStatus.
// It prints all goals and their tasks to stdout, suitable for `st status`.
func PrintAllStatus(ws *workspace.Workspace) { printAllStatus(ws) }

// PrintGoalStatus is the exported equivalent of printGoalStatus.
// It prints the tasks for a specific goal to stdout.
func PrintGoalStatus(ws *workspace.Workspace, goalID string) { printGoalStatus(ws, goalID) }

// IsSupervisorRunning checks if a supervisor process is already running
// by reading the PID file and verifying the process is alive.
func IsSupervisorRunning(ws *workspace.Workspace) bool {
	data, err := os.ReadFile(ws.SupervisorPIDPath())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	return worker.IsAlive(pid)
}

// startupRecovery resets running tasks that have no matching worker record.
func startupRecovery(ws *workspace.Workspace) error {
	running, err := task.ListByStatus(ws, task.StatusRunning)
	if err != nil {
		return err
	}
	workers, err := worker.List(ws)
	if err != nil {
		return err
	}

	// Build set of task IDs that have workers.
	workerTasks := make(map[string]bool)
	for _, w := range workers {
		workerTasks[w.TaskID] = true
	}

	for _, t := range running {
		if !workerTasks[t.ID] {
			slog.Warn("orphaned running task, resetting", slog.String("task_id", t.ID))
			if err := task.ResetToQueue(ws, t.ID); err != nil {
				slog.Error("reset orphaned task failed", slog.String("task_id", t.ID), slog.Any("error", err))
			}
		}
	}

	// Clean up dead worker records.
	for _, w := range workers {
		if !worker.IsAlive(w.PID) {
			slog.Warn("dead worker at startup, cleaning up", slog.String("worker_id", w.ID), slog.Int("pid", w.PID))
			if err := worker.Delete(ws, w.ID); err != nil {
				slog.Error("delete dead worker failed", slog.String("worker_id", w.ID), slog.Any("error", err))
			}
			if strings.HasPrefix(w.TaskID, "planner-") {
				// Planner died — only reset goal if it's still in planning status.
				// If it already transitioned to active/done/failed, don't clobber it.
				goalID := w.TaskID[len("planner-"):]
				g, getErr := goal.Get(ws, goalID)
				if getErr == nil && g.Status == goal.StatusPlanning {
					goalTasks, _ := task.ListForGoal(ws, goalID)
					if len(goalTasks) > 0 {
						slog.Info("dead planner created tasks, transitioning goal to active",
							slog.String("goal_id", goalID),
							slog.Int("task_count", len(goalTasks)))
						if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
							slog.Error("transition goal to active failed", slog.String("goal_id", goalID), slog.Any("error", err))
						}
						checkGoalCompletion(ws, goalID)
					} else {
						slog.Warn("dead planner at startup, resetting goal", slog.String("goal_id", goalID))
						if err := goal.UpdateStatus(ws, goalID, goal.StatusQueued); err != nil {
							slog.Error("reset goal to queued failed", slog.String("goal_id", goalID), slog.Any("error", err))
						}
					}
				}
			} else {
				if err := task.ResetToQueue(ws, w.TaskID); err != nil {
					slog.Error("reset task failed", slog.String("task_id", w.TaskID), slog.Any("error", err))
				}
			}
		}
	}

	// Reset planning goals that have no planner worker record (graceful-shutdown case:
	// monitorPlanner deleted the worker but left goal in planning).
	allGoals, gErr := goal.List(ws)
	if gErr == nil {
		for _, g := range allGoals {
			if g.Status == goal.StatusPlanning && !workerTasks["planner-"+g.ID] {
				goalTasks, _ := task.ListForGoal(ws, g.ID)
				if len(goalTasks) > 0 {
					slog.Info("orphaned planning goal has tasks, transitioning to active",
						slog.String("goal_id", g.ID),
						slog.Int("task_count", len(goalTasks)))
					if err := goal.UpdateStatus(ws, g.ID, goal.StatusActive); err != nil {
						slog.Error("transition goal to active failed", slog.String("goal_id", g.ID), slog.Any("error", err))
					}
					checkGoalCompletion(ws, g.ID)
				} else {
					slog.Warn("orphaned planning goal, resetting", slog.String("goal_id", g.ID))
					if err := goal.UpdateStatus(ws, g.ID, goal.StatusQueued); err != nil {
						slog.Error("reset orphaned goal failed", slog.String("goal_id", g.ID), slog.Any("error", err))
					}
				}
			}
			// Check if active goals have all tasks in terminal state (can happen if the
			// process was killed between task completion and checkGoalCompletion firing).
			if g.Status == goal.StatusActive {
				checkGoalCompletion(ws, g.ID)
			}
		}
	}
	return nil
}
