package goal_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/user/stint/internal/goal"
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

func TestCreateAndGet(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "add user auth", []string{"use JWT"}, []string{"api"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.ID == "" {
		t.Error("ID is empty")
	}
	if g.Status != goal.StatusQueued {
		t.Errorf("Status = %s, want queued", g.Status)
	}
	if g.Text != "add user auth" {
		t.Errorf("Text = %s", g.Text)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != g.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, g.ID)
	}
	if len(got.Hints) != 1 || got.Hints[0] != "use JWT" {
		t.Error("hints not preserved")
	}
	if len(got.Repos) != 1 || got.Repos[0] != "api" {
		t.Error("repos not preserved")
	}
}

func TestCreateDuplicate(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "add user auth", nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	g2, err := goal.Create(ws, "add user auth", nil, nil)
	if !errors.Is(err, goal.ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
	if g2 == nil {
		t.Fatal("expected existing goal to be returned, got nil")
	}
	if g2.ID != g.ID {
		t.Errorf("duplicate Create returned ID %s, want %s", g2.ID, g.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if _, err := goal.Get(ws, "g-nonexistent"); err == nil {
		t.Error("expected error for missing goal")
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if _, err := goal.Create(ws, "first", nil, nil); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if _, err := goal.Create(ws, "second", nil, nil); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(goals) != 2 {
		t.Errorf("List returned %d goals, want 2", len(goals))
	}
	// Should be sorted by created_at.
	if goals[0].Text != "first" {
		t.Error("goals not sorted by created_at")
	}
}

func TestListEmptyWorkspace(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List on empty workspace: %v", err)
	}
	if len(goals) != 0 {
		t.Errorf("expected 0 goals, got %d", len(goals))
	}
}

func TestUpdateStatus(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "test", nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, status := range []string{goal.StatusPlanning, goal.StatusActive, goal.StatusDone} {
		if err := goal.UpdateStatus(ws, g.ID, status); err != nil {
			t.Fatalf("UpdateStatus(%s): %v", status, err)
		}
		got, err := goal.Get(ws, g.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != status {
			t.Errorf("Status = %s, want %s", got.Status, status)
		}
	}
}

func TestActiveRepos(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g1, err := goal.Create(ws, "first", nil, []string{"api"})
	if err != nil {
		t.Fatalf("Create g1: %v", err)
	}
	g2, err := goal.Create(ws, "second", nil, []string{"frontend"})
	if err != nil {
		t.Fatalf("Create g2: %v", err)
	}

	if err := goal.UpdateStatus(ws, g1.ID, goal.StatusActive); err != nil {
		t.Fatalf("UpdateStatus g1: %v", err)
	}
	// g2 is still queued.

	active, err := goal.ActiveRepos(ws)
	if err != nil {
		t.Fatalf("ActiveRepos: %v", err)
	}
	if !active["api"] {
		t.Error("api should be active")
	}
	if active["frontend"] {
		t.Error("frontend should not be active (goal is queued)")
	}

	if err := goal.UpdateStatus(ws, g2.ID, goal.StatusPlanning); err != nil {
		t.Fatalf("UpdateStatus g2: %v", err)
	}
	active, err = goal.ActiveRepos(ws)
	if err != nil {
		t.Fatalf("ActiveRepos after update: %v", err)
	}
	if !active["frontend"] {
		t.Error("frontend should be active after marking planning")
	}
}

// TestGoalNewIDFormat is a regression test verifying that goal.NewID() still
// produces correctly formatted IDs after the time-sortable ID migration.
func TestGoalNewIDFormat(t *testing.T) {
	t.Parallel()
	id := goal.NewID()
	if !strings.HasPrefix(id, "g-") {
		t.Errorf("goal.NewID() = %q, want prefix \"g-\"", id)
	}
	suffix := id[2:] // strip "g-"
	if len(suffix) != 11 {
		t.Errorf("goal.NewID() suffix %q has len=%d, want 11", suffix, len(suffix))
	}
	for _, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("goal.NewID() = %q contains non-base36 char %q", id, c)
		}
	}
}

// TestGoalNewIDTemporalSort verifies that goal IDs generated at different
// times sort lexicographically in temporal order.
func TestGoalNewIDTemporalSort(t *testing.T) {
	t.Parallel()
	id1 := goal.NewID()
	time.Sleep(2 * time.Millisecond)
	id2 := goal.NewID()
	if id1[2:] >= id2[2:] {
		t.Errorf("goal IDs not temporally sorted: %q >= %q", id1, id2)
	}
}

// TestUpdateStatusNotFound verifies that UpdateStatus returns an error for a
// non-existent goal ID, covering the Get-error path in UpdateStatus.
func TestUpdateStatusNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := goal.UpdateStatus(ws, "g-nonexistent", goal.StatusDone); err == nil {
		t.Error("expected error for non-existent goal")
	}
}

// TestSetBranch verifies that SetBranch persists the branch name and refreshes UpdatedAt.
func TestSetBranch(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "branch test goal", nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const branch = "st/goals/g-abc123"
	if err := goal.SetBranch(ws, g.ID, branch); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}
	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Branch != branch {
		t.Errorf("Branch = %q, want %q", got.Branch, branch)
	}
	if got.UpdatedAt <= g.UpdatedAt {
		t.Error("UpdatedAt should have increased after SetBranch")
	}
}

// TestSetBranchNotFound verifies that SetBranch returns an error when the goal
// does not exist, covering the Get-error return path in SetBranch.
func TestSetBranchNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := goal.SetBranch(ws, "g-nonexistent", "some-branch"); err == nil {
		t.Error("expected error for non-existent goal")
	}
}

// TestListGoalsDirNotExist verifies that List returns an empty slice (not an
// error) when the goals directory has been deleted — covering the IsNotExist
// branch of the ReadDir error check.
func TestListGoalsDirNotExist(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// workspace.Init creates an empty goals dir; remove it to trigger the
	// os.IsNotExist branch.
	if err := os.Remove(ws.GoalsDir()); err != nil {
		t.Fatalf("Remove goals dir: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List with missing goals dir should return nil error, got: %v", err)
	}
	if len(goals) != 0 {
		t.Errorf("expected 0 goals, got %d", len(goals))
	}
}

// TestListGoalsDirIsFile verifies that List returns an error when the goals
// directory path is occupied by a regular file — covering the non-IsNotExist
// ReadDir error branch.
func TestListGoalsDirIsFile(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Replace the empty goals directory with a regular file so ReadDir fails
	// with a non-IsNotExist error (ENOTDIR on most platforms).
	if err := os.Remove(ws.GoalsDir()); err != nil {
		t.Fatalf("Remove goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := goal.List(ws); err == nil {
		t.Error("expected error when goals dir is a regular file")
	}
}

// TestCreateWhenListFails verifies that Create propagates errors returned by
// List, covering the early-return error path inside Create.
func TestCreateWhenListFails(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Corrupt the goals directory so that List returns an error.
	if err := os.Remove(ws.GoalsDir()); err != nil {
		t.Fatalf("Remove goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := goal.Create(ws, "test", nil, nil); err == nil {
		t.Error("expected Create to propagate List error when goals dir is corrupt")
	}
}

// TestActiveReposListFails verifies that ActiveRepos propagates the error from
// List when the goals directory is unreadable.
func TestActiveReposListFails(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Corrupt the goals directory so List fails.
	if err := os.Remove(ws.GoalsDir()); err != nil {
		t.Fatalf("Remove goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := goal.ActiveRepos(ws); err == nil {
		t.Error("expected ActiveRepos to propagate List error when goals dir is corrupt")
	}
}

// TestGetReadFileError verifies that Get returns an error (not a not-found
// error) when the goal path exists but cannot be read as a file — covering the
// non-IsNotExist error path in Get.
func TestGetReadFileError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a directory at the path where the goal JSON would be so that
	// os.ReadFile fails with EISDIR (not os.IsNotExist).
	fakePath := ws.GoalPath("g-dirnotfile")
	if err := os.MkdirAll(fakePath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := goal.Get(ws, "g-dirnotfile"); err == nil {
		t.Error("expected error when goal file path is occupied by a directory")
	}
}

// TestListSkipsNonJsonFiles verifies that List silently ignores files whose
// extension is not ".json" and directory entries inside the goals directory.
func TestListSkipsNonJsonFiles(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "real goal", nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Add a non-.json file — must be skipped.
	if err := os.WriteFile(filepath.Join(ws.GoalsDir(), "ignore.txt"), []byte("skip"), 0644); err != nil {
		t.Fatalf("WriteFile non-json: %v", err)
	}
	// Add a subdirectory entry — must also be skipped.
	if err := os.MkdirAll(filepath.Join(ws.GoalsDir(), "subdir"), 0755); err != nil {
		t.Fatalf("MkdirAll subdir: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(goals) != 1 || goals[0].ID != g.ID {
		t.Errorf("List returned %d goals (want 1 with ID %s)", len(goals), g.ID)
	}
}

// TestListSkipsCorruptJson verifies that List silently skips goal files that
// contain invalid JSON, covering the Get-error continue path in the List loop.
func TestListSkipsCorruptJson(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	g, err := goal.Create(ws, "valid goal", nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Write a .json file with invalid content — Get will fail to unmarshal it
	// and List should silently skip it.
	corruptPath := filepath.Join(ws.GoalsDir(), "g-corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{bad json}"), 0644); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}
	goals, err := goal.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(goals) != 1 || goals[0].ID != g.ID {
		t.Errorf("List returned %d goals (want 1 with ID %s)", len(goals), g.ID)
	}
}
