package gitutil

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// run executes a git command in the given directory and returns stdout.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, errBuf.String())
	}
	return strings.TrimSpace(out.String()), nil
}

// runAllowFail executes a git command and returns (stdout, exitCode, error).
func runAllowFail(dir string, args ...string) (string, int, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return "", -1, err
		}
	}
	return strings.TrimSpace(out.String()), code, nil
}

// WorktreeAdd creates a new git worktree at worktreePath on branchName,
// branching off baseBranch. If branchName already exists, it is used directly.
// Prunes stale registrations first so a retry after os.RemoveAll-only cleanup
// doesn't fail with "missing but already registered worktree".
func WorktreeAdd(repoPath, worktreePath, branchName, baseBranch string) error {
	// Prune stale registrations (handles the case where a previous worktree at
	// worktreePath was cleaned up with os.RemoveAll without git worktree remove,
	// leaving a dangling entry in .git/worktrees/). Best-effort; ignore error.
	WorktreePrune(repoPath) //nolint:errcheck

	// Check if branch already exists.
	_, err := run(repoPath, "rev-parse", "--verify", branchName)
	if err != nil {
		// Branch doesn't exist, create it.
		if _, err2 := run(repoPath, "worktree", "add", "-b", branchName, worktreePath, baseBranch); err2 != nil {
			return err2
		}
	} else {
		// Branch exists, just add the worktree.
		if _, err2 := run(repoPath, "worktree", "add", worktreePath, branchName); err2 != nil {
			return err2
		}
	}
	return nil
}

// WorktreeRemove removes a git worktree.
func WorktreeRemove(repoPath, worktreePath string) error {
	_, err := run(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

// WorktreePrune runs git worktree prune to clean up stale references.
func WorktreePrune(repoPath string) error {
	_, err := run(repoPath, "worktree", "prune")
	return err
}

// CreateBranch creates a new branch off baseBranch in repoPath.
func CreateBranch(repoPath, branchName, baseBranch string) error {
	_, err := run(repoPath, "branch", branchName, baseBranch)
	return err
}

// BranchExists returns true if the branch exists in the repo.
func BranchExists(repoPath, branchName string) bool {
	_, err := run(repoPath, "rev-parse", "--verify", branchName)
	return err == nil
}

// Diff returns the diff of branch against baseBranch.
func Diff(repoPath, branch, baseBranch string) (string, error) {
	return run(repoPath, "diff", baseBranch+"..."+branch)
}

// MergeTreeClean runs git merge-tree --write-tree to check if merging branch
// into base would produce conflicts. Returns true if clean.
func MergeTreeClean(repoPath, baseBranch, branch string) (bool, error) {
	_, code, err := runAllowFail(repoPath, "merge-tree", "--write-tree", baseBranch, branch)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// CurrentBranch returns the current branch name in the repo/worktree.
func CurrentBranch(dir string) (string, error) {
	return run(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// DefaultBranch detects the default branch of a repo by checking the remote
// HEAD, then falling back to looking for "main" or "master" locally.
func DefaultBranch(repoPath string) string {
	// Try remote HEAD first (works for cloned repos).
	out, _, err := runAllowFail(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err == nil && out != "" {
		// "origin/main" → "main"
		parts := strings.SplitN(out, "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	// Fall back to checking common branch names.
	for _, branch := range []string{"main", "master"} {
		if BranchExists(repoPath, branch) {
			return branch
		}
	}
	return "main"
}

// Init initializes a new git repository.
func Init(dir string) error {
	_, err := run(dir, "init")
	return err
}

// InitWithBranch initializes a git repo with a specific initial branch name.
func InitWithBranch(dir, branch string) error {
	_, err := run(dir, "init", "-b", branch)
	return err
}

// CommitEmpty creates an empty initial commit.
func CommitEmpty(dir, message string) error {
	if _, err := run(dir, "config", "user.email", "st@local"); err != nil {
		return err
	}
	if _, err := run(dir, "config", "user.name", "Stint"); err != nil {
		return err
	}
	_, err := run(dir, "commit", "--allow-empty", "-m", message)
	return err
}
