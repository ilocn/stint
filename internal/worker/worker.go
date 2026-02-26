package worker

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ilocn/stint/internal/idgen"
	"github.com/ilocn/stint/internal/workspace"
)

// Worker tracks a running agent process.
type Worker struct {
	ID          string `json:"id"`
	PID         int    `json:"pid"`
	TaskID      string `json:"task_id"`
	Agent       string `json:"agent"`
	StartedAt   int64  `json:"started_at"`
	HeartbeatAt int64  `json:"heartbeat_at"`
}

// NewID returns a unique time-sortable worker ID like "w-00001abc123".
func NewID() string {
	return idgen.NewTimeSortableID("w")
}

// Register writes a worker record to disk.
func Register(ws *workspace.Workspace, w *Worker) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp := ws.WorkerPath(w.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, ws.WorkerPath(w.ID))
}

// Delete removes a worker record.
func Delete(ws *workspace.Workspace, id string) error {
	err := os.Remove(ws.WorkerPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Get reads a worker record by ID.
func Get(ws *workspace.Workspace, id string) (*Worker, error) {
	data, err := os.ReadFile(ws.WorkerPath(id))
	if err != nil {
		return nil, fmt.Errorf("read worker %s: %w", id, err)
	}
	var w Worker
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parse worker %s: %w", id, err)
	}
	return &w, nil
}

// List returns all current worker records.
func List(ws *workspace.Workspace) ([]*Worker, error) {
	entries, err := os.ReadDir(ws.WorkersDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var workers []*Worker
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		w, err := Get(ws, id)
		if err != nil {
			slog.Warn("skipping unreadable worker", slog.String("worker_id", id), slog.Any("error", err))
			continue
		}
		workers = append(workers, w)
	}
	return workers, nil
}

// Count returns the total number of active worker records (planners + task workers).
func Count(ws *workspace.Workspace) (int, error) {
	workers, err := List(ws)
	return len(workers), err
}

// CountTaskWorkers returns the number of active task worker records,
// excluding planner workers (TaskID prefix "planner-").
func CountTaskWorkers(ws *workspace.Workspace) (int, error) {
	workers, err := List(ws)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, w := range workers {
		if !strings.HasPrefix(w.TaskID, "planner-") {
			n++
		}
	}
	return n, nil
}

// CountPlanners returns the number of active planner worker records
// (TaskID prefix "planner-").
func CountPlanners(ws *workspace.Workspace) (int, error) {
	workers, err := List(ws)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, w := range workers {
		if strings.HasPrefix(w.TaskID, "planner-") {
			n++
		}
	}
	return n, nil
}

// IsAlive checks if a process with the given PID is still running.
// Uses kill(pid, 0) which works on macOS and Linux without requiring /proc.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if the process exists without sending a real signal.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// ListStale returns workers whose heartbeat is older than timeoutSecs
// or whose PID is no longer alive.
//
// Planner workers (TaskID starting with "planner-") are managed exclusively
// by monitorPlanner goroutines and startupRecovery. They are never included
// in the stale list — this avoids a race where healthCheck fires between
// proc.Wait() returning and worker.Delete() being called in monitorPlanner.
//
// For regular task workers, the most recent of (a) the worker struct's
// HeartbeatAt field and (b) the on-disk heartbeat file written by
// "st heartbeat <task-id>" is used as the last-alive timestamp.
func ListStale(ws *workspace.Workspace, timeoutSecs int64) ([]*Worker, error) {
	workers, err := List(ws)
	if err != nil {
		return nil, err
	}
	var stale []*Worker
	now := time.Now().Unix()
	for _, w := range workers {
		// Planners are managed exclusively by monitorPlanner and startupRecovery.
		// Never include them here — avoids a race where healthCheck fires between
		// proc.Wait() returning and worker.Delete() being called in monitorPlanner.
		if strings.HasPrefix(w.TaskID, "planner-") {
			continue
		}
		// A dead process is always stale.
		if !IsAlive(w.PID) {
			stale = append(stale, w)
			continue
		}
		// For regular workers, use the most recent of the struct field and
		// the on-disk heartbeat file (written by "st heartbeat <task-id>").
		lastHeartbeat := w.HeartbeatAt
		if data, err := os.ReadFile(ws.HeartbeatPath(w.TaskID)); err == nil {
			if ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil && ts > lastHeartbeat {
				lastHeartbeat = ts
			}
		}
		if now-lastHeartbeat > timeoutSecs {
			stale = append(stale, w)
		}
	}
	return stale, nil
}

// UpdateHeartbeat refreshes the heartbeat timestamp on a worker record.
func UpdateHeartbeat(ws *workspace.Workspace, id string) error {
	w, err := Get(ws, id)
	if err != nil {
		return err
	}
	w.HeartbeatAt = time.Now().Unix()
	return Register(ws, w)
}
