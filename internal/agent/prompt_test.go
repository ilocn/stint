package agent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ilocn/stint/internal/agent"
	"github.com/ilocn/stint/internal/gitutil"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/workspace"
)

func TestBuildPromptContainsTaskInstructions(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:     "t-abc123",
		GoalID: "g-001",
		Title:  "implement feature",
		Agent:  "impl",
		Prompt: "Write a hello world function",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	checks := []struct {
		name    string
		contain string
	}{
		{"system prompt", def.SystemPrompt},
		{"task prompt", "Write a hello world function"},
		{"heartbeat instruction", "st heartbeat t-abc123"},
		{"done instruction", "st task-done t-abc123"},
		{"fail instruction", "st task-fail t-abc123"},
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c.contain) {
			t.Errorf("prompt missing %s: expected to contain %q", c.name, c.contain)
		}
	}
}

func TestBuildPromptIncludesBranchInfo(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:         "t-branchtest",
		GoalID:     "g-002",
		Title:      "branch task",
		Agent:      "impl",
		Prompt:     "do the work",
		BranchName: "st/tasks/t-branchtest",
		GoalBranch: "st/goals/g-002",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "st/tasks/t-branchtest") {
		t.Error("prompt should contain task branch name")
	}
	if !strings.Contains(prompt, "st/goals/g-002") {
		t.Error("prompt should contain goal branch name")
	}
}

func TestBuildPromptNoContextFromEmpty(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:     "t-nocontext",
		GoalID: "g-003",
		Title:  "standalone",
		Agent:  "impl",
		Prompt: "standalone task",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if strings.Contains(prompt, "CONTEXT FROM PREVIOUS TASKS") {
		t.Error("prompt should not include context section when ContextFrom is empty")
	}
}

func TestBuildPlannerPromptContainsGoal(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	goalID := "g-planner-01"
	goalText := "Build a REST API"
	hints := []string{"use Go", "use PostgreSQL"}

	prompt, err := agent.BuildPlannerPrompt(ws, goalID, goalText, hints)
	if err != nil {
		t.Fatalf("BuildPlannerPrompt: %v", err)
	}

	checks := []struct {
		name    string
		contain string
	}{
		{"goal ID", goalID},
		{"goal text", goalText},
		{"hint 1", "use Go"},
		{"hint 2", "use PostgreSQL"},
		{"task add command", "st task add"},
		{"task list command", "st task list"},
		{"status command", "st status"},
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c.contain) {
			t.Errorf("planner prompt missing %s: expected to contain %q", c.name, c.contain)
		}
	}
}

func TestBuildPlannerPromptNoHints(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	prompt, err := agent.BuildPlannerPrompt(ws, "g-nohints", "simple goal", nil)
	if err != nil {
		t.Fatalf("BuildPlannerPrompt: %v", err)
	}

	if strings.Contains(prompt, "## Hints") {
		t.Error("prompt should not include hints section when hints is nil")
	}
}

func TestBuildPlannerPromptIncludesRepos(t *testing.T) {
	t.Parallel()
	// Workspace with repos configured.
	ws, err := newWSWithRepos(t, map[string]string{"api": "/tmp/api", "web": "/tmp/web"})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	prompt, err := agent.BuildPlannerPrompt(ws, "g-repos", "multi-repo goal", nil)
	if err != nil {
		t.Fatalf("BuildPlannerPrompt: %v", err)
	}

	if !strings.Contains(prompt, "api") {
		t.Error("planner prompt should list repo 'api'")
	}
	if !strings.Contains(prompt, "web") {
		t.Error("planner prompt should list repo 'web'")
	}
}

func TestBuildPromptWithRepoIncludesCommitRequirement(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:     "t-repotask",
		GoalID: "g-010",
		Title:  "task with repo",
		Agent:  "impl",
		Prompt: "do some work in the repo",
		Repo:   "myrepo",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "git add -A && git commit") {
		t.Error("prompt should contain git commit instruction when Repo is set")
	}
}

func TestBuildPromptWithoutRepoOmitsCommitRequirement(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:     "t-norepo",
		GoalID: "g-011",
		Title:  "task without repo",
		Agent:  "impl",
		Prompt: "do some work without a repo",
		Repo:   "",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}

	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if strings.Contains(prompt, "git add -A && git commit") {
		t.Error("prompt should NOT contain git commit instruction when Repo is empty")
	}
}

func TestBuildPlannerPromptInstructsParallelExploration(t *testing.T) {
	t.Parallel()
	ws, err := newWSWithRepos(t, map[string]string{"api": "/tmp/api", "web": "/tmp/web"})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	prompt, err := agent.BuildPlannerPrompt(ws, "g-explore", "add authentication", nil)
	if err != nil {
		t.Fatalf("BuildPlannerPrompt: %v", err)
	}

	checks := []struct {
		name    string
		contain string
	}{
		{"Explore subagent type", "Explore"},
		{"Phase 1 header", "Phase 1"},
		{"parallel instruction", "PARALLEL"},
		{"repo path api", "/tmp/api"},
		{"repo path web", "/tmp/web"},
		{"wait instruction", "Wait for ALL"},
		{"Phase 2 header", "Phase 2"},
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c.contain) {
			t.Errorf("planner prompt missing %s: expected to contain %q", c.name, c.contain)
		}
	}
}

// newWSWithRepos creates a workspace with repos for testing.
func newWSWithRepos(t *testing.T, repos map[string]string) (*workspace.Workspace, error) {
	t.Helper()
	return workspace.Init(t.TempDir(), repos)
}

// runGitInDir runs a git command in dir, failing the test on error.
func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestBuildPromptWithContextFromNonEmptyDiff verifies that BuildPrompt writes
// the "Git diff:" section when the dep branch has diverged from the goal branch.
func TestBuildPromptWithContextFromNonEmptyDiff(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	// Create a worktree on feature-branch and commit a file so the diff is non-empty.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := gitutil.WorktreeAdd(repoDir, wt, "feature-nondiff", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInDir(t, wt, "add", "feature.go")
	runGitInDir(t, wt, "commit", "-m", "add feature")

	ws, err := newWSWithRepos(t, map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	depTask := &task.Task{
		ID:         "t-dep-nondiff",
		GoalID:     "g-001",
		GoalBranch: "main",
		Title:      "dep with changes",
		Agent:      "default",
		Prompt:     "do stuff",
		Result: &task.Result{
			Summary: "added feature file",
			Branch:  "feature-nondiff",
		},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	mainTask := &task.Task{
		ID:          "t-main-nondiff",
		GoalID:      "g-001",
		Title:       "main with diff context",
		Agent:       "default",
		Prompt:      "use context with diff",
		Repo:        "myrepo",
		ContextFrom: []string{"t-dep-nondiff"},
	}
	if err := task.Create(ws, mainTask); err != nil {
		t.Fatalf("task.Create main: %v", err)
	}

	def, _ := agent.Get(ws, "default")
	prompt, err := agent.BuildPrompt(ws, mainTask, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "Git diff:") {
		t.Error("prompt should include 'Git diff:' section when dep branch has diverged")
	}
	if !strings.Contains(prompt, "feature.go") {
		t.Error("prompt should show the changed file in the git diff")
	}
}

// TestBuildPromptWithLargeDiff verifies that BuildPrompt truncates diffs
// larger than 8000 characters with a "(truncated)" marker.
func TestBuildPromptWithLargeDiff(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	// Create feature branch with a large file to produce a diff > 8000 chars.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := gitutil.WorktreeAdd(repoDir, wt, "large-diff-branch", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	// 9000+ chars of content ensures the diff exceeds 8000 bytes.
	largeContent := strings.Repeat("x", 9000) + "\n"
	if err := os.WriteFile(filepath.Join(wt, "large.txt"), []byte(largeContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInDir(t, wt, "add", "large.txt")
	runGitInDir(t, wt, "commit", "-m", "add large file")

	ws, err := newWSWithRepos(t, map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	depTask := &task.Task{
		ID:         "t-dep-large",
		GoalID:     "g-001",
		GoalBranch: "main",
		Title:      "dep large diff",
		Agent:      "default",
		Prompt:     "large change",
		Result: &task.Result{
			Summary: "added large file",
			Branch:  "large-diff-branch",
		},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	mainTask := &task.Task{
		ID:          "t-main-large",
		GoalID:      "g-001",
		Title:       "main with large diff",
		Agent:       "default",
		Prompt:      "use large diff context",
		Repo:        "myrepo",
		ContextFrom: []string{"t-dep-large"},
	}
	if err := task.Create(ws, mainTask); err != nil {
		t.Fatalf("task.Create main: %v", err)
	}

	def, _ := agent.Get(ws, "default")
	prompt, err := agent.BuildPrompt(ws, mainTask, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "(truncated)") {
		t.Error("prompt should truncate large diffs with '(truncated)' marker")
	}
}

// TestBuildPromptWithContextFrom verifies that BuildPrompt includes the
// CONTEXT section when ContextFrom references a valid task with a Result.
func TestBuildPromptWithContextFrom(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create dependency task with a result (status: pending is fine for Get).
	depTask := &task.Task{
		ID:     "t-dep-ctx",
		GoalID: "g-001",
		Title:  "dep task",
		Agent:  "default",
		Prompt: "do stuff",
		Result: &task.Result{Summary: "finished doing stuff"},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	// Create main task with ContextFrom referencing the dep.
	tk := &task.Task{
		ID:          "t-main-ctx",
		GoalID:      "g-001",
		Title:       "main task",
		Agent:       "default",
		Prompt:      "use the context",
		ContextFrom: []string{"t-dep-ctx"},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create main: %v", err)
	}

	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get agent: %v", err)
	}
	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "CONTEXT FROM PREVIOUS TASKS") {
		t.Error("prompt should include context section when ContextFrom is non-empty")
	}
	if !strings.Contains(prompt, "t-dep-ctx") {
		t.Error("prompt should include the dependency task ID")
	}
	if !strings.Contains(prompt, "finished doing stuff") {
		t.Error("prompt should include the dependency task summary")
	}
}

// TestBuildPromptWithContextFromInvalidDep verifies that BuildPrompt continues
// gracefully when a ContextFrom dep task ID does not exist.
func TestBuildPromptWithContextFromInvalidDep(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := &task.Task{
		ID:          "t-ctx-missing",
		GoalID:      "g-001",
		Title:       "main",
		Agent:       "default",
		Prompt:      "do work",
		ContextFrom: []string{"t-nonexistent-dep"},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	def, _ := agent.Get(ws, "default")
	prompt, err := agent.BuildPrompt(ws, tk, def)
	if err != nil {
		t.Fatalf("BuildPrompt should not fail for missing dep: %v", err)
	}

	// The context section is still emitted (headers + END CONTEXT) even if the
	// dep was not found (best-effort: the missing dep is silently skipped).
	if !strings.Contains(prompt, "CONTEXT FROM PREVIOUS TASKS") {
		t.Error("prompt should include context section header even when deps are missing")
	}
}

// TestBuildPromptWithContextFromAndDiff verifies that BuildPrompt invokes the
// diff path when a dep task has Result.Branch and GoalBranch set and a real
// repo is registered in the workspace.
func TestBuildPromptWithContextFromAndDiff(t *testing.T) {
	t.Parallel()

	// Create a real git repo with two branches so gitutil.Diff doesn't error.
	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}
	if err := gitutil.CreateBranch(repoDir, "feature-branch", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	ws, err := newWSWithRepos(t, map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	// Dep task has a Result with Branch and GoalBranch so the diff code runs.
	depTask := &task.Task{
		ID:         "t-dep-diff",
		GoalID:     "g-001",
		GoalBranch: "main",
		Title:      "dep with branch",
		Agent:      "default",
		Prompt:     "do stuff",
		Result: &task.Result{
			Summary: "branch work done",
			Branch:  "feature-branch",
		},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	// Main task has Repo set so the diff lookup fires.
	mainTask := &task.Task{
		ID:          "t-main-diff",
		GoalID:      "g-001",
		Title:       "main with diff context",
		Agent:       "default",
		Prompt:      "use context with diff",
		Repo:        "myrepo",
		ContextFrom: []string{"t-dep-diff"},
	}
	if err := task.Create(ws, mainTask); err != nil {
		t.Fatalf("task.Create main: %v", err)
	}

	def, _ := agent.Get(ws, "default")
	prompt, err := agent.BuildPrompt(ws, mainTask, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	if !strings.Contains(prompt, "CONTEXT FROM PREVIOUS TASKS") {
		t.Error("prompt should include context section")
	}
	if !strings.Contains(prompt, "t-dep-diff") {
		t.Error("prompt should include dep task ID")
	}
}

// TestBuildPromptUsesPrecomputedDiff verifies that BuildPrompt reads Result.Diff
// instead of calling git when the pre-computed diff is available.
func TestBuildPromptUsesPrecomputedDiff(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Dep task with a pre-computed diff (no real git repo needed).
	depTask := &task.Task{
		ID:         "t-dep-precomputed",
		GoalID:     "g-001",
		GoalBranch: "main",
		Title:      "dep with precomputed diff",
		Agent:      "default",
		Prompt:     "do stuff",
		Result: &task.Result{
			Summary: "made changes",
			Branch:  "feature-branch",
			Diff:    "diff --git a/precomputed.go b/precomputed.go\n+added precomputed line\n",
		},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	mainTask := &task.Task{
		ID:          "t-main-precomputed",
		GoalID:      "g-001",
		Title:       "main using precomputed diff",
		Agent:       "default",
		Prompt:      "use precomputed context",
		Repo:        "myrepo",
		ContextFrom: []string{"t-dep-precomputed"},
	}
	// Register "myrepo" — no actual repo needed since we use the pre-computed diff.
	wsWithRepo, err := workspace.Init(t.TempDir(), map[string]string{"myrepo": "/nonexistent-repo"})
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	// Create tasks in the repo workspace.
	depTask2 := *depTask
	depTask2.ID = "t-dep-precomputed2"
	if err := task.Create(wsWithRepo, &depTask2); err != nil {
		t.Fatalf("task.Create dep2: %v", err)
	}
	mainTask2 := *mainTask
	mainTask2.ID = "t-main-precomputed2"
	mainTask2.ContextFrom = []string{"t-dep-precomputed2"}
	if err := task.Create(wsWithRepo, &mainTask2); err != nil {
		t.Fatalf("task.Create main2: %v", err)
	}

	def, _ := agent.Get(wsWithRepo, "default")
	prompt, err := agent.BuildPrompt(wsWithRepo, &mainTask2, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	// The pre-computed diff should appear in the prompt.
	if !strings.Contains(prompt, "precomputed.go") {
		t.Error("prompt should include the pre-computed diff content")
	}
	if !strings.Contains(prompt, "Git diff:") {
		t.Error("prompt should include 'Git diff:' section when pre-computed diff is available")
	}
}

// TestBuildPromptFallsBackToOnDemandDiffWhenResultDiffEmpty verifies that
// BuildPrompt falls back to computing the git diff on-demand when Result.Diff
// is empty (backwards compatibility with tasks created before this feature).
func TestBuildPromptFallsBackToOnDemandDiffWhenResultDiffEmpty(t *testing.T) {
	t.Parallel()

	// Set up a real git repo so the on-demand diff path can succeed.
	repoDir := t.TempDir()
	if err := gitutil.InitWithBranch(repoDir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(repoDir, "initial"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	// Create a feature branch with a committed file.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := gitutil.WorktreeAdd(repoDir, wt, "legacy-branch", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "legacy.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGitInDir(t, wt, "add", "legacy.go")
	runGitInDir(t, wt, "commit", "-m", "add legacy.go")

	ws, err := newWSWithRepos(t, map[string]string{"myrepo": repoDir})
	if err != nil {
		t.Fatalf("newWSWithRepos: %v", err)
	}

	// Dep task with NO pre-computed diff (Result.Diff is empty — legacy format).
	depTask := &task.Task{
		ID:         "t-dep-legacy",
		GoalID:     "g-001",
		GoalBranch: "main",
		Title:      "legacy dep",
		Agent:      "default",
		Prompt:     "do stuff",
		Result: &task.Result{
			Summary: "legacy changes",
			Branch:  "legacy-branch",
			Diff:    "", // empty — triggers fallback
		},
	}
	if err := task.Create(ws, depTask); err != nil {
		t.Fatalf("task.Create dep: %v", err)
	}

	mainTask := &task.Task{
		ID:          "t-main-legacy",
		GoalID:      "g-001",
		Title:       "main with legacy dep",
		Agent:       "default",
		Prompt:      "use legacy context",
		Repo:        "myrepo",
		ContextFrom: []string{"t-dep-legacy"},
	}
	if err := task.Create(ws, mainTask); err != nil {
		t.Fatalf("task.Create main: %v", err)
	}

	def, _ := agent.Get(ws, "default")
	prompt, err := agent.BuildPrompt(ws, mainTask, def)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	// On-demand diff fallback should find the committed file.
	if !strings.Contains(prompt, "legacy.go") {
		t.Error("prompt should include 'legacy.go' via on-demand diff fallback")
	}
}

// TestBuildRemediationPlannerPromptContainsContext verifies that
// BuildRemediationPlannerPrompt includes the remediation context.
func TestBuildRemediationPlannerPromptContainsContext(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	goalID := "g-remediation-01"
	remediationContext := "Review task t-review-01 (review) failed.\nIssues: missing tests\n"

	prompt, err := agent.BuildRemediationPlannerPrompt(ws, goalID, remediationContext)
	if err != nil {
		t.Fatalf("BuildRemediationPlannerPrompt: %v", err)
	}

	checks := []struct {
		name    string
		contain string
	}{
		{"goal ID in tools", goalID},
		{"remediation context", "Review task t-review-01"},
		{"issues", "missing tests"},
		{"fix cycle instruction", "fix-impl → merge-task → re-review → merge-task"},
		{"exit instruction", "Exit IMMEDIATELY"},
		{"task add command", "st task add"},
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c.contain) {
			t.Errorf("remediation planner prompt missing %s: expected to contain %q", c.name, c.contain)
		}
	}
}

// TestBuildRemediationPlannerPromptIncludesAgents verifies that
// BuildRemediationPlannerPrompt lists available agents.
func TestBuildRemediationPlannerPromptIncludesAgents(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	prompt, err := agent.BuildRemediationPlannerPrompt(ws, "g-rem", "context here")
	if err != nil {
		t.Fatalf("BuildRemediationPlannerPrompt: %v", err)
	}

	if !strings.Contains(prompt, "impl") {
		t.Error("remediation planner prompt should list available agents including 'impl'")
	}
}
