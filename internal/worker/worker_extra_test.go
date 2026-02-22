package worker

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestListReadDirPermissionError verifies that List returns a non-nil, non-NotExist
// error when the workers directory is unreadable (permission denied).
// This covers the `return nil, err` branch in List for non-NotExist errors,
// and also transitively covers the error paths in CountTaskWorkers, CountPlanners,
// and ListStale (which all call List internally).
func TestListReadDirPermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors when running as root")
	}
	ws := newWS(t)

	// Make workers dir unreadable so os.ReadDir returns EACCES (not ENOENT).
	if err := os.Chmod(ws.WorkersDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.WorkersDir(), 0755) //nolint:errcheck

	// List must return a non-nil error that is NOT os.IsNotExist.
	_, err := List(ws)
	if err == nil {
		t.Error("List should return error when workers dir is unreadable, got nil")
	}
	if os.IsNotExist(err) {
		t.Errorf("List error should not be NotExist when dir is unreadable; got: %v", err)
	}

	// CountTaskWorkers must propagate the error from List.
	_, ctErr := CountTaskWorkers(ws)
	if ctErr == nil {
		t.Error("CountTaskWorkers should return error when workers dir is unreadable")
	}

	// CountPlanners must propagate the error from List.
	_, cpErr := CountPlanners(ws)
	if cpErr == nil {
		t.Error("CountPlanners should return error when workers dir is unreadable")
	}

	// ListStale must propagate the error from List.
	_, lsErr := ListStale(ws, 180)
	if lsErr == nil {
		t.Error("ListStale should return error when workers dir is unreadable")
	}
}

// TestIsAliveWithNegativePIDs verifies that IsAlive returns false for various
// negative and invalid PID values, exercising the `if pid <= 0 { return false }` branch.
func TestIsAliveWithNegativePIDs(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{-1, -100, -999} {
		pid := pid
		t.Run(fmt.Sprintf("pid=%d", pid), func(t *testing.T) {
			t.Parallel()
			if IsAlive(pid) {
				t.Errorf("IsAlive(%d) = true, want false", pid)
			}
		})
	}
}

// TestListStaleHeartbeatFileOlderThanStructField verifies that when a heartbeat
// file has an OLDER timestamp than the worker's HeartbeatAt field, the struct
// field takes precedence and the worker is not incorrectly marked stale.
func TestListStaleHeartbeatFileOlderThanStructField(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	selfPID := os.Getpid()
	now := time.Now().Unix()

	w := &Worker{
		ID:          "w-hb-older-file",
		PID:         selfPID,
		TaskID:      "t-hb-older",
		Agent:       "default",
		StartedAt:   now,
		HeartbeatAt: now, // fresh struct field
	}
	if err := Register(ws, w); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Write a heartbeat file with an OLDER timestamp than the struct HeartbeatAt.
	// The struct field (now) is fresher, so the worker must NOT appear stale.
	oldTS := now - 600 // 10 minutes ago
	hbContent := []byte(fmt.Sprintf("%d", oldTS))
	if err := os.WriteFile(ws.HeartbeatPath(w.TaskID), hbContent, 0644); err != nil {
		t.Fatalf("WriteFile heartbeat: %v", err)
	}

	stale, err := ListStale(ws, 180)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	for _, s := range stale {
		if s.ID == w.ID {
			t.Errorf("ListStale returned worker even though struct HeartbeatAt is fresh — struct field should win over older file")
		}
	}
}
