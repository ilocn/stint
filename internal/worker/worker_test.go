package worker

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ilocn/stint/internal/workspace"
)

func newWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Init(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws
}

// TestListStaleSkipsPlannerWithDeadPID verifies that a planner worker with a
// dead PID is NOT returned by ListStale. Planners are managed exclusively by
// monitorPlanner; ListStale must never race with it.
func TestListStaleSkipsPlannerWithDeadPID(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	w := &Worker{
		ID:          "w-planner1",
		PID:         999999999, // dead PID
		TaskID:      "planner-goal-abc",
		Agent:       "planner",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	for _, s := range stale {
		if s.ID == w.ID {
			t.Errorf("ListStale returned planner worker with dead PID — should be excluded")
		}
	}
}

// TestListStaleIncludesDeadTaskWorker verifies that a regular task worker with
// a dead PID IS returned by ListStale (existing behavior must be preserved).
func TestListStaleIncludesDeadTaskWorker(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	w := &Worker{
		ID:          "w-task1",
		PID:         999999999, // dead PID
		TaskID:      "t-some-task",
		Agent:       "default",
		StartedAt:   time.Now().Unix(),
		HeartbeatAt: time.Now().Unix(),
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	found := false
	for _, s := range stale {
		if s.ID == w.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListStale did not return dead task worker — it should be stale")
	}
}

// TestListStaleSkipsAlivePlannerAndAliveTaskWorkerWithFreshHeartbeat verifies
// that an alive planner and an alive task worker with a fresh heartbeat are
// both excluded from ListStale results.
func TestListStaleSkipsAlivePlannerAndAliveTaskWorkerWithFreshHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Use the current process PID — guaranteed alive for the duration of this test.
	selfPID := os.Getpid()
	now := time.Now().Unix()

	planner := &Worker{
		ID:          "w-planner2",
		PID:         selfPID,
		TaskID:      "planner-goal-xyz",
		Agent:       "planner",
		StartedAt:   now,
		HeartbeatAt: now,
	}
	taskWorker := &Worker{
		ID:          "w-task2",
		PID:         selfPID,
		TaskID:      "t-live-task",
		Agent:       "default",
		StartedAt:   now,
		HeartbeatAt: now,
	}
	if err := Register(ws, planner); err != nil {
		t.Fatalf("Register planner: %v", err)
	}
	if err := Register(ws, taskWorker); err != nil {
		t.Fatalf("Register task worker: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	for _, s := range stale {
		if s.ID == planner.ID {
			t.Errorf("ListStale returned alive planner — should be excluded")
		}
		if s.ID == taskWorker.ID {
			t.Errorf("ListStale returned alive task worker with fresh heartbeat — should not be stale")
		}
	}
}

// TestNewID verifies that NewID returns a properly-formed worker ID with the "w-" prefix
// and that consecutive calls return distinct values.
func TestNewID(t *testing.T) {
	t.Parallel()
	id := NewID()
	if len(id) == 0 {
		t.Fatal("NewID returned empty string")
	}
	if id[:2] != "w-" {
		t.Errorf("NewID = %q, want prefix %q", id, "w-")
	}
	// Two consecutive IDs should be distinct.
	id2 := NewID()
	if id == id2 {
		t.Errorf("NewID returned duplicate IDs: %q", id)
	}
}

// TestDelete verifies that after Register + Delete, Get returns an error.
func TestDelete(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	w := &Worker{
		ID:        "w-del1",
		PID:       os.Getpid(),
		TaskID:    "t-delete-test",
		Agent:     "default",
		StartedAt: time.Now().Unix(),
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := Delete(ws, w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := Get(ws, w.ID)
	if err == nil {
		t.Fatal("Get after Delete should return an error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Get after Delete: expected os.ErrNotExist in error chain, got: %v", err)
	}
}

// TestDeleteNonExistent verifies that Delete on a non-existent ID returns nil (idempotent).
func TestDeleteNonExistent(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	if err := Delete(ws, "w-nonexistent"); err != nil {
		t.Errorf("Delete on non-existent ID should return nil, got: %v", err)
	}
}

// TestUpdateHeartbeat verifies that UpdateHeartbeat sets HeartbeatAt to a recent value.
func TestUpdateHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	before := time.Now().Unix()
	w := &Worker{
		ID:          "w-hb1",
		PID:         os.Getpid(),
		TaskID:      "t-hb-test",
		Agent:       "default",
		StartedAt:   before,
		HeartbeatAt: 0, // explicitly zero
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := UpdateHeartbeat(ws, w.ID); err != nil {
		t.Fatalf("UpdateHeartbeat: %v", err)
	}
	updated, err := Get(ws, w.ID)
	if err != nil {
		t.Fatalf("Get after UpdateHeartbeat: %v", err)
	}
	if updated.HeartbeatAt < before {
		t.Errorf("HeartbeatAt = %d after UpdateHeartbeat, want >= %d", updated.HeartbeatAt, before)
	}
}

// TestUpdateHeartbeatNonExistent verifies that UpdateHeartbeat returns an error for
// a worker that does not exist.
func TestUpdateHeartbeatNonExistent(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	if err := UpdateHeartbeat(ws, "w-nonexistent"); err == nil {
		t.Error("UpdateHeartbeat on non-existent ID should return an error, got nil")
	}
}

// TestCount verifies that Count tracks the number of registered workers,
// decreasing correctly after a Delete.
func TestCount(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	w1 := &Worker{ID: "w-cnt1", PID: os.Getpid(), TaskID: "t-cnt1", Agent: "default", StartedAt: time.Now().Unix()}
	w2 := &Worker{ID: "w-cnt2", PID: os.Getpid(), TaskID: "t-cnt2", Agent: "default", StartedAt: time.Now().Unix()}

	if err := Register(ws, w1); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	if err := Register(ws, w2); err != nil {
		t.Fatalf("Register w2: %v", err)
	}

	n, err := Count(ws)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d after 2 registrations, want 2", n)
	}

	if err := Delete(ws, w1.ID); err != nil {
		t.Fatalf("Delete w1: %v", err)
	}

	n, err = Count(ws)
	if err != nil {
		t.Fatalf("Count after delete: %v", err)
	}
	if n != 1 {
		t.Errorf("Count = %d after deleting 1, want 1", n)
	}
}

// TestCountEmptyWorkspace verifies that Count returns 0 for a fresh workspace.
func TestCountEmptyWorkspace(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	n, err := Count(ws)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("Count = %d on empty workspace, want 0", n)
	}
}

// TestCountTaskWorkers verifies that CountTaskWorkers excludes planner workers
// (those whose TaskID has the "planner-" prefix).
func TestCountTaskWorkers(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	planner := &Worker{ID: "w-pl1", PID: os.Getpid(), TaskID: "planner-goal-abc", Agent: "planner", StartedAt: time.Now().Unix()}
	tw1 := &Worker{ID: "w-tw1", PID: os.Getpid(), TaskID: "t-task1", Agent: "default", StartedAt: time.Now().Unix()}
	tw2 := &Worker{ID: "w-tw2", PID: os.Getpid(), TaskID: "t-task2", Agent: "default", StartedAt: time.Now().Unix()}

	for _, w := range []*Worker{planner, tw1, tw2} {
		if err := Register(ws, w); err != nil {
			t.Fatalf("Register %s: %v", w.ID, err)
		}
	}

	n, err := CountTaskWorkers(ws)
	if err != nil {
		t.Fatalf("CountTaskWorkers: %v", err)
	}
	if n != 2 {
		t.Errorf("CountTaskWorkers = %d, want 2 (planners must be excluded)", n)
	}
}

// TestCountTaskWorkersOnlyPlanners verifies that CountTaskWorkers returns 0
// when all workers are planners.
func TestCountTaskWorkersOnlyPlanners(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	planner := &Worker{ID: "w-pl3", PID: os.Getpid(), TaskID: "planner-only", Agent: "planner", StartedAt: time.Now().Unix()}
	if err := Register(ws, planner); err != nil {
		t.Fatalf("Register: %v", err)
	}

	n, err := CountTaskWorkers(ws)
	if err != nil {
		t.Fatalf("CountTaskWorkers: %v", err)
	}
	if n != 0 {
		t.Errorf("CountTaskWorkers = %d with only planners registered, want 0", n)
	}
}

// TestCountPlanners verifies that CountPlanners only counts workers whose
// TaskID has the "planner-" prefix.
func TestCountPlanners(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	planner := &Worker{ID: "w-pl2", PID: os.Getpid(), TaskID: "planner-goal-xyz", Agent: "planner", StartedAt: time.Now().Unix()}
	tw1 := &Worker{ID: "w-tw3", PID: os.Getpid(), TaskID: "t-task3", Agent: "default", StartedAt: time.Now().Unix()}
	tw2 := &Worker{ID: "w-tw4", PID: os.Getpid(), TaskID: "t-task4", Agent: "default", StartedAt: time.Now().Unix()}

	for _, w := range []*Worker{planner, tw1, tw2} {
		if err := Register(ws, w); err != nil {
			t.Fatalf("Register %s: %v", w.ID, err)
		}
	}

	n, err := CountPlanners(ws)
	if err != nil {
		t.Fatalf("CountPlanners: %v", err)
	}
	if n != 1 {
		t.Errorf("CountPlanners = %d, want 1", n)
	}
}

// TestCountPlannersNoPlanner verifies that CountPlanners returns 0 when there
// are no planner workers registered.
func TestCountPlannersNoPlanner(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tw := &Worker{ID: "w-tw5", PID: os.Getpid(), TaskID: "t-task5", Agent: "default", StartedAt: time.Now().Unix()}
	if err := Register(ws, tw); err != nil {
		t.Fatalf("Register: %v", err)
	}

	n, err := CountPlanners(ws)
	if err != nil {
		t.Fatalf("CountPlanners: %v", err)
	}
	if n != 0 {
		t.Errorf("CountPlanners = %d with no planners, want 0", n)
	}
}

// TestGetMalformedJSON verifies that Get returns an error when the worker file
// contains invalid JSON.
func TestGetMalformedJSON(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	if err := os.WriteFile(ws.WorkerPath("w-bad"), []byte("not valid json{{{"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Get(ws, "w-bad")
	if err == nil {
		t.Error("Get with malformed JSON should return an error, got nil")
	}
}

// TestListSkipsNonJSONFiles verifies that List ignores files without a .json
// extension in the workers directory.
func TestListSkipsNonJSONFiles(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Write a non-JSON file — should be silently ignored.
	if err := os.WriteFile(ws.WorkerPath("w-ignored")+".txt", []byte("ignored"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	workers, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(workers) != 0 {
		t.Errorf("List = %d workers, want 0 (non-JSON files must be skipped)", len(workers))
	}
}

// TestListSkipsSubdirectories verifies that List ignores sub-directories inside
// the workers directory.
func TestListSkipsSubdirectories(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create a sub-directory — should be silently ignored.
	if err := os.Mkdir(ws.WorkerPath("w-subdir"), 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	workers, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(workers) != 0 {
		t.Errorf("List = %d workers, want 0 (subdirectories must be skipped)", len(workers))
	}
}

// TestListWorkersNotExist verifies that List returns nil (not an error) when the
// workers directory has been removed, matching the "empty workspace" semantics.
func TestListWorkersNotExist(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	if err := os.RemoveAll(ws.WorkersDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	workers, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v (want nil when workers dir is absent)", err)
	}
	if workers != nil {
		t.Errorf("List = %v, want nil when workers dir is absent", workers)
	}
}

// TestListSkipsCorruptWorker verifies that List skips a worker whose JSON file is
// corrupted, still returning the healthy workers.
func TestListSkipsCorruptWorker(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	good := &Worker{
		ID:        "w-good1",
		PID:       os.Getpid(),
		TaskID:    "t-good",
		Agent:     "default",
		StartedAt: time.Now().Unix(),
	}
	if err := Register(ws, good); err != nil {
		t.Fatalf("Register good worker: %v", err)
	}

	// Write a corrupt .json file — should be skipped.
	if err := os.WriteFile(ws.WorkerPath("w-corrupt"), []byte("{invalid}"), 0644); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}

	workers, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(workers) != 1 {
		t.Errorf("List = %d workers, want 1 (corrupt entry must be skipped)", len(workers))
	}
	if workers[0].ID != good.ID {
		t.Errorf("List returned worker %q, want %q", workers[0].ID, good.ID)
	}
}

// TestListReadDirError verifies that List returns a non-nil error when the workers
// directory exists but cannot be read (permission denied — not os.ErrNotExist).
func TestListReadDirError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	_, err := List(ws)
	if err == nil {
		t.Error("List should return error when workers dir is unreadable, got nil")
	}
}

// TestCountTaskWorkersListError verifies that CountTaskWorkers propagates an error
// returned by List.
func TestCountTaskWorkersListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	_, err := CountTaskWorkers(ws)
	if err == nil {
		t.Error("CountTaskWorkers should return error when workers dir is unreadable, got nil")
	}
}

// TestCountPlannersListError verifies that CountPlanners propagates an error
// returned by List.
func TestCountPlannersListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	_, err := CountPlanners(ws)
	if err == nil {
		t.Error("CountPlanners should return error when workers dir is unreadable, got nil")
	}
}

// TestListStaleListError verifies that ListStale propagates an error returned
// by List (e.g. permission denied on the workers directory).
func TestListStaleListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	_, err := ListStale(ws, 180)
	if err == nil {
		t.Error("ListStale should return error when workers dir is unreadable, got nil")
	}
}

// TestIsAliveInvalidPIDs verifies that IsAlive returns false for zero and negative PIDs.
func TestIsAliveInvalidPIDs(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("pid=%d", pid), func(t *testing.T) {
			t.Parallel()
			if IsAlive(pid) {
				t.Errorf("IsAlive(%d) = true, want false", pid)
			}
		})
	}
}

// TestIsAliveSelfPID verifies that IsAlive returns true for the current process.
func TestIsAliveSelfPID(t *testing.T) {
	t.Parallel()
	if !IsAlive(os.Getpid()) {
		t.Error("IsAlive(os.Getpid()) = false, want true")
	}
}

// TestIsAliveDeadPID verifies that IsAlive returns false for a PID that does
// not correspond to a running process.
func TestIsAliveDeadPID(t *testing.T) {
	t.Parallel()
	// PID 999999999 is astronomically unlikely to be an active process.
	if IsAlive(999999999) {
		t.Error("IsAlive(999999999) = true, want false")
	}
}

// TestRegisterWriteError verifies that Register propagates an error when the
// workers directory is read-only and the file cannot be written.
func TestRegisterWriteError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test write errors when running as root")
	}
	ws := newWS(t)

	// Make the workers directory read-only so WriteFile fails.
	if err := os.Chmod(ws.WorkersDir(), 0444); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	w := &Worker{
		ID:        "w-err1",
		PID:       os.Getpid(),
		TaskID:    "t-err",
		Agent:     "default",
		StartedAt: time.Now().Unix(),
	}
	if err := Register(ws, w); err == nil {
		t.Error("Register should fail when workers dir is read-only, got nil")
	}
}

// TestListStaleWithFreshHeartbeatFile verifies that a worker with an old
// HeartbeatAt struct field is NOT considered stale when a fresh on-disk
// heartbeat file exists — the file timestamp takes precedence.
func TestListStaleWithFreshHeartbeatFile(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	selfPID := os.Getpid()
	oldTime := time.Now().Unix() - 600 // 10 minutes ago → stale without file

	w := &Worker{
		ID:          "w-hbfile1",
		PID:         selfPID,
		TaskID:      "t-hb-file-task",
		Agent:       "default",
		StartedAt:   oldTime,
		HeartbeatAt: oldTime,
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Write a fresh heartbeat file for this task ID.
	freshTS := time.Now().Unix()
	hbContent := fmt.Appendf(nil, "%d", freshTS)
	if err := os.WriteFile(ws.HeartbeatPath(w.TaskID), hbContent, 0644); err != nil {
		t.Fatalf("WriteFile heartbeat: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	for _, s := range stale {
		if s.ID == w.ID {
			t.Errorf("ListStale returned worker with fresh heartbeat file as stale — file should take precedence")
		}
	}
}

// TestListStaleAliveWorkerOldHeartbeat verifies that a worker with an alive PID
// but an old heartbeat (and no heartbeat file) IS returned by ListStale.
func TestListStaleAliveWorkerOldHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	selfPID := os.Getpid()
	oldTime := time.Now().Unix() - 600 // 10 minutes ago

	w := &Worker{
		ID:          "w-stale-alive1",
		PID:         selfPID,
		TaskID:      "t-stale-alive",
		Agent:       "default",
		StartedAt:   oldTime,
		HeartbeatAt: oldTime,
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	found := false
	for _, s := range stale {
		if s.ID == w.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListStale did not return alive worker with old heartbeat — it should be stale")
	}
}
