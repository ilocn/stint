package task_test

import (
	"testing"
	"time"

	"github.com/ilocn/stint/internal/task"
)

func TestHeartbeatWriteAndRead(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	if err := task.WriteHeartbeat(ws, "t-abc"); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}

	ts := task.ReadHeartbeat(ws, "t-abc")
	if ts == 0 {
		t.Error("ReadHeartbeat returned 0")
	}
	if time.Now().Unix()-ts > 2 {
		t.Error("heartbeat timestamp is too old")
	}
}

func TestReadHeartbeatMissing(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	ts := task.ReadHeartbeat(ws, "t-nonexistent")
	if ts != 0 {
		t.Errorf("ReadHeartbeat for missing file should return 0, got %d", ts)
	}
}

// TestIsStale is a table-driven test covering the three staleness conditions:
// missing heartbeat, fresh heartbeat within timeout, and stale heartbeat at
// zero timeout.
func TestIsStale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		writeFirst  bool // whether to write a heartbeat before calling IsStale
		timeoutSecs int64
		wantStale   bool
	}{
		{
			name:        "no heartbeat is always stale",
			writeFirst:  false,
			timeoutSecs: 60,
			wantStale:   true,
		},
		{
			name:        "fresh heartbeat within generous timeout is not stale",
			writeFirst:  true,
			timeoutSecs: 60,
			wantStale:   false,
		},
		{
			name:        "zero timeout immediately considers task stale",
			writeFirst:  true,
			timeoutSecs: 0,
			wantStale:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := newWS(t)
			taskID := "t-stale-test-" + tc.name
			if tc.writeFirst {
				if err := task.WriteHeartbeat(ws, taskID); err != nil {
					t.Fatalf("WriteHeartbeat: %v", err)
				}
			}
			got := task.IsStale(ws, taskID, tc.timeoutSecs)
			if got != tc.wantStale {
				t.Errorf("IsStale(timeout=%d) = %v, want %v",
					tc.timeoutSecs, got, tc.wantStale)
			}
		})
	}
}
