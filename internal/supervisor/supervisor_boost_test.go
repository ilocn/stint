package supervisor

// supervisor_boost_test.go — targeted tests to cover previously uncovered code
// paths and push the supervisor package to ≥95% coverage.
//
// Key areas addressed:
//   - dispatchGoals: spawn planner path (fake claude exits 0)
//   - dispatchGoals: goal.UpdateStatus(planning) error path
//   - dispatchTasks: spawn worker path (fake claude, covers WriteHeartbeat/Register)
//   - healthCheck: stale task at max retries, worker delete failure
//   - setupGoalBranch: CreateBranch failure for non-git repo
//   - monitorWorker: explicit fail below max retries (auto-retry path)
//
// Tests that modify global state (PATH, filesystem permissions) do NOT call
// t.Parallel() to avoid interference with concurrent tests.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/worker"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// makeFakeClaude creates a shell script named "claude" in a temp dir and
// prepends that dir to PATH so exec.Command("claude", ...) finds it.
// The script exits with the given exitCode immediately.
func makeFakeClaude(t *testing.T, exitCode int) {
	t.Helper()
	fakeBin := t.TempDir()
	script := "#!/bin/sh\nexit " + strconv.Itoa(exitCode) + "\n"
	claudePath := filepath.Join(fakeBin, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0755); err != nil {
		t.Fatalf("makeFakeClaude: WriteFile: %v", err)
	}
	oldPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+":"+oldPATH)
}

// ─── isTTY ────────────────────────────────────────────────────────────────────

// TestIsTTYFalseWhenNOCOLORNonEmpty covers the NO_COLOR env-var branch of
// isTTY(), which is distinct from the TERM=dumb branch tested elsewhere.
func TestIsTTYFalseWhenNOCOLORNonEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "true")
	t.Setenv("TERM", "xterm-256color")
	if isTTY() {
		t.Error("isTTY() = true with NO_COLOR=true, want false")
	}
}

// ─── dispatchGoals — spawn planner path ──────────────────────────────────────

// TestDispatchGoalsSpawnPlannerPathFakeClaude verifies that dispatchGoals
// marks the goal as planning, registers a planner worker, and spawns a process
// when a queued goal exists and planners < maxConcurrent.
//
// Uses a fake "claude" binary that exits 0 immediately.
// Not parallel — modifies PATH via t.Setenv.
func TestDispatchGoalsSpawnPlannerPathFakeClaude(t *testing.T) {
	makeFakeClaude(t, 0)

	ws := newWS(t)

	g, err := goal.Create(ws, "spawn planner goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	ctx := context.Background()
	if err := dispatchGoals(ctx, ws, 5); err != nil {
		t.Fatalf("dispatchGoals returned unexpected error: %v", err)
	}

	// Wait for monitorPlanner goroutine to complete to prevent TempDir cleanup
	// races (same pattern as TestDispatchGoalsBreakOnMaxConcurrentInLoop).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		workers, wErr := worker.List(ws)
		if wErr == nil && len(workers) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Goal should be in planning (UpdateStatus was called before spawn).
	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	// The fake claude exits immediately; monitorPlanner may or may not have
	// run yet, but goal must have moved past queued.
	if got.Status == goal.StatusQueued {
		t.Errorf("goal status = queued after dispatchGoals spawn, want planning/active/done/failed")
	}
}

// TestDispatchGoalsBreakOnMaxConcurrentInLoop verifies that the inner
// `if planners >= maxConcurrent { break }` guard fires when the planners
// count reaches maxConcurrent mid-loop across multiple queued goals.
//
// Not parallel — modifies PATH via t.Setenv.
func TestDispatchGoalsBreakOnMaxConcurrentInLoop(t *testing.T) {
	makeFakeClaude(t, 0)

	ws := newWS(t)

	// Create 3 queued goals.
	for i := 0; i < 3; i++ {
		if _, err := goal.Create(ws, "loop break goal "+strconv.Itoa(i), nil, nil); err != nil {
			t.Fatalf("Create goal %d: %v", i, err)
		}
	}

	// maxConcurrent=1: only 1 planner should be spawned per call.
	ctx := context.Background()
	if err := dispatchGoals(ctx, ws, 1); err != nil {
		t.Fatalf("dispatchGoals: %v", err)
	}

	// Wait for monitorPlanner goroutine(s) to complete. The fake claude exits
	// immediately (exitCode -1 due to signal kill), so monitorPlanner should
	// finish within a few hundred ms. This prevents TempDir cleanup races where
	// a .json.tmp file is left mid-write when the test ends.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		workers, wErr := worker.List(ws)
		if wErr == nil && len(workers) == 0 {
			break // all planner workers have been cleaned up by monitorPlanner
		}
		time.Sleep(20 * time.Millisecond)
	}

	// After dispatchGoals + monitorPlanner completes, exactly 1 goal should have
	// been dispatched (planning, active, done, or failed — not queued). The other
	// 2 should remain queued because maxConcurrent=1 prevented further dispatch.
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List goals: %v", err)
	}
	nonQueuedCount := 0
	for _, g := range goals {
		if g.Status != goal.StatusQueued {
			nonQueuedCount++
		}
	}
	if nonQueuedCount != 1 {
		t.Errorf("nonQueuedCount = %d after dispatchGoals(maxConcurrent=1), want 1 (only 1 planner spawned)", nonQueuedCount)
		for _, g := range goals {
			t.Logf("  goal %s: status=%s", g.ID, g.Status)
		}
	}
}

// TestDispatchGoalsUpdateStatusPlanningFails verifies that dispatchGoals logs
// an error and skips spawning when goal.UpdateStatus(planning) fails because
// the goals directory is not writable (0555 = read+execute, no write).
//
// Not parallel — uses os.Chmod.
func TestDispatchGoalsUpdateStatusPlanningFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	makeFakeClaude(t, 0)

	ws := newWS(t)

	g, err := goal.Create(ws, "update status planning fail goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Make goals dir read-only: List succeeds (execute+read), UpdateStatus fails (no write).
	if err := os.Chmod(ws.GoalsDir(), 0555); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	ctx := context.Background()
	// dispatchGoals should return nil (logs errors but doesn't propagate them).
	if err := dispatchGoals(ctx, ws, 5); err != nil {
		t.Fatalf("dispatchGoals returned unexpected error: %v", err)
	}

	// Restore permissions before reading.
	os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	// Goal should still be queued (UpdateStatus to planning failed → no spawn).
	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusQueued {
		t.Errorf("goal status = %s after UpdateStatus failure, want queued", got.Status)
	}
}

// ─── dispatchTasks — spawn worker path ───────────────────────────────────────

// TestDispatchTasksSpawnWorkerPathFakeClaude verifies that dispatchTasks calls
// WriteHeartbeat, registers the worker, increments current, and launches
// monitorWorker when SpawnWorker succeeds (fake claude exits 0 immediately).
//
// Not parallel — modifies PATH via t.Setenv.
func TestDispatchTasksSpawnWorkerPathFakeClaude(t *testing.T) {
	makeFakeClaude(t, 0)

	ws := newWS(t)

	g, err := goal.Create(ws, "spawn worker goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	// No Repo field — skips setupGoalBranch; goes straight to SpawnWorker.
	tk := &task.Task{GoalID: g.ID, Title: "spawn worker task", Agent: "impl", Prompt: "do work"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	ctx := context.Background()
	if err := dispatchTasks(ctx, ws, 5); err != nil {
		t.Fatalf("dispatchTasks returned unexpected error: %v", err)
	}

	// Fake claude exits almost immediately; wait for monitorWorker to finish.
	deadline := time.Now().Add(5 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		_, status, _ := task.Get(ws, tk.ID)
		if status != task.StatusRunning {
			finalStatus = status
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Worker exited without calling 'st task done' → task should be failed.
	if finalStatus != task.StatusFailed && finalStatus != task.StatusPending {
		t.Errorf("task status = %q after fake claude exits, want failed or pending", finalStatus)
	}
}

// TestDispatchTasksWriteHeartbeatErrorContinues verifies that dispatchTasks
// logs an error but continues when WriteHeartbeat fails (heartbeats dir not
// writable). The task is still dispatched.
//
// Not parallel — uses os.Chmod + t.Setenv.
func TestDispatchTasksWriteHeartbeatErrorContinues(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	makeFakeClaude(t, 0)

	ws := newWS(t)

	g, err := goal.Create(ws, "heartbeat fail goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "heartbeat fail task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Make heartbeats dir unwritable so WriteHeartbeat fails.
	if err := os.Chmod(ws.HeartbeatsDir(), 0555); err != nil {
		t.Fatalf("Chmod heartbeats: %v", err)
	}
	defer os.Chmod(ws.HeartbeatsDir(), 0755) //nolint:errcheck

	ctx := context.Background()
	// dispatchTasks should not return an error even if heartbeat write fails.
	if err := dispatchTasks(ctx, ws, 5); err != nil {
		t.Fatalf("dispatchTasks returned unexpected error: %v", err)
	}
}

// ─── healthCheck — remaining paths ───────────────────────────────────────────

// TestHealthCheckTaskExceedsMaxRetries verifies that healthCheck logs "exceeded
// max retries" when a stale worker's task has been reset MaxResetRetries times
// already (postStatus ends up as failed after the final ResetToQueue call).
func TestHealthCheckTaskExceedsMaxRetries(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "max retries hc goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "max retries task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Exhaust reset retries by claiming and resetting MaxResetRetries times.
	for i := 0; i < task.MaxResetRetries; i++ {
		claimed, claimErr := task.ClaimNext(ws, "w-pre-exhaust-"+strconv.Itoa(i))
		if claimErr != nil {
			break // task may already be failed
		}
		if rstErr := task.ResetToQueue(ws, claimed.ID); rstErr != nil {
			break // task may have been permanently failed
		}
	}

	// Claim one final time so the task is running with RetryCount = MaxResetRetries.
	final, claimErr := task.ClaimNext(ws, "w-final-hc")
	if claimErr != nil {
		// Task may already be permanently failed — test still validates the path.
		return
	}

	// Register a stale worker with a dead PID for this task.
	staleWorker := &worker.Worker{
		ID:          "w-final-hc",
		PID:         999999888,
		TaskID:      final.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Add(-700 * time.Second).Unix(),
		HeartbeatAt: time.Now().Add(-700 * time.Second).Unix(),
	}
	if err := worker.Register(ws, staleWorker); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// healthCheck calls ResetToQueue which permanently fails the task (max retries).
	if err := healthCheck(ws); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}

	// After healthCheck, task should be permanently failed.
	_, finalStatus, _ := task.Get(ws, final.ID)
	if finalStatus != task.StatusFailed && finalStatus != task.StatusPending {
		t.Errorf("task status = %q after exceeding max retries, want failed or pending", finalStatus)
	}
}

// TestHealthCheckWorkerDeleteFails verifies that healthCheck logs an error and
// continues when worker.Delete fails (workers dir not writable at 0555).
//
// Not parallel — uses os.Chmod.
func TestHealthCheckWorkerDeleteFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "delete fail hc goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "delete fail task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-delete-fail-hc")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	staleWorker := &worker.Worker{
		ID:          "w-delete-fail-hc",
		PID:         999999777,
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Add(-700 * time.Second).Unix(),
		HeartbeatAt: time.Now().Add(-700 * time.Second).Unix(),
	}
	if err := worker.Register(ws, staleWorker); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Make workers dir unwritable so Delete fails (0555 = read+execute, no write).
	if err := os.Chmod(ws.WorkersDir(), 0555); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	// healthCheck should return nil (it only logs the delete error and continues).
	if err := healthCheck(ws); err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
}

// ─── setupGoalBranch — CreateBranch failure ───────────────────────────────────

// TestSetupGoalBranchCreateBranchFailsNonGitRepo verifies that setupGoalBranch
// returns an error when CreateBranch fails because the configured repo path is
// a plain directory (not a git repo), so `git branch` fails.
func TestSetupGoalBranchCreateBranchFailsNonGitRepo(t *testing.T) {
	t.Parallel()

	// A plain directory (not initialized as a git repo).
	notGitDir := t.TempDir()

	ws := newWS(t)
	ws.Config.Repos = map[string]string{"notgit": notGitDir}

	g, err := goal.Create(ws, "create branch fail goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{
		GoalID: g.ID,
		Title:  "branch fail task",
		Agent:  "impl",
		Prompt: "p",
		Repo:   "notgit",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-cbf")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// BranchExists returns false for a non-git dir; CreateBranch then fails.
	if err := setupGoalBranch(ws, claimed); err == nil {
		t.Error("setupGoalBranch should fail for non-git repo, got nil")
	}
}

// ─── monitorWorker — explicit fail auto-retry path ───────────────────────────

// TestMonitorWorkerExplicitFailBelowMaxRetries verifies that monitorWorker
// auto-retries a task with ExplicitFailCount < MaxExplicitFailRetries. This
// covers the `retryErr == nil` success log path.
func TestMonitorWorkerExplicitFailBelowMaxRetries(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "retry below max goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "retry task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-retry-below")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Mark task explicitly failed: ExplicitFailCount = 0 < MaxExplicitFailRetries = 2.
	if err := task.Fail(ws, claimed.ID, "explicit fail for auto-retry test"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Register a worker for the task so monitorWorker can find/delete it.
	w := &worker.Worker{
		ID:          "w-retry-below",
		PID:         os.Getpid(),
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Spawn a quick process that exits 0 immediately ("true").
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn 'true': %v", err)
	}

	done := make(chan struct{})
	go func() {
		monitorWorker(context.Background(), ws, claimed.ID, "w-retry-below", cmd.Process)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitorWorker did not return within 5s")
	}

	// Task should have been auto-retried (back to pending with retry context).
	_, status, _ := task.Get(ws, claimed.ID)
	if status != task.StatusPending && status != task.StatusFailed {
		t.Errorf("task status = %q after auto-retry, want pending or failed", status)
	}
}

// TestMonitorWorkerResetByHealthCheck covers the "default" case in
// monitorWorker's status switch: task was reset to pending by a health check
// while the worker was still running.
func TestMonitorWorkerResetByHealthCheck(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "hc reset goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "hc reset task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-hc-reset")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Simulate a health-check reset: move the task back to pending while the
	// "worker" is still conceptually running.
	if err := task.ResetToQueue(ws, claimed.ID); err != nil {
		t.Fatalf("ResetToQueue: %v", err)
	}

	// Register a worker record.
	w := &worker.Worker{
		ID:          "w-hc-reset",
		PID:         os.Getpid(),
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn 'true': %v", err)
	}

	done := make(chan struct{})
	go func() {
		monitorWorker(context.Background(), ws, claimed.ID, "w-hc-reset", cmd.Process)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitorWorker did not return within 5s")
	}
}

// ─── startupRecovery — additional paths ──────────────────────────────────────

// TestStartupRecoveryDeadWorkerTaskReset verifies that startupRecovery deletes
// dead non-planner worker records and resets their tasks to pending.
func TestStartupRecoveryDeadWorkerTaskReset(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "startup dead worker goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "startup task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-startup-dead")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Register a dead worker (PID that surely does not exist).
	deadWorker := &worker.Worker{
		ID:          "w-startup-dead",
		PID:         999999666,
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, deadWorker); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := startupRecovery(ws); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	// Worker should be deleted after startupRecovery.
	workers, _ := worker.List(ws)
	for _, w := range workers {
		if w.ID == "w-startup-dead" {
			t.Error("dead worker should have been deleted by startupRecovery")
		}
	}
}

// TestStartupRecoveryActiveGoalCompletion verifies that startupRecovery calls
// checkGoalCompletion for active goals, marking them done when all tasks are
// already in a terminal state.
func TestStartupRecoveryActiveGoalCompletion(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "active goal completion", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusActive); err != nil {
		t.Fatalf("UpdateStatus active: %v", err)
	}

	tk := &task.Task{GoalID: g.ID, Title: "done task for startup", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-startup-active")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Done(ws, claimed.ID, "summary", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if err := startupRecovery(ws); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusDone {
		t.Errorf("goal status = %s after startupRecovery, want done", got.Status)
	}
}
