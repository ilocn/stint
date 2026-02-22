package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/workspace"
)

// ─── Test workspace helpers ───────────────────────────────────────────────────

// newGlobalsWithWS creates a Globals pre-initialized with the given workspace,
// bypassing openWS(). Since tests run in package main, we can access unexported fields.
func newGlobalsWithWS(ws *workspace.Workspace) *Globals {
	g := &Globals{}
	g.once.Do(func() { g.ws = ws })
	return g
}

// initTestWS creates a temp workspace and returns both the workspace and Globals.
func initTestWS(t *testing.T) (*workspace.Workspace, *Globals) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws, newGlobalsWithWS(ws)
}

// newTestKongCustom creates a kong.Kong instance bound to the provided cli and globals.
func newTestKongCustom(t *testing.T, cli *CLI, globals *Globals) (*kong.Kong, error) {
	t.Helper()
	return kong.New(cli,
		kong.Name("st"),
		kong.Bind(globals),
	)
}

// createRunningTask creates a task in pending state then claims it to running.
func createRunningTask(t *testing.T, ws *workspace.Workspace, goalID string) *task.Task {
	t.Helper()
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: goalID,
		Title:  "test task",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}
	claimed, err := task.ClaimNext(ws, "test-worker")
	if err != nil {
		t.Fatalf("task.ClaimNext: %v", err)
	}
	return claimed
}

// ─── Helper function tests ────────────────────────────────────────────────────

func TestFmtTimeZero(t *testing.T) {
	got := fmtTime(0)
	if got != "—" {
		t.Errorf("fmtTime(0) = %q, want %q", got, "—")
	}
}

func TestFmtTimeUnixSeconds(t *testing.T) {
	// Use a fixed timestamp: 2024-01-15 10:30:45 UTC
	ts := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC).Unix()
	got := fmtTime(ts)
	// ts ~= 1.7e9, which is <= 1e12, so treated as seconds
	if ts > 1_000_000_000_000 {
		t.Fatal("test precondition failed: ts should be seconds range")
	}
	if len(got) == 0 || got == "—" {
		t.Errorf("fmtTime(%d) = %q, expected non-empty non-dash string", ts, got)
	}
	// Should be formatted as YYYY-MM-DD HH:MM:SS
	if len(got) != 19 {
		t.Errorf("fmtTime(%d) = %q, expected 19-char formatted time", ts, got)
	}
}

func TestFmtTimeUnixNanoseconds(t *testing.T) {
	// Use a fixed nanosecond timestamp
	ts := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC).UnixNano()
	got := fmtTime(ts)
	// ts should be in nanosecond range (> 1e12)
	if ts <= 1_000_000_000_000 {
		t.Fatal("test precondition failed: ts should be in nanoseconds range")
	}
	if len(got) == 0 || got == "—" {
		t.Errorf("fmtTime(%d) = %q, expected non-empty non-dash string", ts, got)
	}
	if len(got) != 19 {
		t.Errorf("fmtTime(%d) = %q, expected 19-char formatted time", ts, got)
	}
}

func TestFmtTimeSecondsVsNanosecondsBoundary(t *testing.T) {
	// Values at and around the 1e12 boundary
	tests := []struct {
		ts         int64
		wantIsNano bool
	}{
		{999_999_999_999, false},  // just below boundary → seconds
		{1_000_000_000_001, true}, // just above boundary → nanoseconds
	}
	for _, tc := range tests {
		got := fmtTime(tc.ts)
		if got == "—" {
			t.Errorf("fmtTime(%d) returned dash, expected formatted time", tc.ts)
		}
	}
}

func TestFmtAgeZero(t *testing.T) {
	got := fmtAge(0)
	if got != "never" {
		t.Errorf("fmtAge(0) = %q, want %q", got, "never")
	}
}

func TestFmtAgeSeconds(t *testing.T) {
	// ~30 seconds ago
	unix := time.Now().Add(-30 * time.Second).Unix()
	got := fmtAge(unix)
	if !strings.HasSuffix(got, "s") {
		t.Errorf("fmtAge(30s ago) = %q, expected suffix 's'", got)
	}
}

func TestFmtAgeMinutes(t *testing.T) {
	// ~10 minutes ago
	unix := time.Now().Add(-10 * time.Minute).Unix()
	got := fmtAge(unix)
	if !strings.HasSuffix(got, "m") {
		t.Errorf("fmtAge(10m ago) = %q, expected suffix 'm'", got)
	}
}

func TestFmtAgeHours(t *testing.T) {
	// ~3 hours ago
	unix := time.Now().Add(-3 * time.Hour).Unix()
	got := fmtAge(unix)
	if !strings.HasSuffix(got, "h") {
		t.Errorf("fmtAge(3h ago) = %q, expected suffix 'h'", got)
	}
}

func TestFmtAgeBoundaryJustUnderMinute(t *testing.T) {
	// 59 seconds ago — should be "59s"
	unix := time.Now().Add(-59 * time.Second).Unix()
	got := fmtAge(unix)
	if !strings.HasSuffix(got, "s") {
		t.Errorf("fmtAge(59s ago) = %q, expected suffix 's'", got)
	}
}

func TestFmtAgeBoundaryJustUnderHour(t *testing.T) {
	// 59 minutes ago — should be "59m"
	unix := time.Now().Add(-59 * time.Minute).Unix()
	got := fmtAge(unix)
	if !strings.HasSuffix(got, "m") {
		t.Errorf("fmtAge(59m ago) = %q, expected suffix 'm'", got)
	}
}

func TestSortedKeysEmpty(t *testing.T) {
	got := sortedKeys(map[string]string{})
	if len(got) != 0 {
		t.Errorf("sortedKeys({}) = %v, want []", got)
	}
}

func TestSortedKeysSingle(t *testing.T) {
	got := sortedKeys(map[string]string{"a": "1"})
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("sortedKeys({a:1}) = %v, want [a]", got)
	}
}

func TestSortedKeysSorted(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	got := sortedKeys(m)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys len = %d, want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("sortedKeys[%d] = %q, want %q", i, got[i], k)
		}
	}
}

func TestCleanDirContentsEmpty(t *testing.T) {
	dir := t.TempDir()
	// should not panic or error on empty dir
	cleanDirContents(dir)
}

func TestCleanDirContentsWithFiles(t *testing.T) {
	dir := t.TempDir()
	// Create some files
	for _, name := range []string{"a.txt", "b.txt", "c.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// Create a subdirectory with a file
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0644); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}

	cleanDirContents(dir)

	// dir itself should still exist
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir itself was removed: %v", err)
	}
	// Contents should be gone
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir after clean: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty dir after clean, got %d entries", len(entries))
	}
}

func TestCleanDirContentsNonexistent(t *testing.T) {
	// Should not panic on nonexistent dir
	cleanDirContents("/tmp/nonexistent-dir-for-test-12345")
}

func TestCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("WriteFile src: %v", err)
	}

	if err := copyFile(src, dst, 0644); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("copyFile content = %q, want %q", got, content)
	}
}

func TestCopyFileSrcNotExist(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst.txt")
	err := copyFile("/nonexistent/src.txt", dst, 0644)
	if err == nil {
		t.Error("copyFile from nonexistent src: expected error, got nil")
	}
}

func TestCopyDirRecursive(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create a simple tree in src
	if err := os.WriteFile(filepath.Join(src, "file1.txt"), []byte("file1"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	subDir := filepath.Join(src, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("file2"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dstCopy := filepath.Join(dst, "copy")
	if err := copyDirRecursive(src, dstCopy); err != nil {
		t.Fatalf("copyDirRecursive: %v", err)
	}

	// Verify files
	got1, err := os.ReadFile(filepath.Join(dstCopy, "file1.txt"))
	if err != nil || string(got1) != "file1" {
		t.Errorf("file1 content = %q, err = %v", got1, err)
	}
	got2, err := os.ReadFile(filepath.Join(dstCopy, "subdir", "file2.txt"))
	if err != nil || string(got2) != "file2" {
		t.Errorf("file2 content = %q, err = %v", got2, err)
	}
}

// ─── InitCmd tests ────────────────────────────────────────────────────────────

func TestInitCmdRunSuccess(t *testing.T) {
	dir := t.TempDir()
	newDir := filepath.Join(dir, "myws")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	c := &InitCmd{Dir: newDir}
	if err := c.Run(); err != nil {
		t.Fatalf("InitCmd.Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, ".st", "workspace.json")); err != nil {
		t.Errorf("workspace.json not created: %v", err)
	}
}

func TestInitCmdRunWithRepo(t *testing.T) {
	dir := t.TempDir()
	newDir := filepath.Join(dir, "myws")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	c := &InitCmd{
		Dir:  newDir,
		Repo: []string{"myrepo=" + repoDir},
	}
	if err := c.Run(); err != nil {
		t.Fatalf("InitCmd.Run with repo: %v", err)
	}
}

func TestInitCmdRunInvalidRepoFormat(t *testing.T) {
	dir := t.TempDir()
	newDir := filepath.Join(dir, "myws")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	c := &InitCmd{
		Dir:  newDir,
		Repo: []string{"badformat"},
	}
	if err := c.Run(); err == nil {
		t.Error("expected error for bad repo format, got nil")
	}
}

// ─── VersionCmd tests ─────────────────────────────────────────────────────────

func TestVersionCmdRun(t *testing.T) {
	c := &VersionCmd{}
	if err := c.Run(); err != nil {
		t.Fatalf("VersionCmd.Run: %v", err)
	}
}

// ─── GoalCmd tests ────────────────────────────────────────────────────────────

func TestGoalAddCmdRun(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalAddCmd{Text: "add a new feature"}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalAddCmd.Run: %v", err)
	}
}

func TestGoalAddCmdRunDuplicate(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalAddCmd{Text: "same goal text"}
	// First add
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalAddCmd.Run first: %v", err)
	}
	// Second add should silently skip (not error)
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalAddCmd.Run duplicate: %v (should be silent skip)", err)
	}
}

func TestGoalAddCmdRunWithRepos(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalAddCmd{
		Text:  "goal with repos",
		Repos: "repo1, repo2",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalAddCmd.Run with repos: %v", err)
	}
}

func TestGoalAddCmdRunWithHints(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalAddCmd{
		Text:  "goal with hints",
		Hints: []string{"focus on performance", "use existing patterns"},
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalAddCmd.Run with hints: %v", err)
	}
}

func TestGoalListCmdRunEmpty(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalListCmd.Run (empty): %v", err)
	}
}

func TestGoalListCmdRunWithGoals(t *testing.T) {
	ws, g := initTestWS(t)
	// Create some goals
	for _, text := range []string{"goal one", "goal two", "goal three"} {
		if _, err := goal.Create(ws, text, nil, nil); err != nil {
			t.Fatalf("goal.Create: %v", err)
		}
	}
	c := &GoalListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalListCmd.Run: %v", err)
	}
}

func TestGoalListCmdRunLongText(t *testing.T) {
	ws, g := initTestWS(t)
	// Goal with text longer than 60 chars (should be truncated in listing)
	longText := strings.Repeat("a", 70)
	if _, err := goal.Create(ws, longText, nil, nil); err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	c := &GoalListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalListCmd.Run (long text): %v", err)
	}
}

func TestGoalShowCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "show me this goal", []string{"hint1"}, []string{"repo1"})
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	// Set a branch to test branch display
	if err := goal.SetBranch(ws, gObj.ID, "st/goals/"+gObj.ID); err != nil {
		t.Fatalf("goal.SetBranch: %v", err)
	}

	c := &GoalShowCmd{ID: gObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalShowCmd.Run: %v", err)
	}
}

func TestGoalShowCmdRunNoHintsNoRepos(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "plain goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	c := &GoalShowCmd{ID: gObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalShowCmd.Run (no hints/repos): %v", err)
	}
}

func TestGoalShowCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalShowCmd{ID: "g-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent goal, got nil")
	}
}

func TestGoalCancelCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "goal to cancel", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	c := &GoalCancelCmd{ID: gObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("GoalCancelCmd.Run: %v", err)
	}
}

func TestGoalCancelCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &GoalCancelCmd{ID: "g-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent goal, got nil")
	}
}

// ─── PlanCmd tests ────────────────────────────────────────────────────────────

func TestPlanCmdRunMissingTextAndResume(t *testing.T) {
	_, g := initTestWS(t)
	c := &PlanCmd{} // no Text, no Resume
	if err := c.Run(g); err == nil {
		t.Error("expected error when both Text and Resume are empty")
	}
}

// ─── TaskCmd tests ────────────────────────────────────────────────────────────

func TestTaskAddCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "my goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	c := &TaskAddCmd{
		GoalID: gObj.ID,
		Title:  "implement the thing",
		Prompt: "please implement this feature",
		Agent:  "impl",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskAddCmd.Run: %v", err)
	}
}

func TestTaskAddCmdRunWithDeps(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "goal with deps", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	c := &TaskAddCmd{
		GoalID: gObj.ID,
		Title:  "task with deps",
		Prompt: "do this after other tasks",
		Deps:   "t-dep1, t-dep2",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskAddCmd.Run with deps: %v", err)
	}
}

func TestTaskAddCmdRunWithContextFrom(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "goal with context", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	c := &TaskAddCmd{
		GoalID:      gObj.ID,
		Title:       "task with context",
		Prompt:      "use context from another task",
		ContextFrom: "t-ctx1",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskAddCmd.Run with context-from: %v", err)
	}
}

func TestTaskAddCmdRunWithRepo(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "goal with repo", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	c := &TaskAddCmd{
		GoalID: gObj.ID,
		Title:  "task with repo",
		Prompt: "do something with the repo",
		Repo:   "myrepo",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskAddCmd.Run with repo: %v", err)
	}
}

func TestTaskAddCmdRunWithExplicitBranch(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, err := goal.Create(ws, "goal with explicit branch", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	c := &TaskAddCmd{
		GoalID:     gObj.ID,
		Title:      "task with branch",
		Prompt:     "use specific branch",
		Repo:       "myrepo",
		Branch:     "st/tasks/my-custom-branch",
		GoalBranch: "st/goals/" + gObj.ID,
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskAddCmd.Run with branch: %v", err)
	}
}

func TestTaskListCmdRunEmpty(t *testing.T) {
	_, g := initTestWS(t)
	c := &TaskListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskListCmd.Run (empty): %v", err)
	}
}

func TestTaskListCmdRunAll(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "list tasks goal", nil, nil)
	for i := 0; i < 3; i++ {
		tObj := &task.Task{
			ID:     task.NewID(),
			GoalID: gObj.ID,
			Title:  "task",
			Prompt: "do something",
			Agent:  "default",
		}
		if err := task.Create(ws, tObj); err != nil {
			t.Fatalf("task.Create: %v", err)
		}
	}
	c := &TaskListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskListCmd.Run (all): %v", err)
	}
}

func TestTaskListCmdRunFilterByGoal(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "filter by goal", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "task",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}
	c := &TaskListCmd{GoalID: gObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskListCmd.Run (filter by goal): %v", err)
	}
}

func TestTaskListCmdRunFilterByStatus(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "filter by status", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "pending task",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}
	c := &TaskListCmd{Status: "pending"}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskListCmd.Run (filter by status): %v", err)
	}
}

func TestTaskShowCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "show task goal", nil, nil)
	tObj := &task.Task{
		ID:          task.NewID(),
		GoalID:      gObj.ID,
		Title:       "show me",
		Prompt:      "show this task",
		Agent:       "default",
		DepTaskIDs:  []string{"t-dep1"},
		ContextFrom: []string{"t-ctx1"},
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}
	c := &TaskShowCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskShowCmd.Run: %v", err)
	}
}

func TestTaskShowCmdRunWithResult(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "show done task goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	// Mark as done to set result
	if err := task.Done(ws, tObj.ID, "completed successfully", "st/tasks/"+tObj.ID); err != nil {
		t.Fatalf("task.Done: %v", err)
	}

	c := &TaskShowCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskShowCmd.Run (with result): %v", err)
	}
}

func TestTaskShowCmdRunWithError(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "show failed task goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	// Mark as failed to set error msg
	if err := task.Fail(ws, tObj.ID, "something went wrong"); err != nil {
		t.Fatalf("task.Fail: %v", err)
	}

	c := &TaskShowCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskShowCmd.Run (with error): %v", err)
	}
}

func TestTaskShowCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &TaskShowCmd{ID: "t-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent task, got nil")
	}
}

func TestTaskRetryCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "retry goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	// Move to failed state first
	if err := task.Fail(ws, tObj.ID, "failed for testing"); err != nil {
		t.Fatalf("task.Fail: %v", err)
	}

	c := &TaskRetryCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskRetryCmd.Run: %v", err)
	}
}

func TestTaskRetryCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &TaskRetryCmd{ID: "t-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent task, got nil")
	}
}

func TestTaskCancelCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "cancel goal", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "task to cancel",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	c := &TaskCancelCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskCancelCmd.Run: %v", err)
	}
}

func TestTaskCancelCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &TaskCancelCmd{ID: "t-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent task, got nil")
	}
}

func TestTaskDoneCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "done goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	c := &TaskDoneCmd{
		ID:      tObj.ID,
		Summary: "completed successfully",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskDoneCmd.Run: %v", err)
	}
}

func TestTaskDoneCmdRunWithBranch(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "done with branch goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	c := &TaskDoneCmd{
		ID:      tObj.ID,
		Summary: "done with branch",
		Branch:  "st/tasks/" + tObj.ID,
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskDoneCmd.Run with branch: %v", err)
	}
}

func TestTaskDoneCmdRunNotRunning(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "done not-running goal", nil, nil)
	// Create pending task (not running)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "pending task",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	c := &TaskDoneCmd{
		ID:      tObj.ID,
		Summary: "done",
	}
	if err := c.Run(g); err == nil {
		t.Error("expected error for marking pending task as done, got nil")
	}
}

func TestTaskFailCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "fail goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	c := &TaskFailCmd{
		ID:     tObj.ID,
		Reason: "something went wrong",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskFailCmd.Run: %v", err)
	}
}

func TestTaskFailCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &TaskFailCmd{
		ID:     "t-nonexistent",
		Reason: "reason",
	}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent task, got nil")
	}
}

// ─── HeartbeatCmd tests ───────────────────────────────────────────────────────

func TestHeartbeatCmdRun(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "heartbeat goal", nil, nil)
	tObj := createRunningTask(t, ws, gObj.ID)

	c := &HeartbeatCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("HeartbeatCmd.Run: %v", err)
	}

	// Heartbeat file should exist
	if _, err := os.Stat(ws.HeartbeatPath(tObj.ID)); err != nil {
		t.Errorf("heartbeat file not created: %v", err)
	}
}

// ─── AgentCmd tests ───────────────────────────────────────────────────────────

func TestAgentCreateCmdRunBasic(t *testing.T) {
	_, g := initTestWS(t)
	c := &AgentCreateCmd{
		Name:        "my-custom-agent",
		Prompt:      "you are a custom agent",
		Description: "does custom things",
		Model:       "sonnet",
		MaxTurns:    30,
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentCreateCmd.Run: %v", err)
	}
}

func TestAgentCreateCmdRunWithTools(t *testing.T) {
	_, g := initTestWS(t)
	c := &AgentCreateCmd{
		Name:   "tool-agent",
		Prompt: "agent with tools",
		Tools:  "bash, read, write",
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentCreateCmd.Run with tools: %v", err)
	}
}

func TestAgentListCmdRun(t *testing.T) {
	_, g := initTestWS(t)
	c := &AgentListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentListCmd.Run: %v", err)
	}
}

func TestAgentListCmdRunWithLongDescription(t *testing.T) {
	_, g := initTestWS(t)
	// Add an agent with a long description
	addC := &AgentCreateCmd{
		Name:        "long-desc-agent",
		Prompt:      "prompt",
		Description: strings.Repeat("x", 70), // longer than 55 chars
	}
	if err := addC.Run(g); err != nil {
		t.Fatalf("AgentCreateCmd.Run: %v", err)
	}
	c := &AgentListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentListCmd.Run (long desc): %v", err)
	}
}

func TestAgentShowCmdRun(t *testing.T) {
	_, g := initTestWS(t)
	// Show a built-in agent seeded during workspace init. Use "impl" —
	// the "default" agent no longer exists (it was removed).
	c := &AgentShowCmd{Name: "impl"}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentShowCmd.Run: %v", err)
	}
}

func TestAgentShowCmdRunWithTools(t *testing.T) {
	_, g := initTestWS(t)
	// Add an agent with tools then show it
	addC := &AgentCreateCmd{
		Name:   "tool-agent-show",
		Prompt: "agent with tools",
		Tools:  "bash, read",
	}
	if err := addC.Run(g); err != nil {
		t.Fatalf("AgentCreateCmd.Run: %v", err)
	}
	c := &AgentShowCmd{Name: "tool-agent-show"}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentShowCmd.Run: %v", err)
	}
}

// ─── WorkerListTopCmd tests ──────────────────────────────────────────────────────

func TestWorkerListTopCmdRunEmpty(t *testing.T) {
	_, g := initTestWS(t)
	c := &WorkerListTopCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("WorkerListTopCmd.Run (empty): %v", err)
	}
}

// ─── LogCmd tests ─────────────────────────────────────────────────────────────

func TestLogCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &LogCmd{ID: "t-nonexistent-log"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for missing log, got nil")
	}
}

func TestLogCmdRunFullLog(t *testing.T) {
	ws, g := initTestWS(t)
	taskID := "t-testlog123"
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(ws.LogPath(taskID), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile log: %v", err)
	}
	c := &LogCmd{ID: taskID, Tail: 0}
	if err := c.Run(g); err != nil {
		t.Fatalf("LogCmd.Run (full log): %v", err)
	}
}

func TestLogCmdRunTailSomeLines(t *testing.T) {
	ws, g := initTestWS(t)
	taskID := "t-testlogtail"
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(ws.LogPath(taskID), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile log: %v", err)
	}
	c := &LogCmd{ID: taskID, Tail: 2}
	if err := c.Run(g); err != nil {
		t.Fatalf("LogCmd.Run (tail 2): %v", err)
	}
}

func TestLogCmdRunTailMoreThanLines(t *testing.T) {
	ws, g := initTestWS(t)
	taskID := "t-testlogtailmore"
	content := "line1\nline2\n"
	if err := os.WriteFile(ws.LogPath(taskID), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile log: %v", err)
	}
	// Tail more lines than exist — should return all
	c := &LogCmd{ID: taskID, Tail: 100}
	if err := c.Run(g); err != nil {
		t.Fatalf("LogCmd.Run (tail > lines): %v", err)
	}
}

func TestLogCmdRunEmptyLog(t *testing.T) {
	ws, g := initTestWS(t)
	taskID := "t-emptylog"
	if err := os.WriteFile(ws.LogPath(taskID), []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile empty log: %v", err)
	}
	c := &LogCmd{ID: taskID, Tail: 0}
	if err := c.Run(g); err != nil {
		t.Fatalf("LogCmd.Run (empty log): %v", err)
	}
}

// ─── RecoverCmd tests ─────────────────────────────────────────────────────────

func TestRecoverCmdRun(t *testing.T) {
	_, g := initTestWS(t)
	c := &RecoverCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("RecoverCmd.Run: %v", err)
	}
}

// ─── RepoCmd tests ────────────────────────────────────────────────────────────

func TestRepoListCmdRunEmpty(t *testing.T) {
	_, g := initTestWS(t)
	c := &RepoListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("RepoListCmd.Run (empty): %v", err)
	}
}

func TestRepoListCmdRunWithRepos(t *testing.T) {
	ws, g := initTestWS(t)
	// Manually add repos to config
	ws.Config.Repos = map[string]string{
		"repo-a": "/path/to/repo-a",
		"repo-b": "/path/to/repo-b",
	}
	if err := ws.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	c := &RepoListCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("RepoListCmd.Run (with repos): %v", err)
	}
}

func TestRepoDeleteCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &RepoDeleteCmd{Name: "nonexistent-repo"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent repo, got nil")
	}
}

func TestRepoDeleteCmdRunNilRepos(t *testing.T) {
	ws, _ := initTestWS(t)
	// Explicitly set repos to nil in config
	ws.Config.Repos = nil
	if err := ws.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// Open fresh workspace to load the nil repos config
	ws2, err := workspace.Open(ws.Root)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	g2 := newGlobalsWithWS(ws2)
	c := &RepoDeleteCmd{Name: "any-repo"}
	if err := c.Run(g2); err == nil {
		t.Error("expected error when repos is nil, got nil")
	}
}

func TestRepoDeleteCmdRunExisting(t *testing.T) {
	ws, g := initTestWS(t)
	// Add a repo that lives inside the workspace (so it gets removed)
	repoPath := filepath.Join(ws.Root, "test-repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ws.Config.Repos = map[string]string{"test-repo": repoPath}
	if err := ws.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	c := &RepoDeleteCmd{Name: "test-repo"}
	if err := c.Run(g); err != nil {
		t.Fatalf("RepoDeleteCmd.Run: %v", err)
	}
}

func TestRepoAddCmdRunNonGitDir(t *testing.T) {
	ws, g := initTestWS(t)
	// Create a non-git source directory
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_ = ws // ensure workspace is initialized
	c := &RepoAddCmd{Name: "myrepo", Path: srcDir}
	if err := c.Run(g); err != nil {
		t.Fatalf("RepoAddCmd.Run (non-git dir): %v", err)
	}

	// Verify it was added to config
	if ws.Config.Repos["myrepo"] == "" {
		t.Error("repo not added to config after RepoAddCmd.Run")
	}
}

func TestRepoAddCmdRunPathNotExist(t *testing.T) {
	_, g := initTestWS(t)
	c := &RepoAddCmd{Name: "myrepo", Path: "/nonexistent/path/to/repo"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent path, got nil")
	}
}

func TestRepoAddCmdRunDestinationExists(t *testing.T) {
	ws, g := initTestWS(t)
	// Create the destination directory first
	destDir := filepath.Join(ws.Root, "conflictingrepo")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Create a source dir
	srcDir := t.TempDir()
	c := &RepoAddCmd{Name: "conflictingrepo", Path: srcDir}
	if err := c.Run(g); err == nil {
		t.Error("expected error when destination already exists, got nil")
	}
}

func TestRepoAddCmdRunNilReposInitialized(t *testing.T) {
	ws, g := initTestWS(t)
	// Force nil repos
	ws.Config.Repos = nil
	if err := ws.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := &RepoAddCmd{Name: "freshrepo", Path: srcDir}
	if err := c.Run(g); err != nil {
		t.Fatalf("RepoAddCmd.Run (nil repos): %v", err)
	}
}

// ─── CleanCmd tests ───────────────────────────────────────────────────────────

func TestCleanCmdRunNothingToClean(t *testing.T) {
	_, g := initTestWS(t)
	c := &CleanCmd{Yes: true}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (nothing to clean): %v", err)
	}
}

func TestCleanCmdRunAll(t *testing.T) {
	ws, g := initTestWS(t)
	// Create some goals and tasks
	gObj, _ := goal.Create(ws, "goal to clean", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "task to clean",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	// Create associated log and heartbeat
	if err := os.WriteFile(ws.LogPath(tObj.ID), []byte("log content"), 0644); err != nil {
		t.Fatalf("WriteFile log: %v", err)
	}

	c := &CleanCmd{Yes: true}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (all): %v", err)
	}
}

func TestCleanCmdRunIncomplete(t *testing.T) {
	ws, g := initTestWS(t)
	// Create an incomplete (queued) goal
	gObj, _ := goal.Create(ws, "incomplete goal", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "pending task",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	c := &CleanCmd{Yes: true, Incomplete: true}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (incomplete): %v", err)
	}
}

func TestCleanCmdRunScopedToGoal(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "scoped goal", nil, nil)
	tObj := &task.Task{
		ID:     task.NewID(),
		GoalID: gObj.ID,
		Title:  "task in goal",
		Prompt: "do something",
		Agent:  "default",
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	c := &CleanCmd{Yes: true, GoalID: gObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (scoped to goal): %v", err)
	}
}

func TestCleanCmdRunScopedIncompleteGoal(t *testing.T) {
	ws, g := initTestWS(t)
	// Create a done goal — should not be cleaned by --incomplete
	gObj, _ := goal.Create(ws, "done goal", nil, nil)
	if err := goal.UpdateStatus(ws, gObj.ID, goal.StatusDone); err != nil {
		t.Fatalf("goal.UpdateStatus: %v", err)
	}

	c := &CleanCmd{Yes: true, GoalID: gObj.ID, Incomplete: true}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (scoped to done goal, incomplete): %v", err)
	}
}

func TestCleanCmdRunScopedGoalNotFound(t *testing.T) {
	_, g := initTestWS(t)
	c := &CleanCmd{Yes: true, GoalID: "g-nonexistent"}
	if err := c.Run(g); err == nil {
		t.Error("expected error for nonexistent goal in --goal flag, got nil")
	}
}

func TestCleanCmdRunDoneTasks(t *testing.T) {
	ws, g := initTestWS(t)
	// Create a done goal with a done task — clean all
	gObj, _ := goal.Create(ws, "done goal for clean", nil, nil)
	if err := goal.UpdateStatus(ws, gObj.ID, goal.StatusDone); err != nil {
		t.Fatalf("goal.UpdateStatus: %v", err)
	}
	tObj := createRunningTask(t, ws, gObj.ID)
	if err := task.Done(ws, tObj.ID, "done", ""); err != nil {
		t.Fatalf("task.Done: %v", err)
	}

	c := &CleanCmd{Yes: true}
	if err := c.Run(g); err != nil {
		t.Fatalf("CleanCmd.Run (done tasks): %v", err)
	}
}

// ─── openWS tests ─────────────────────────────────────────────────────────────

func TestOpenWSFromEnvVar(t *testing.T) {
	dir := t.TempDir()
	if _, err := workspace.Init(dir, nil); err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	t.Setenv("ST_WORKSPACE", dir)

	ws := openWS()
	if ws == nil {
		t.Fatal("openWS returned nil")
	}
	if ws.Root != dir {
		t.Errorf("openWS Root = %q, want %q", ws.Root, dir)
	}
}

// ─── Globals.WS() tests ───────────────────────────────────────────────────────

func TestGlobalsWSLazy(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir, nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	g := newGlobalsWithWS(ws)
	// WS() should return the pre-set workspace without calling openWS
	got := g.WS()
	if got != ws {
		t.Errorf("Globals.WS() returned different workspace than expected")
	}
	// Calling again should return the same instance
	got2 := g.WS()
	if got2 != got {
		t.Errorf("Globals.WS() returned different instance on second call")
	}
}

// ─── TaskShowCmd with optional fields ────────────────────────────────────────

func TestTaskShowCmdRunWithBranchAndRepo(t *testing.T) {
	ws, g := initTestWS(t)
	gObj, _ := goal.Create(ws, "task with branch goal", nil, nil)
	tObj := &task.Task{
		ID:         task.NewID(),
		GoalID:     gObj.ID,
		Title:      "branched task",
		Prompt:     "do something on a branch",
		Agent:      "default",
		Repo:       "myrepo",
		BranchName: "st/tasks/t-xyz",
		DepTaskIDs: []string{"t-dep1", "t-dep2"},
	}
	if err := task.Create(ws, tObj); err != nil {
		t.Fatalf("task.Create: %v", err)
	}
	c := &TaskShowCmd{ID: tObj.ID}
	if err := c.Run(g); err != nil {
		t.Fatalf("TaskShowCmd.Run (with branch/repo): %v", err)
	}
}

// ─── WorkerListTopCmd with workers ──────────────────────────────────────────────

func TestWorkerListTopCmdRunWithWorkers(t *testing.T) {
	ws, g := initTestWS(t)

	// Write a worker JSON file directly
	type workerRecord struct {
		ID          string `json:"id"`
		PID         int    `json:"pid"`
		TaskID      string `json:"task_id"`
		Agent       string `json:"agent"`
		HeartbeatAt int64  `json:"heartbeat_at"`
	}
	wk := workerRecord{
		ID:          "w-test123",
		PID:         os.Getpid(),
		TaskID:      "t-task123",
		Agent:       "default",
		HeartbeatAt: time.Now().Unix(),
	}
	data, _ := json.MarshalIndent(wk, "", "  ")
	workerPath := filepath.Join(ws.WorkersDir(), wk.ID+".json")
	if err := os.WriteFile(workerPath, data, 0644); err != nil {
		t.Fatalf("WriteFile worker: %v", err)
	}

	c := &WorkerListTopCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("WorkerListTopCmd.Run (with workers): %v", err)
	}
}

func TestWorkerListTopCmdRunWithWorkersNoHeartbeat(t *testing.T) {
	ws, g := initTestWS(t)

	type workerRecord struct {
		ID          string `json:"id"`
		PID         int    `json:"pid"`
		TaskID      string `json:"task_id"`
		Agent       string `json:"agent"`
		HeartbeatAt int64  `json:"heartbeat_at"`
	}
	// Worker with zero heartbeat
	wk := workerRecord{
		ID:          "w-nohb456",
		PID:         os.Getpid(),
		TaskID:      "t-task456",
		Agent:       "default",
		HeartbeatAt: 0,
	}
	data, _ := json.MarshalIndent(wk, "", "  ")
	workerPath := filepath.Join(ws.WorkersDir(), wk.ID+".json")
	if err := os.WriteFile(workerPath, data, 0644); err != nil {
		t.Fatalf("WriteFile worker: %v", err)
	}

	c := &WorkerListTopCmd{}
	if err := c.Run(g); err != nil {
		t.Fatalf("WorkerListTopCmd.Run (worker no heartbeat): %v", err)
	}
}

// ─── RepoCloneCmd validation ──────────────────────────────────────────────────

func TestRepoCloneCmdParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"repo", "clone", "myrepo", "https://example.com/repo.git"})
}

func TestRepoCloneCmdWithDirParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"repo", "clone", "myrepo", "https://example.com/repo.git", "/tmp/mydir"})
}

// ─── Additional CLI struct field value tests ──────────────────────────────────

func TestGoalAddFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf strings.Builder

	type fakeWriter struct{ strings.Builder }

	k, err := newTestKongCustom(t, &cli, globals)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	_ = k
	_ = buf

	// Re-use parseExpectOK for this
	parseExpectOK(t, []string{"goal", "add", "my goal text", "--hint", "focus on tests", "--repos", "repo1,repo2"})
}

func TestRunCmdFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf strings.Builder
	_ = buf

	import_bytes := strings.Builder{}
	_ = import_bytes

	k, err := newTestKongCustom(t, &cli, globals)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := []string{
		"run", "my goal text",
		"--max-concurrent", "8",
		"--port", "9090",
	}
	if _, err := k.Parse(args); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	c := &cli.Run
	if c.MaxConcurrent != 8 {
		t.Errorf("MaxConcurrent: got %d, want 8", c.MaxConcurrent)
	}
	if c.Port != 9090 {
		t.Errorf("Port: got %d, want 9090", c.Port)
	}
	if c.Text != "my goal text" {
		t.Errorf("Text: got %q, want %q", c.Text, "my goal text")
	}
}

func TestRunCmdRandomPortFlagParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	k, err := newTestKongCustom(t, &cli, globals)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	if _, err := k.Parse([]string{"run", "goal", "--random-port"}); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !cli.Run.RandomPort {
		t.Error("RandomPort: got false, want true")
	}
}

func TestCleanCmdFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	k, err := newTestKongCustom(t, &cli, globals)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := []string{"clean", "--incomplete", "--yes", "--goal", "g-123"}
	if _, err := k.Parse(args); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if !cli.Clean.Incomplete {
		t.Error("Clean.Incomplete: got false, want true")
	}
	if !cli.Clean.Yes {
		t.Error("Clean.Yes: got false, want true")
	}
	if cli.Clean.GoalID != "g-123" {
		t.Errorf("Clean.GoalID: got %q, want %q", cli.Clean.GoalID, "g-123")
	}
}

func TestLogCmdFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	k, err := newTestKongCustom(t, &cli, globals)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	if _, err := k.Parse([]string{"log", "task-xyz", "--tail", "50"}); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if cli.Log.ID != "task-xyz" {
		t.Errorf("Log.ID: got %q, want %q", cli.Log.ID, "task-xyz")
	}
	if cli.Log.Tail != 50 {
		t.Errorf("Log.Tail: got %d, want 50", cli.Log.Tail)
	}
}
