package recovery_test

import (
	"os"
	"testing"
	"time"

	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/recovery"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/worker"
	"github.com/user/stint/internal/workspace"
)

func newWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Init(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws
}

func TestRecoverResetsStaleRunningTasks(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create a task and claim it.
	tk := &task.Task{GoalID: "g-001", Title: "stale", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-dead")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// No worker record → stale.
	if err := recovery.Recover(ws, 0); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	_, status, _ := task.Get(ws, claimed.ID)
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending after recovery", status)
	}
}

func TestRecoverLeavesRecentTasksAlone(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{GoalID: "g-001", Title: "active", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "w-live")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Register a live worker using the current process PID (definitely alive).
	w := &worker.Worker{
		ID:          "w-live",
		PID:         os.Getpid(),
		TaskID:      claimed.ID,
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Use a large threshold so nothing is considered stale by age.
	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	_, status, _ := task.Get(ws, claimed.ID)
	if status != task.StatusRunning {
		t.Errorf("status = %s, want running (should not have been reset)", status)
	}
}

func TestRecoverDeletesDeadWorkerRecords(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Register a worker with a dead PID.
	w := &worker.Worker{
		ID:          "w-dead",
		PID:         99999999, // definitely not a real PID
		TaskID:      "t-fake",
		Agent:       "impl",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	workers, err := worker.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, wk := range workers {
		if wk.ID == "w-dead" {
			t.Error("dead worker record should have been deleted")
		}
	}
}

// TestRecoverDoesNotResetActiveGoalWithDeadPlannerWorker verifies that Recover
// does not clobber a goal already in active status when it finds a dead planner
// worker record for it (crash-between-Wait-and-Delete scenario).
func TestRecoverDoesNotResetActiveGoalWithDeadPlannerWorker(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "active goal recover test", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	// Goal already transitioned to active (planner finished successfully).
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusActive); err != nil {
		t.Fatalf("UpdateStatus active: %v", err)
	}
	// Dead planner worker record still on disk (supervisor crashed before Delete).
	w := &worker.Worker{
		ID:          "w-planner-dead-recover",
		PID:         99999999,
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

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusActive {
		t.Errorf("goal status = %s, want active (Recover must not reset active goal)", got.Status)
	}
}

// TestRecoverResetsPlannerForPlanningGoal verifies that Recover resets a goal
// to queued when its planner worker is dead and the goal is still in planning
// status — covering the StatusPlanning branch inside the dead-worker loop.
func TestRecoverResetsPlannerForPlanningGoal(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	g, err := goal.Create(ws, "still planning goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusPlanning); err != nil {
		t.Fatalf("UpdateStatus planning: %v", err)
	}

	// Register a dead planner worker for this goal.
	w := &worker.Worker{
		ID:          "w-planner-planning-dead",
		PID:         99999999,
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

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusQueued {
		t.Errorf("goal status = %s, want queued after dead planner during planning", got.Status)
	}
}

// TestRecoverPrunesWorktreesInExistingRepo verifies that Recover calls
// WorktreePrune for repos that exist on disk and does not propagate prune
// errors — covering the WorktreePrune call and its error-log branch.
func TestRecoverPrunesWorktreesInExistingRepo(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Use a real but non-git temp directory so that WorktreePrune fails with a
	// "not a git repository" error; recovery must log the warning and return nil.
	nonGitDir := t.TempDir()
	ws.Config.Repos = map[string]string{
		"non-git": nonGitDir,
	}
	// WorktreePrune errors are logged, not returned — Recover must succeed.
	if err := recovery.Recover(ws, 9999); err != nil {
		t.Fatalf("Recover: %v", err)
	}
}

// TestRecoverRunningDirCorrupt verifies that Recover accumulates and returns
// an error when task.ListByStatus fails because the running directory is
// corrupt — covering the ListByStatus error path and the final error return.
func TestRecoverRunningDirCorrupt(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Replace the running/ directory with a regular file so os.ReadDir fails
	// with ENOTDIR (not os.IsNotExist), causing ListByStatus to return an error.
	if err := os.Remove(ws.RunningDir()); err != nil {
		t.Fatalf("Remove running dir: %v", err)
	}
	if err := os.WriteFile(ws.RunningDir(), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := recovery.Recover(ws, 9999); err == nil {
		t.Fatal("expected Recover to return an error when running dir is corrupt")
	}
}

// TestRecoverWorkersDirCorrupt verifies that Recover returns an early error
// when the workers directory is corrupt and cannot be listed — covering the
// worker.List error early-return path at the top of Recover.
func TestRecoverWorkersDirCorrupt(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Replace the workers/ directory with a regular file so worker.List fails.
	if err := os.Remove(ws.WorkersDir()); err != nil {
		t.Fatalf("Remove workers dir: %v", err)
	}
	if err := os.WriteFile(ws.WorkersDir(), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := recovery.Recover(ws, 9999); err == nil {
		t.Fatal("expected Recover to return an error when workers dir is corrupt")
	}
}

// TestRecoverWorkerDeleteFails verifies that Recover accumulates errors when a
// dead worker record cannot be deleted (e.g. the workers directory is
// read-only) — covering the worker.Delete error append path.
func TestRecoverWorkerDeleteFails(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Register a dead-process worker so Recover will attempt to delete it.
	w := &worker.Worker{
		ID:          "w-undeletable",
		PID:         99999999,
		TaskID:      "t-fakeid",
		Agent:       "default",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := worker.Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Make the workers directory read-only so os.Remove inside Delete fails.
	if err := os.Chmod(ws.WorkersDir(), 0555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	// Restore write permission so t.TempDir() cleanup can remove the directory.
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	if err := recovery.Recover(ws, 9999); err == nil {
		t.Fatal("expected Recover to return an error when worker delete fails")
	}
}
