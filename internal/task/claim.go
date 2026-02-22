package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/user/stint/internal/workspace"
)

// ErrNoTask is returned when no claimable task is found.
var ErrNoTask = errors.New("no claimable task available")

// ClaimNext atomically claims the next available pending task for a worker.
// It uses O_CREATE|O_EXCL to guarantee exactly one caller wins the race.
// Tasks are attempted in ascending Seq order so lower-numbered tasks are
// always preferred over higher-numbered ones.
func ClaimNext(ws *workspace.Workspace, workerID string) (*Task, error) {
	entries, err := os.ReadDir(ws.PendingDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoTask
		}
		return nil, err
	}

	// Collect pending task IDs with their seq numbers so we can sort them.
	type taskEntry struct {
		id        string
		seq       int
		createdAt int64
	}
	var pending []taskEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		data, err := os.ReadFile(ws.PendingPath(id))
		if err != nil {
			continue
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		pending = append(pending, taskEntry{id: id, seq: t.Seq, createdAt: t.CreatedAt})
	}

	// Sort by seq ascending; break ties with creation time so the ordering is
	// deterministic and consistent with listDir.
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].seq != pending[j].seq {
			return pending[i].seq < pending[j].seq
		}
		return pending[i].createdAt < pending[j].createdAt
	})

	for _, entry := range pending {
		id := entry.id
		src := ws.PendingPath(id)

		// Re-read the task; another process may have claimed it between our
		// initial read (used only for sorting) and now.
		data, err := os.ReadFile(src)
		if err != nil {
			continue // file may have been claimed already
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}

		// Atomic claim: O_EXCL ensures exactly one caller creates this file.
		// Tasks in pending/ are guaranteed to have all deps met (Create and
		// UnblockReady enforce this invariant).
		t.WorkerID = workerID
		t.ClaimedAt = time.Now().Unix() // seconds — compared against staleThresholdSecs in recovery
		t.UpdatedAt = time.Now().UnixNano()

		claimedData, err := json.MarshalIndent(&t, "", "  ")
		if err != nil {
			continue
		}

		dst := ws.RunningPath(id)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			// EEXIST: another process claimed it first, try next.
			continue
		}
		_, writeErr := f.Write(claimedData)
		f.Close()
		if writeErr != nil {
			os.Remove(dst)
			continue
		}

		// Remove from pending (best-effort; recovery handles orphans).
		os.Remove(src)
		return &t, nil
	}
	return nil, ErrNoTask
}

// allDepsDone returns true if every task ID in deps is in the done directory.
func allDepsDone(ws *workspace.Workspace, deps []string) bool {
	for _, depID := range deps {
		if _, err := os.Stat(ws.DonePath(depID)); err != nil {
			return false
		}
	}
	return true
}
