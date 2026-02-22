package recovery_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/recovery"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/worker"
	"github.com/user/stint/internal/workspace"
)

// TestRecoverWorkerListError verifies that Recover returns an error immediately
// when the workers directory is unreadable.
func TestRecoverWorkerListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Make workers dir unreadable so worker.List fails.
	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	err := recovery.Recover(ws, 9999)
	if err == nil {
		t.Error("Recover should return error when workers dir is unreadable")
	}
}

// TestRecoverRunningTasksListError verifies that Recover accumulates an error
// when the running tasks directory is unreadable, and returns it.
func TestRecoverRunningTasksListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Make the running dir unreadable so task.ListByStatus fails.
	if err := os.Chmod(ws.RunningDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.RunningDir(), 0755) //nolint:errcheck

	// Recover should accumulate the listing error and return it.
	err := recovery.Recover(ws, 9999)
	if err == nil {
		t.Error("Recover should return error when running dir is unreadable")
	}
}

// TestRecoverStaleByAge verifies that a running task belonging to a live worker
// is still reset when its ClaimedAt timestamp is older than the threshold.
func TestRecoverStaleByAge(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create and claim a task.
	tk := &task.Task{GoalID: "g-001", Title: "age-stale", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-age-stale")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Register a live worker (current process PID is alive).
	w := &worker.Worker{
		ID:          "w-age-stale",
		PID:         os.Getpid(),
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Backdate the task's claimed_at so it appears old.
	rawData, err := os.ReadFile(ws.RunningPath(claimed.ID))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var taskData map[string]interface{}
	if err := json.Unmarshal(rawData, &taskData); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	taskData["claimed_at"] = float64(time.Now().Unix() - 1000)
	newData, _ := json.MarshalIndent(taskData, "", "  ")
	if err := os.WriteFile(ws.RunningPath(claimed.ID), newData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Recover with threshold = 500 s; task is 1000 s old → stale by age.
	if err := recovery.Recover(ws, 500); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	_, status, _ := task.Get(ws, claimed.ID)
	if status != task.StatusPending {
		t.Errorf("status = %s after age-based recovery, want pending", status)
	}
}

// TestRecoverSkipsNonExistentRepo verifies that Recover skips repos whose
// directory does not exist on disk.
func TestRecoverSkipsNonExistentRepo(t *testing.T) {
	t.Parallel()

	ws, err := workspace.Init(t.TempDir(), map[string]string{
		"missing-repo": "/nonexistent/path/that/does/not/exist",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover with non-existent repo should succeed: %v", err)
	}
}

// TestRecoverRepoPruning verifies that Recover calls worktree prune for repos
// whose directory exists on disk.
func TestRecoverRepoPruning(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir := t.TempDir()
	if err := exec.Command("git", "-C", repoDir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Minimal git config so any commit commands don't fail.
	exec.Command("git", "-C", repoDir, "config", "user.email", "test@test.com").Run() //nolint:errcheck
	exec.Command("git", "-C", repoDir, "config", "user.name", "Test").Run()           //nolint:errcheck

	ws, err := workspace.Init(t.TempDir(), map[string]string{"test-repo": repoDir})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Recover should call gitutil.WorktreePrune for repoDir (no error expected).
	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover: %v", err)
	}
}

// TestRecoverDeadPlannerGoalGetError verifies that Recover does not crash when
// goal.Get returns an error for a dead planner worker's goal (e.g., goal was
// never created). The function logs the warning and continues.
func TestRecoverDeadPlannerGoalGetError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Register a planner worker with a dead PID for a non-existent goal.
	w := &worker.Worker{
		ID:          "w-planner-nogoal",
		PID:         999999999,
		TaskID:      "planner-g-nonexistent",
		Agent:       "planner",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Recover should not return an error when goal.Get fails for the planner goal.
	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover should succeed even when planner goal doesn't exist: %v", err)
	}
}

// TestRecoverRemovesStaleWorktree verifies that Recover removes the worktree
// directory for a reset stale task.
func TestRecoverRemovesStaleWorktree(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{GoalID: "g-001", Title: "worktree-stale", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-wt-stale")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Create a fake worktree directory for the task.
	worktreePath := ws.WorktreePath(claimed.ID)
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("MkdirAll worktree: %v", err)
	}

	// No live worker → task is stale.
	if err := recovery.Recover(ws, 0); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Worktree should have been removed.
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree directory should have been removed after recovery")
	}
}

// TestRecoverWorkerDeleteError verifies that Recover accumulates errors when
// worker.Delete fails (workers dir is read+execute but not writable).
func TestRecoverWorkerDeleteError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Register a dead worker.
	w := &worker.Worker{
		ID:          "w-dead-del-fail",
		PID:         999999999, // dead PID
		TaskID:      "t-fake-del",
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 0555: readable+executable (list and read files) but not writable (cannot delete).
	if err := os.Chmod(ws.WorkersDir(), 0555); err != nil {
		t.Fatalf("Chmod workers dir 0555: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	// Recover should: find the dead worker, fail to delete it, accumulate error, return it.
	err := recovery.Recover(ws, 9999)
	if err == nil {
		t.Error("Recover should return accumulated error when worker.Delete fails")
	}
}

// TestRecoverResetToQueueError verifies that Recover accumulates errors when
// task.ResetToQueue fails (pending dir is unreadable so it cannot write there).
func TestRecoverResetToQueueError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Create and claim a task (now in running/).
	tk := &task.Task{GoalID: "g-001", Title: "reset-fail", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-rf")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	_ = claimed

	// Make pending dir unreadable so ResetToQueue cannot write the task back there.
	if err := os.Chmod(ws.PendingDir(), 0000); err != nil {
		t.Fatalf("Chmod pending: %v", err)
	}
	defer os.Chmod(ws.PendingDir(), 0755) //nolint:errcheck

	// No worker record → task is stale → ResetToQueue called → fails → error accumulated.
	err = recovery.Recover(ws, 0)
	if err == nil {
		t.Error("Recover should return accumulated error when ResetToQueue fails")
	}
}

// TestRecoverPlannerDeadGoalIsPlanning verifies that when a dead planner worker's
// goal is still in planning status, Recover resets the goal back to queued.
func TestRecoverPlannerDeadGoalIsPlanning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create a goal and advance it to planning status.
	g, err := goal.Create(ws, "planning recovery goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusPlanning); err != nil {
		t.Fatalf("UpdateStatus planning: %v", err)
	}

	// Register a dead planner worker for this planning goal.
	w := &worker.Worker{
		ID:          "w-planner-planning",
		PID:         999999999, // dead PID
		TaskID:      "planner-" + g.ID,
		Agent:       "planner",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Goal should have been reset to queued.
	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusQueued {
		t.Errorf("goal status = %s after planner death, want queued", got.Status)
	}
}

// TestRecoverWorktreePruneError verifies that Recover logs but does not fail
// when gitutil.WorktreePrune returns an error (non-git directory as repo path).
func TestRecoverWorktreePruneError(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// A plain directory (not a git repo) → WorktreePrune will fail.
	nonGitDir := t.TempDir()

	ws, err := workspace.Init(t.TempDir(), map[string]string{"not-a-git-repo": nonGitDir})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Recover logs the WorktreePrune error but should still succeed.
	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover should succeed even when WorktreePrune fails: %v", err)
	}
}
