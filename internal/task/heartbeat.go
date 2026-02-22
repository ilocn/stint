package task

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/user/stint/internal/workspace"
)

// DefaultTimeoutSecs is the number of seconds without a heartbeat before a
// worker is considered dead.
const DefaultTimeoutSecs = 180 // 3 minutes

// WriteHeartbeat records the current timestamp for a task.
func WriteHeartbeat(ws *workspace.Workspace, taskID string) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return os.WriteFile(ws.HeartbeatPath(taskID), []byte(ts), 0644)
}

// ReadHeartbeat returns the last heartbeat timestamp for a task, or 0 if none.
func ReadHeartbeat(ws *workspace.Workspace, taskID string) int64 {
	data, err := os.ReadFile(ws.HeartbeatPath(taskID))
	if err != nil {
		return 0
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return ts
}

// IsStale returns true if the task's heartbeat is older than timeoutSecs,
// or if no heartbeat exists.
func IsStale(ws *workspace.Workspace, taskID string, timeoutSecs int64) bool {
	ts := ReadHeartbeat(ws, taskID)
	if ts == 0 {
		return true
	}
	return time.Now().Unix()-ts >= timeoutSecs
}
