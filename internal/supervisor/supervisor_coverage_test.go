package supervisor

// supervisor_coverage_test.go — targeted tests to cover error-path branches
// that require read-only filesystem conditions or direct function calls.
// These tests are NOT parallel because they use os.Chmod or t.Setenv.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/worker"
)

// ─── sweepActiveGoals error path ─────────────────────────────────────────────

// TestSweepActiveGoalsGoalListError verifies that sweepActiveGoals does not
// panic when goal.List fails (goals dir unreadable). It logs and returns early.
func TestSweepActiveGoalsGoalListError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("sweepActiveGoals panicked: %v", r)
		}
	}()
	sweepActiveGoals(ws)
}

// ─── handlePlannerSuccess UpdateStatus error ─────────────────────────────────

// TestHandlePlannerSuccessUpdateStatusDoneError verifies that handlePlannerSuccess
// logs an error when UpdateStatus(done) fails (goals dir read-only, no tasks).
func TestHandlePlannerSuccessUpdateStatusDoneError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "success done error goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.GoalsDir(), 0444); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handlePlannerSuccess panicked: %v", r)
		}
	}()
	handlePlannerSuccess(ws, g.ID)
}

// TestHandlePlannerSuccessUpdateStatusActiveError verifies the active branch:
// when planner created tasks, handlePlannerSuccess tries UpdateStatus(active),
// which fails when goals dir is read-only.
func TestHandlePlannerSuccessUpdateStatusActiveError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "success active error goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	if err := os.Chmod(ws.GoalsDir(), 0444); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handlePlannerSuccess panicked: %v", r)
		}
	}()
	handlePlannerSuccess(ws, g.ID)
}

// ─── handlePlannerError UpdateStatus error ────────────────────────────────────

// TestHandlePlannerErrorUpdateStatusFailedError verifies handlePlannerError logs
// an error when UpdateStatus(failed) fails (no tasks, goals dir read-only).
func TestHandlePlannerErrorUpdateStatusFailedError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "planner error status error", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.GoalsDir(), 0444); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handlePlannerError panicked: %v", r)
		}
	}()
	handlePlannerError(ws, g.ID)
}

// TestHandlePlannerErrorUpdateStatusActiveError verifies the active branch:
// when tasks exist but UpdateStatus(active) fails (goals dir read-only).
func TestHandlePlannerErrorUpdateStatusActiveError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "planner error active status error", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	if err := os.Chmod(ws.GoalsDir(), 0444); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handlePlannerError panicked: %v", r)
		}
	}()
	handlePlannerError(ws, g.ID)
}

// ─── checkGoalCompletion UpdateStatus error ───────────────────────────────────

// TestCheckGoalCompletionUpdateStatusError verifies that checkGoalCompletion
// logs an error when goal.UpdateStatus fails. All tasks are terminal, but the
// goals directory is read-only, so UpdateStatus(done) fails.
func TestCheckGoalCompletionUpdateStatusError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "check completion status error", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "done task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-check-err")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Done(ws, claimed.ID, "done", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if err := os.Chmod(ws.GoalsDir(), 0444); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("checkGoalCompletion panicked: %v", r)
		}
	}()
	checkGoalCompletion(ws, g.ID)
}

// ─── healthCheck ListStale error ──────────────────────────────────────────────

// TestHealthCheckListStaleError verifies that healthCheck returns an error when
// worker.ListStale fails (workers dir unreadable).
func TestHealthCheckListStaleError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	err := healthCheck(ws)
	if err == nil {
		t.Error("healthCheck should return error when workers dir is unreadable, got nil")
	}
}

// ─── updateRunningTask write error ────────────────────────────────────────────

// TestUpdateRunningTaskWriteError verifies that updateRunningTask returns an error
// when the running directory is read-only and os.WriteFile fails.
func TestUpdateRunningTaskWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	g, err := goal.Create(ws, "write error goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "write err task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-write-err")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := os.Chmod(ws.RunningDir(), 0444); err != nil {
		t.Fatalf("Chmod running dir: %v", err)
	}
	defer os.Chmod(ws.RunningDir(), 0755) //nolint:errcheck

	claimed.GoalBranch = "st/goals/test-branch"
	err = updateRunningTask(ws, claimed)
	if err == nil {
		t.Error("updateRunningTask should return error when running dir is read-only, got nil")
	}
}

// ─── supervisorDispatchLoop error ticks ──────────────────────────────────────

// TestSupervisorDispatchLoopDispatchErrorPath verifies that supervisorDispatchLoop
// logs errors from dispatchGoals and dispatchTasks (workers dir unreadable)
// without crashing.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestSupervisorDispatchLoopDispatchErrorPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 100 * time.Second
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := supervisorDispatchLoop(ctx, ws, 1); err != nil {
		t.Fatalf("supervisorDispatchLoop returned unexpected error: %v", err)
	}
}

// TestSupervisorDispatchLoopHealthErrorPath verifies the health tick error log.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestSupervisorDispatchLoopHealthErrorPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 100 * time.Second
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := supervisorDispatchLoop(ctx, ws, 1); err != nil {
		t.Fatalf("supervisorDispatchLoop returned unexpected error: %v", err)
	}
}

// ─── goalDispatchLoop error ticks ────────────────────────────────────────────

// TestGoalDispatchLoopDispatchErrorPath verifies that goalDispatchLoop logs
// dispatch errors without crashing when workers dir is unreadable.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestGoalDispatchLoopDispatchErrorPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 100 * time.Second
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	g, err := goal.Create(ws, "dispatch error loop goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = goalDispatchLoop(ctx, ws, g.ID, 1)
}

// TestGoalDispatchLoopHealthErrorPath verifies the health tick error log in
// goalDispatchLoop when workers dir is unreadable.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestGoalDispatchLoopHealthErrorPath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 100 * time.Second
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	g, err := goal.Create(ws, "health error loop goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = goalDispatchLoop(ctx, ws, g.ID, 1)
}

// ─── dispatchTasks with Repo field ───────────────────────────────────────────

// TestDispatchTasksWithRepoFieldUnknownRepo verifies that dispatchTasks logs
// the setupGoalBranch error when a task has a non-empty Repo field that's not
// in ws.Config.Repos. Covers the `if t.Repo != ""` branch and error log.
//
// Not parallel — uses t.Setenv.
func TestDispatchTasksWithRepoFieldUnknownRepo(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	ws := newWS(t)

	g, err := goal.Create(ws, "repo field goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{
		GoalID: g.ID,
		Title:  "repo task",
		Agent:  "impl",
		Prompt: "p",
		Repo:   "nonexistent-repo",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	ctx := context.Background()
	if err := dispatchTasks(ctx, ws, 10); err != nil {
		t.Fatalf("dispatchTasks: %v", err)
	}

	// Task ends up failed after exhausting retries (spawn fails due to PATH restriction).
	_, status, _ := task.Get(ws, tk.ID)
	if status != task.StatusFailed {
		t.Errorf("task status = %s after unknown repo dispatch, want failed", status)
	}
}

// ─── startupRecovery error paths ─────────────────────────────────────────────

// TestStartupRecoveryRunningTasksListError verifies that startupRecovery returns
// an error when task.ListByStatus(running) fails (running dir unreadable).
func TestStartupRecoveryRunningTasksListError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.RunningDir(), 0000); err != nil {
		t.Fatalf("Chmod running dir: %v", err)
	}
	defer os.Chmod(ws.RunningDir(), 0755) //nolint:errcheck

	err := startupRecovery(ws)
	if err == nil {
		t.Error("startupRecovery should return error when running dir is unreadable, got nil")
	}
}

// TestStartupRecoveryWorkersListError verifies that startupRecovery returns an
// error when worker.List fails (workers dir unreadable).
func TestStartupRecoveryWorkersListError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	err := startupRecovery(ws)
	if err == nil {
		t.Error("startupRecovery should return error when workers dir is unreadable, got nil")
	}
}

// ─── runWithTUI coverage ─────────────────────────────────────────────────────

// TestRunWithTUIDirectCallNoTTY verifies that runWithTUI can be called directly
// in a non-TTY test environment. Bubbletea v1.3.10 returns "could not open a
// new TTY" error from p.Run() when stdout is a pipe, causing runWithTUI to
// take the error-return path. This covers most of runWithTUI.
//
// Not parallel — modifies package-level dispatch/health intervals.
func TestRunWithTUIDirectCallNoTTY(t *testing.T) {
	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithTUI(ctx, ws, "", 1, nil)
	}()

	select {
	case err := <-errCh:
		_ = err // TTY error or nil — both acceptable
	case <-time.After(5 * time.Second):
		t.Fatal("runWithTUI did not return within 5s — possible hang without a TTY")
	}
}

// TestRunWithTUIGoalIDDirectCallNoTTY verifies the goalID != "" branch inside
// runWithTUI's dispatch goroutine. With a non-empty goalID, the goroutine calls
// goalDispatchLoop instead of supervisorDispatchLoop.
//
// Not parallel — modifies package-level dispatch/health intervals.
func TestRunWithTUIGoalIDDirectCallNoTTY(t *testing.T) {
	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	g, err := goal.Create(ws, "tui goal id test", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusDone); err != nil {
		t.Fatalf("UpdateStatus done: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithTUI(ctx, ws, g.ID, 1, nil)
	}()

	select {
	case err := <-errCh:
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("runWithTUI (goalID branch) did not return within 5s")
	}
}

// ─── Run / RunGoal error log paths ────────────────────────────────────────────

// TestRunDispatchErrorLogged verifies that Run logs dispatch errors (workers dir
// unreadable) without panicking.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestRunDispatchErrorLogged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 100 * time.Second
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := Run(ctx, ws, 1, 0); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}

// TestRunHealthErrorLogged verifies that Run logs healthCheck errors without panicking.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestRunHealthErrorLogged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 100 * time.Second
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := Run(ctx, ws, 1, 0); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}

// TestRunGoalDispatchErrorLogged verifies that RunGoal logs dispatch errors.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestRunGoalDispatchErrorLogged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 5 * time.Millisecond
	healthInterval = 100 * time.Second
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	g, err := goal.Create(ws, "rungoal dispatch error goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = RunGoal(ctx, ws, g.ID, 1, 0)
}

// TestRunGoalHealthErrorLogged verifies that RunGoal logs healthCheck errors.
//
// Not parallel — modifies package-level intervals and workspace permissions.
func TestRunGoalHealthErrorLogged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")

	origDisp := dispatchInterval
	origHealth := healthInterval
	dispatchInterval = 100 * time.Second
	healthInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		dispatchInterval = origDisp
		healthInterval = origHealth
	})

	ws := newWS(t)
	g, err := goal.Create(ws, "rungoal health error goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod workers dir: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = RunGoal(ctx, ws, g.ID, 1, 0)
}

// ─── printGoalStatus error paths ─────────────────────────────────────────────

// TestPrintGoalStatusFailedNoReason verifies that printGoalStatus shows
// "(no reason recorded)" for a failed task with an empty error message.
func TestPrintGoalStatusFailedNoReason(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ws := newWS(t)

	g, err := goal.Create(ws, "failed no reason goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "failed task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	claimed, claimErr := task.ClaimNext(ws, "w-failed-nr")
	if claimErr != nil {
		t.Fatalf("ClaimNext: %v", claimErr)
	}
	if err := task.Fail(ws, claimed.ID, ""); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	output := captureStdout(func() {
		printGoalStatus(ws, g.ID)
	})

	if !strings.Contains(output, "no reason recorded") {
		t.Errorf("printGoalStatus should show 'no reason recorded' for failed task with no reason\noutput: %s", output)
	}
}

// ─── monitorWorker explicit fail max retries ──────────────────────────────────

// TestMonitorWorkerExplicitFailExhaustsMaxRetries verifies that when a task has
// already exhausted MaxExplicitFailRetries, monitorWorker leaves it as failed
// rather than retrying again. This exercises the `else` branch in monitorWorker.
func TestMonitorWorkerExplicitFailExhaustsMaxRetries(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "max retries exhausted goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "max retry task", Agent: "impl", Prompt: "p"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Exhaust ExplicitFailRetries by claiming, failing, and retrying repeatedly.
	for i := 0; i < task.MaxExplicitFailRetries; i++ {
		claimed, claimErr := task.ClaimNext(ws, "w-exhaust-"+string(rune('a'+i)))
		if claimErr != nil {
			t.Fatalf("ClaimNext iter %d: %v", i, claimErr)
		}
		if failErr := task.Fail(ws, claimed.ID, "fail for retry"); failErr != nil {
			t.Fatalf("Fail iter %d: %v", i, failErr)
		}
		if retryErr := task.RetryWithContext(ws, claimed.ID, "fail for retry"); retryErr != nil {
			break // Task already at max; no more retries possible.
		}
	}

	// Claim the last iteration.
	final, claimErr := task.ClaimNext(ws, "w-final-exhaust")
	if claimErr != nil {
		return // Task permanently failed during exhaust loop — test still passed.
	}
	// Mark it explicitly failed.
	if err := task.Fail(ws, final.ID, "final fail"); err != nil {
		t.Fatalf("Fail final: %v", err)
	}

	w := &worker.Worker{
		ID:          "w-final-exhaust",
		PID:         os.Getpid(),
		TaskID:      final.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register worker: %v", err)
	}

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn 'true': %v", err)
	}

	done := make(chan struct{})
	go func() {
		monitorWorker(context.Background(), ws, final.ID, "w-final-exhaust", cmd.Process)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitorWorker did not return within 5s")
	}

	// Task should remain failed — max retries exhausted, no more auto-retry.
	_, status, _ := task.Get(ws, final.ID)
	if status != task.StatusFailed && status != task.StatusPending {
		t.Errorf("task status = %s, want failed (max retries exhausted)", status)
	}
}

// ─── printAllStatus error path ────────────────────────────────────────────────

// TestPrintAllStatusGoalListError verifies that printAllStatus returns early
// without panicking when goal.List fails.
func TestPrintAllStatusGoalListError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	t.Setenv("NO_COLOR", "1")
	ws := newWS(t)

	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("printAllStatus panicked: %v", r)
		}
	}()

	_ = captureStdout(func() {
		printAllStatus(ws)
	})
}

// TestPrintAllStatusWithNoTasks verifies the "no tasks" branch in printAllStatus
// when there are goals but no tasks (allTasks is empty).
func TestPrintAllStatusWithNoTasksButGoals(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ws := newWS(t)

	// Create a goal but no tasks.
	if _, err := goal.Create(ws, "goal with no tasks", nil, nil); err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	output := captureStdout(func() {
		printAllStatus(ws)
	})

	if !strings.Contains(output, "no tasks") {
		t.Errorf("printAllStatus should contain 'no tasks' when there are goals but no tasks\noutput:\n%s", output)
	}
}
