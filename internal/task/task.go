package task

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ilocn/stint/internal/idgen"
	"github.com/ilocn/stint/internal/workspace"
)

// Status values — the directory a task lives in IS its status.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusBlocked   = "blocked"
)

// MaxResetRetries is the maximum number of times a task can be reset to the
// queue by the health check before it is permanently failed.
const MaxResetRetries = 3

// MaxExplicitFailRetries is the maximum number of times the supervisor will
// auto-retry a task with failure context injected before permanently failing it.
const MaxExplicitFailRetries = 2

// Result holds what a worker produced.
type Result struct {
	Summary string `json:"summary"`
	Branch  string `json:"branch,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// Task is one atomic unit of work.
type Task struct {
	ID                string   `json:"id"`
	GoalID            string   `json:"goal_id"`
	Seq               int      `json:"seq"`
	Title             string   `json:"title"`
	Agent             string   `json:"agent"`
	Prompt            string   `json:"prompt"`
	Repo              string   `json:"repo"`
	GoalBranch        string   `json:"goal_branch,omitempty"`
	BranchName        string   `json:"branch_name,omitempty"`
	ContextFrom       []string `json:"context_from,omitempty"`
	DepTaskIDs        []string `json:"dep_task_ids,omitempty"`
	WorkerID          string   `json:"worker_id,omitempty"`
	ClaimedAt         int64    `json:"claimed_at,omitempty"`
	CompletedAt       int64    `json:"completed_at,omitempty"`
	Result            *Result  `json:"result,omitempty"`
	ErrorMsg          string   `json:"error_msg,omitempty"`
	RetryCount        int      `json:"retry_count,omitempty"`
	ExplicitFailCount int      `json:"explicit_fail_count,omitempty"`
	FailureHistory    []string `json:"failure_history,omitempty"`
	AttemptedDiff     string   `json:"attempted_diff,omitempty"`
	CreatedAt         int64    `json:"created_at"`
	UpdatedAt         int64    `json:"updated_at"`
}

// NewID returns a unique time-sortable task ID like "t-00001abc123".
func NewID() string {
	return idgen.NewTimeSortableID("t")
}

// Create writes a new task to the workspace.
// Tasks with unmet dependencies go to blocked/; others go to pending/.
func Create(ws *workspace.Workspace, t *Task) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	now := time.Now().UnixNano()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Agent == "" {
		t.Agent = "impl"
	}
	if len(t.DepTaskIDs) > 0 && !allDepsDone(ws, t.DepTaskIDs) {
		return writeToDir(ws.BlockedDir(), t)
	}
	return writeToDir(ws.PendingDir(), t)
}

// UnblockReady moves any blocked tasks whose dependencies are now all done
// into pending/. Call this after completing a task.
func UnblockReady(ws *workspace.Workspace) error {
	blocked, err := ListByStatus(ws, StatusBlocked)
	if err != nil {
		return err
	}
	for _, t := range blocked {
		if allDepsDone(ws, t.DepTaskIDs) {
			t.UpdatedAt = time.Now().UnixNano()
			if err := writeToDir(ws.PendingDir(), t); err != nil {
				return err
			}
			os.Remove(ws.BlockedPath(t.ID))
		}
	}
	return nil
}

// GetByStatus finds a task with the given ID in the given status directory.
// If status is empty, all status directories are searched.
func GetByStatus(ws *workspace.Workspace, id, status string) (*Task, string, error) {
	dirs := map[string]string{
		StatusPending:   ws.PendingDir(),
		StatusRunning:   ws.RunningDir(),
		StatusDone:      ws.DoneDir(),
		StatusFailed:    ws.FailedDir(),
		StatusCancelled: ws.CancelledDir(),
		StatusBlocked:   ws.BlockedDir(),
	}
	if status != "" {
		dir, ok := dirs[status]
		if !ok {
			return nil, "", fmt.Errorf("unknown status: %s", status)
		}
		dirs = map[string]string{status: dir}
	}
	for s, dir := range dirs {
		path := filepath.Join(dir, id+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Permission or other error: if we're searching all statuses, skip
			// this directory and keep looking elsewhere.  If we were asked to
			// look in a specific directory, propagate the error.
			if status != "" {
				return nil, "", err
			}
			continue
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, "", fmt.Errorf("malformed task %s: %w", id, err)
		}
		return &t, s, nil
	}
	return nil, "", fmt.Errorf("task %s not found", id)
}

// Get finds a task by ID across all status directories.
func Get(ws *workspace.Workspace, id string) (*Task, string, error) {
	return GetByStatus(ws, id, "")
}

// ListByStatus returns tasks from a specific status directory.
func ListByStatus(ws *workspace.Workspace, status string) ([]*Task, error) {
	dir := ws.StatusDir(status)
	if dir == "" {
		return nil, fmt.Errorf("unknown status: %s", status)
	}
	return listDir(dir)
}

// ListAll returns all tasks across all status directories, with their status.
type TaskWithStatus struct {
	Task   *Task
	Status string
}

func ListAll(ws *workspace.Workspace) ([]*TaskWithStatus, error) {
	statuses := []string{StatusPending, StatusRunning, StatusDone, StatusFailed, StatusCancelled, StatusBlocked}
	var all []*TaskWithStatus
	for _, s := range statuses {
		tasks, err := ListByStatus(ws, s)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			all = append(all, &TaskWithStatus{Task: t, Status: s})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Task.CreatedAt < all[j].Task.CreatedAt
	})
	return all, nil
}

// ListForGoal returns all tasks for a specific goal.
func ListForGoal(ws *workspace.Workspace, goalID string) ([]*TaskWithStatus, error) {
	all, err := ListAll(ws)
	if err != nil {
		return nil, err
	}
	var filtered []*TaskWithStatus
	for _, ts := range all {
		if ts.Task.GoalID == goalID {
			filtered = append(filtered, ts)
		}
	}
	return filtered, nil
}

// NextSeq returns the next sequence number for a goal's tasks.
func NextSeq(ws *workspace.Workspace, goalID string) (int, error) {
	tasks, err := ListForGoal(ws, goalID)
	if err != nil {
		return 1, err
	}
	max := 0
	for _, ts := range tasks {
		if ts.Task.Seq > max {
			max = ts.Task.Seq
		}
	}
	return max + 1, nil
}

// writeToDir atomically writes a task JSON to the given directory.
func writeToDir(dir string, t *Task) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, t.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// listDir reads all task JSON files from a directory.
func listDir(dir string) ([]*Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []*Task
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable task file", slog.String("path", path), slog.Any("error", err))
			continue
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			slog.Warn("skipping malformed task file", slog.String("path", path), slog.Any("error", err))
			continue
		}
		tasks = append(tasks, &t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Seq != tasks[j].Seq {
			return tasks[i].Seq < tasks[j].Seq
		}
		return tasks[i].CreatedAt < tasks[j].CreatedAt
	})
	return tasks, nil
}
