package recovery

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ilocn/stint/internal/gitutil"
	"github.com/ilocn/stint/internal/goal"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/worker"
	"github.com/ilocn/stint/internal/workspace"
)

// DefaultStaleThresholdSecs is the age (in seconds) of a running task's
// claimed_at time beyond which it is considered abandoned during recovery.
const DefaultStaleThresholdSecs = 300 // 5 minutes

// Recover resets stale tasks, deletes orphaned worker records,
// and prunes orphaned git worktrees across all repos.
func Recover(ws *workspace.Workspace, staleThresholdSecs int64) error {
	var errs []error

	slog.Info("starting recovery pass")

	// 1. Kill and delete dead worker records. Reset goals/tasks they owned.
	workers, err := worker.List(ws)
	if err != nil {
		return fmt.Errorf("listing workers: %w", err)
	}
	for _, w := range workers {
		if !worker.IsAlive(w.PID) {
			slog.Info("deleting dead worker", slog.String("worker_id", w.ID), slog.Int("pid", w.PID))
			if err := worker.Delete(ws, w.ID); err != nil {
				errs = append(errs, err)
			}
			// Planner died — only reset goal if it's still in planning status.
			// If it already transitioned to active/done/failed, don't clobber it.
			if strings.HasPrefix(w.TaskID, "planner-") {
				goalID := w.TaskID[len("planner-"):]
				g, getErr := goal.Get(ws, goalID)
				if getErr == nil && g.Status == goal.StatusPlanning {
					slog.Warn("dead planner, resetting goal to queued", slog.String("goal_id", goalID))
					if err := goal.UpdateStatus(ws, goalID, goal.StatusQueued); err != nil {
						errs = append(errs, fmt.Errorf("reset goal %s: %w", goalID, err))
					}
				}
			}
		}
	}

	// 2. Reset running tasks that are stale (no matching live worker, or claimed too long ago).
	running, err := task.ListByStatus(ws, task.StatusRunning)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing running tasks: %w", err))
	} else {
		// Rebuild live worker task IDs after cleanup above.
		// If listing fails, skip stale-task resets entirely: proceeding with an empty
		// live-worker set would mark every running task as stale and incorrectly reset
		// all live workers.
		liveWorkers, lwErr := worker.List(ws)
		if lwErr != nil {
			errs = append(errs, fmt.Errorf("listing live workers after cleanup: %w", lwErr))
		} else {
			liveTaskIDs := make(map[string]bool)
			for _, w := range liveWorkers {
				liveTaskIDs[w.TaskID] = true
			}

			now := time.Now().Unix()
			for _, t := range running {
				isStale := !liveTaskIDs[t.ID] || (t.ClaimedAt > 0 && now-t.ClaimedAt > staleThresholdSecs)
				if isStale {
					slog.Warn("resetting stale task", slog.String("task_id", t.ID), slog.String("title", t.Title))
					if err := task.ResetToQueue(ws, t.ID); err != nil {
						errs = append(errs, err)
					}
					// Remove the stale worktree.
					os.RemoveAll(ws.WorktreePath(t.ID))
				}
			}
		}
	}

	// 3. Prune orphaned worktrees in all repos.
	for name, repoPath := range ws.Config.Repos {
		if _, err := os.Stat(repoPath); os.IsNotExist(err) {
			continue
		}
		slog.Info("pruning worktrees", slog.String("repo", name), slog.String("path", repoPath))
		if err := gitutil.WorktreePrune(repoPath); err != nil {
			slog.Warn("worktree prune warning", slog.String("repo", name), slog.Any("error", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("recovery completed with %d errors: %w", len(errs), errors.Join(errs...))
	}
	slog.Info("recovery pass complete")
	return nil
}
