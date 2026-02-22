package gitutil_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/stint/internal/gitutil"
)

// initRepo creates a minimal git repo in a temp dir with an initial commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := gitutil.InitWithBranch(dir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(dir, "initial commit"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}
	return dir
}

func TestBranchExists(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	if !gitutil.BranchExists(repo, "main") {
		t.Error("main branch should exist")
	}
	if gitutil.BranchExists(repo, "nonexistent") {
		t.Error("nonexistent branch should not exist")
	}
}

func TestCreateBranch(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	if err := gitutil.CreateBranch(repo, "feature/foo", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if !gitutil.BranchExists(repo, "feature/foo") {
		t.Error("feature/foo branch should exist after creation")
	}
}

func TestCurrentBranch(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	branch, err := gitutil.CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("CurrentBranch = %q, want %q", branch, "main")
	}
}

func TestDefaultBranchDetectsMain(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	branch := gitutil.DefaultBranch(repo)
	if branch != "main" {
		t.Errorf("DefaultBranch = %q, want %q", branch, "main")
	}
}

func TestDefaultBranchDetectsMaster(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := gitutil.InitWithBranch(dir, "master"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(dir, "initial commit"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	branch := gitutil.DefaultBranch(dir)
	if branch != "master" {
		t.Errorf("DefaultBranch = %q, want %q", branch, "master")
	}
}

func TestWorktreeAdd(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "wt")

	if err := gitutil.WorktreeAdd(repo, worktreePath, "st/tasks/t-test", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	// Branch should now exist in the repo.
	if !gitutil.BranchExists(repo, "st/tasks/t-test") {
		t.Error("task branch should exist after WorktreeAdd")
	}
}

func TestWorktreeAddExistingBranch(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	// Create branch first.
	if err := gitutil.CreateBranch(repo, "existing-branch", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// WorktreeAdd to an existing branch should not error.
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := gitutil.WorktreeAdd(repo, worktreePath, "existing-branch", "main"); err != nil {
		t.Fatalf("WorktreeAdd existing branch: %v", err)
	}
}

func TestWorktreePrune(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	// prune on a repo with no stale worktrees should succeed.
	if err := gitutil.WorktreePrune(repo); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}
}

func TestMergeTreeClean(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	// A branch with no divergent commits should merge cleanly.
	if err := gitutil.CreateBranch(repo, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	clean, err := gitutil.MergeTreeClean(repo, "main", "feature")
	if err != nil {
		t.Fatalf("MergeTreeClean: %v", err)
	}
	if !clean {
		t.Error("merge should be clean when branches are identical")
	}
}

// runInDir runs a git command in dir for test setup purposes.
func runInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Use the repo's already-configured user identity; no env override needed.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// writeFileInDir writes content to filename inside dir.
func writeFileInDir(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", filename, err)
	}
}

// TestInit verifies that Init creates a new git repository (.git directory).
func TestInit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := gitutil.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("Init should create a .git directory: %v", err)
	}
}

// TestDiff verifies that Diff returns an empty string when two branches have
// the same tree.
func TestDiff(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	if err := gitutil.CreateBranch(repo, "feature-diff", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	diff, err := gitutil.Diff(repo, "feature-diff", "main")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// Identical branches produce an empty diff.
	if diff != "" {
		t.Errorf("Diff of identical branches should be empty, got %q", diff)
	}
}

// TestDiffWithChanges verifies that Diff detects file changes made on a branch.
func TestDiffWithChanges(t *testing.T) {
	t.Parallel()
	repo := initRepo(t) // has main with one empty commit

	// Create a worktree on a new branch and commit a file there.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := gitutil.WorktreeAdd(repo, wt, "feature-changes", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	writeFileInDir(t, wt, "new_feature.txt", "hello from feature")
	runInDir(t, wt, "add", "new_feature.txt")
	runInDir(t, wt, "commit", "-m", "add feature file")

	diff, err := gitutil.Diff(repo, "feature-changes", "main")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "new_feature.txt") {
		t.Errorf("Diff should mention new_feature.txt, got: %q", diff)
	}
}

// TestWorktreeRemove verifies that a worktree can be removed cleanly.
func TestWorktreeRemove(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt-remove")

	if err := gitutil.WorktreeAdd(repo, wt, "remove-test-branch", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	if err := gitutil.WorktreeRemove(repo, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	// The worktree directory should no longer exist.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree directory should have been removed by WorktreeRemove")
	}
}

// TestWorktreeRemoveError verifies that WorktreeRemove returns an error when
// given a path that was never registered as a worktree.
func TestWorktreeRemoveError(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	err := gitutil.WorktreeRemove(repo, "/nonexistent/not-a-worktree")
	if err == nil {
		t.Error("WorktreeRemove should fail for a path that is not a registered worktree")
	}
}

// TestCommitEmptyInNonGitDir verifies that CommitEmpty returns an error when
// called in a directory that is not a git repository.
func TestCommitEmptyInNonGitDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // plain directory, not a git repo
	err := gitutil.CommitEmpty(dir, "should fail")
	if err == nil {
		t.Fatal("CommitEmpty should fail in a non-git directory")
	}
}

// TestDefaultBranchFallbackToMain verifies that DefaultBranch returns "main"
// when neither "main" nor "master" exist in the repo.
func TestDefaultBranchFallbackToMain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Init with "develop" — neither "main" nor "master" will exist.
	if err := gitutil.InitWithBranch(dir, "develop"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}
	if err := gitutil.CommitEmpty(dir, "initial commit"); err != nil {
		t.Fatalf("CommitEmpty: %v", err)
	}

	branch := gitutil.DefaultBranch(dir)
	if branch != "main" {
		t.Errorf("DefaultBranch = %q, want \"main\" (fallback when no common branch found)", branch)
	}
}

// TestDefaultBranchFromRemoteHead verifies that DefaultBranch reads the remote
// HEAD ref when the repo was cloned (origin/HEAD is set).
func TestDefaultBranchFromRemoteHead(t *testing.T) {
	t.Parallel()

	// Create a "remote" bare-ish repo.
	remote := initRepo(t)

	// Clone it so the local repo has origin/HEAD configured.
	local := filepath.Join(t.TempDir(), "cloned")
	cmd := exec.Command("git", "clone", remote, local)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	branch := gitutil.DefaultBranch(local)
	if branch != "main" {
		t.Errorf("DefaultBranch = %q, want \"main\" (from remote HEAD)", branch)
	}
}

// TestMergeTreeCleanWithConflict verifies that MergeTreeClean returns false
// when two branches have conflicting changes to the same file.
func TestMergeTreeCleanWithConflict(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	// Add a file on main.
	writeFileInDir(t, repo, "conflict.txt", "original")
	runInDir(t, repo, "add", "conflict.txt")
	runInDir(t, repo, "commit", "-m", "add conflict.txt")

	// Create feature branch at current main tip.
	if err := gitutil.CreateBranch(repo, "conflict-feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	// Advance main with a different change to the same file.
	writeFileInDir(t, repo, "conflict.txt", "main version — changed independently")
	runInDir(t, repo, "add", "conflict.txt")
	runInDir(t, repo, "commit", "-m", "main: update conflict.txt")

	// Apply a different change to conflict.txt on the feature branch via worktree.
	wt := filepath.Join(t.TempDir(), "conflict-wt")
	if err := gitutil.WorktreeAdd(repo, wt, "conflict-feature", "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	writeFileInDir(t, wt, "conflict.txt", "feature version — different change")
	runInDir(t, wt, "add", "conflict.txt")
	runInDir(t, wt, "commit", "-m", "feature: update conflict.txt")

	clean, err := gitutil.MergeTreeClean(repo, "main", "conflict-feature")
	if err != nil {
		t.Fatalf("MergeTreeClean: %v", err)
	}
	if clean {
		t.Error("MergeTreeClean should return false when branches conflict")
	}
}

// TestWorktreeAddFailsOnBadBase verifies that WorktreeAdd returns an error when
// the base branch does not exist in the repo, covering the error-return path for
// the "new branch" code path (git worktree add -b branch wt nonexistent-base).
func TestWorktreeAddFailsOnBadBase(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt-bad-base")

	// "nonexistent-base" does not exist, so creating a branch from it must fail.
	err := gitutil.WorktreeAdd(repo, wt, "new-branch-from-bad-base", "nonexistent-base")
	if err == nil {
		t.Error("WorktreeAdd should fail when the base branch does not exist")
	}
}

// TestWorktreeAddFailsWhenBranchAlreadyCheckedOut verifies that WorktreeAdd
// returns an error when trying to check out a branch that is already active in
// another worktree, covering the error-return path for the "existing branch" path.
func TestWorktreeAddFailsWhenBranchAlreadyCheckedOut(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)

	// Check out the branch in a first worktree.
	wt1 := filepath.Join(t.TempDir(), "wt1")
	if err := gitutil.WorktreeAdd(repo, wt1, "shared-branch", "main"); err != nil {
		t.Fatalf("WorktreeAdd wt1: %v", err)
	}

	// Attempt to add a second worktree for the same branch — git forbids this.
	wt2 := filepath.Join(t.TempDir(), "wt2")
	err := gitutil.WorktreeAdd(repo, wt2, "shared-branch", "main")
	if err == nil {
		t.Error("WorktreeAdd should fail when the branch is already checked out in another worktree")
	}
}

// TestCommitEmptyUserNameFails verifies that CommitEmpty returns an error on the
// second git config call (user.name) by installing a fake git wrapper that lets
// "config user.email" through but fails on "config user.name".
func TestCommitEmptyUserNameFails(t *testing.T) {
	// Must not call t.Parallel() — modifies PATH via t.Setenv.

	// Locate the real git binary before we override PATH.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found in PATH: %v", err)
	}

	// Write a fake git wrapper that blocks "git config user.name …".
	fakeDir := t.TempDir()
	fakePath := filepath.Join(fakeDir, "git")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  \"config user.name \"*)\n    echo \"fatal: blocked user.name\" >&2\n    exit 1\n    ;;\nesac\nexec %s \"$@\"\n", realGit)
	if err := os.WriteFile(fakePath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	// Initialize a real repo (using our fake git, which passes init through).
	dir := t.TempDir()
	if err := gitutil.InitWithBranch(dir, "main"); err != nil {
		t.Fatalf("InitWithBranch: %v", err)
	}

	// CommitEmpty should fail when user.name config is blocked.
	err = gitutil.CommitEmpty(dir, "test commit")
	if err == nil {
		t.Fatal("CommitEmpty should fail when git config user.name is blocked")
	}
	if !strings.Contains(err.Error(), "user.name") {
		t.Errorf("expected error mentioning user.name, got: %v", err)
	}
}

// TestMergeTreeCleanError verifies that MergeTreeClean returns an error when
// git cannot be executed (PATH overridden to empty directory), covering both
// the runAllowFail non-ExitError path and MergeTreeClean's error return.
func TestMergeTreeCleanError(t *testing.T) {
	// Must not call t.Parallel() — modifies PATH via t.Setenv.
	t.Setenv("PATH", t.TempDir()) // empty dir: git not found

	_, err := gitutil.MergeTreeClean("/any/path", "main", "feature")
	if err == nil {
		t.Fatal("MergeTreeClean should return error when git cannot be executed")
	}
}
