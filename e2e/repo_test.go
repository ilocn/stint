//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── st repo delete ──────────────────────────────────────────────────────────

// TestRepoDeleteRemovesRepo verifies that "st repo delete <name>" removes the
// repo from the workspace config and it no longer appears in "st repo list".
func TestRepoDeleteRemovesRepo(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, nil)

	// Add the repo first.
	mustSt(t, wsDir, "repo", "add", "todelete", repoDir)

	// Confirm it's present.
	out := mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "todelete") {
		t.Fatalf("repo list should contain 'todelete' before delete:\n%s", out)
	}

	// Delete it.
	out = mustSt(t, wsDir, "repo", "delete", "todelete")
	if !strings.Contains(out, "deleted repo todelete") {
		t.Errorf("delete output should say 'deleted repo todelete':\n%s", out)
	}

	// Confirm it's gone.
	out = mustSt(t, wsDir, "repo", "list")
	if strings.Contains(out, "todelete") {
		t.Errorf("repo list should NOT contain 'todelete' after delete:\n%s", out)
	}
}

// TestRepoDeleteUnknownErrors verifies that "st repo delete <unknown>" exits
// non-zero with a meaningful error message.
func TestRepoDeleteUnknownErrors(t *testing.T) {
	wsDir := initWorkspace(t, nil)

	out, code := runSt(t, wsDir, "repo", "delete", "nonexistent")
	if code == 0 {
		t.Errorf("st repo delete nonexistent should exit non-zero, got 0; output:\n%s", out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("error output should mention 'not found':\n%s", out)
	}
}

// TestRepoListAfterDeleteShowsOnlyRemainingRepos confirms that deleting one repo
// from a multi-repo workspace leaves the other repos intact.
func TestRepoListAfterDeleteShowsOnlyRemainingRepos(t *testing.T) {
	r1 := initRepo(t)
	r2 := initRepo(t)
	wsDir := initWorkspace(t, nil)

	mustSt(t, wsDir, "repo", "add", "keep", r1)
	mustSt(t, wsDir, "repo", "add", "gone", r2)

	// Verify both present.
	out := mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "keep") || !strings.Contains(out, "gone") {
		t.Fatalf("both repos should be listed before delete:\n%s", out)
	}

	// Delete "gone".
	mustSt(t, wsDir, "repo", "delete", "gone")

	// Only "keep" should remain.
	out = mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "keep") {
		t.Errorf("'keep' should still be listed after deleting 'gone':\n%s", out)
	}
	if strings.Contains(out, "gone") {
		t.Errorf("'gone' should not appear after delete:\n%s", out)
	}
}

// ─── st repo add ─────────────────────────────────────────────────────────────

// TestRepoAddGitClonesIntoWorkspace verifies that "st repo add <name> <path>"
// with a git repository source clones into <workspace-root>/<name> and
// registers that destination path in the workspace config.
func TestRepoAddGitClonesIntoWorkspace(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, nil)

	out := mustSt(t, wsDir, "repo", "add", "cloned", repoDir)
	if !strings.Contains(out, "cloned") {
		t.Errorf("add output should mention cloning:\n%s", out)
	}

	// Destination must exist inside the workspace.
	dest := filepath.Join(wsDir, "cloned")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("destination %s should exist after repo add: %v", dest, err)
	}

	// Destination must be a git repo (has .git/).
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("destination should be a git repo (has .git/): %v", err)
	}

	// Config must store the destination path, not the source path.
	out = mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, dest) {
		t.Errorf("repo list should show destination %s:\n%s", dest, out)
	}
	if strings.Contains(out, repoDir) {
		t.Errorf("repo list should NOT show original source %s (should show dest):\n%s", repoDir, out)
	}
}

// TestRepoAddNonGitCopiesIntoWorkspace verifies that "st repo add <name> <path>"
// with a plain (non-git) directory copies it into <workspace-root>/<name>.
func TestRepoAddNonGitCopiesIntoWorkspace(t *testing.T) {
	// Create a plain directory (not a git repo) with some files.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	subDir := filepath.Join(srcDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0644); err != nil {
		t.Fatalf("create nested file: %v", err)
	}

	wsDir := initWorkspace(t, nil)

	out := mustSt(t, wsDir, "repo", "add", "mycopy", srcDir)
	if !strings.Contains(out, "copied") {
		t.Errorf("add output should mention copying:\n%s", out)
	}

	// Destination must exist inside workspace.
	dest := filepath.Join(wsDir, "mycopy")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("destination %s should exist after repo add: %v", dest, err)
	}

	// Files must be copied.
	if _, err := os.Stat(filepath.Join(dest, "hello.txt")); err != nil {
		t.Errorf("hello.txt should be copied to destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", "nested.txt")); err != nil {
		t.Errorf("sub/nested.txt should be copied to destination: %v", err)
	}

	// Config stores destination.
	out = mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, dest) {
		t.Errorf("repo list should show destination %s:\n%s", dest, out)
	}
}

// TestRepoAddDestinationAlreadyExistsErrors verifies that "st repo add" fails
// with a clear error when <workspace-root>/<name> already exists on disk.
func TestRepoAddDestinationAlreadyExistsErrors(t *testing.T) {
	repoDir := initRepo(t)
	wsDir := initWorkspace(t, nil)

	// Pre-create the destination directory.
	dest := filepath.Join(wsDir, "conflict")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatalf("pre-create dest: %v", err)
	}

	out, code := runSt(t, wsDir, "repo", "add", "conflict", repoDir)
	if code == 0 {
		t.Errorf("st repo add should fail when destination already exists, got exit 0:\n%s", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("error should mention 'already exists':\n%s", out)
	}
}

// TestRepoDeleteAfterAddRemovesCopiedDirectory verifies that "st repo delete"
// removes the directory that was copied/cloned into the workspace by "st repo add".
func TestRepoDeleteAfterAddRemovesCopiedDirectory(t *testing.T) {
	// Use a non-git source so we test the copy path specifically.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "marker.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("create marker file: %v", err)
	}
	wsDir := initWorkspace(t, nil)

	// Add the repo — copies srcDir into <workspace>/mydir.
	mustSt(t, wsDir, "repo", "add", "mydir", srcDir)

	dest := filepath.Join(wsDir, "mydir")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("destination %s should exist after repo add: %v", dest, err)
	}

	// Delete the repo — should remove the copied directory.
	mustSt(t, wsDir, "repo", "delete", "mydir")

	// Directory must be gone.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination %s should be removed after repo delete, err=%v", dest, err)
	}

	// Repo must not appear in config.
	out := mustSt(t, wsDir, "repo", "list")
	if strings.Contains(out, "mydir") {
		t.Errorf("repo list should not contain 'mydir' after delete:\n%s", out)
	}
}

// TestRepoDeleteExternalPathNotRemoved verifies that "st repo delete" does NOT
// remove a directory that lives outside the workspace root (e.g. registered via
// "st init --repo name=path" pointing at an external path).
func TestRepoDeleteExternalPathNotRemoved(t *testing.T) {
	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalDir, "external.txt"), []byte("keep"), 0644); err != nil {
		t.Fatalf("create external file: %v", err)
	}

	// Init workspace with the external dir registered directly (no copy/clone).
	wsDir := initWorkspace(t, map[string]string{"external": externalDir})

	// Delete the repo — must NOT remove the external directory.
	mustSt(t, wsDir, "repo", "delete", "external")

	// External directory must still exist.
	if _, err := os.Stat(externalDir); err != nil {
		t.Errorf("external directory %s should NOT be removed on delete: %v", externalDir, err)
	}
	if _, err := os.Stat(filepath.Join(externalDir, "external.txt")); err != nil {
		t.Errorf("external.txt should still exist after deleting external repo: %v", err)
	}
}

// ─── st repo clone ────────────────────────────────────────────────────────────

// TestRepoCloneDefaultsToWorkspaceRoot verifies that "st repo clone <name> <url>"
// (without an explicit <dir>) clones into <workspace-root>/<name>, not a sibling
// of the workspace directory.
func TestRepoCloneDefaultsToWorkspaceRoot(t *testing.T) {
	// Use a local git repo as the "remote" URL (git supports file:// and plain paths).
	remoteDir := initRepo(t)
	wsDir := initWorkspace(t, nil)

	mustSt(t, wsDir, "repo", "clone", "clonetest", remoteDir)

	// The clone must be inside the workspace root.
	expectedDest := filepath.Join(wsDir, "clonetest")
	if _, err := os.Stat(expectedDest); err != nil {
		t.Fatalf("clone destination %s should exist inside workspace: %v", expectedDest, err)
	}
	// Must be a valid git repo.
	if _, err := os.Stat(filepath.Join(expectedDest, ".git")); err != nil {
		t.Errorf("cloned repo should contain .git/: %v", err)
	}

	// Config must store the workspace-internal path.
	out := mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, "clonetest") {
		t.Errorf("repo list should show 'clonetest':\n%s", out)
	}
	if !strings.Contains(out, expectedDest) {
		t.Errorf("repo list should show destination %s (inside workspace):\n%s", expectedDest, out)
	}
}

// TestRepoCloneExplicitDirOverridesDefault verifies that when a <dir> argument
// is provided to "st repo clone", that path is used instead of <workspace-root>/<name>.
func TestRepoCloneExplicitDirOverridesDefault(t *testing.T) {
	remoteDir := initRepo(t)
	wsDir := initWorkspace(t, nil)
	customDir := t.TempDir()

	mustSt(t, wsDir, "repo", "clone", "explicit", remoteDir, customDir)

	// customDir must contain the cloned repo.
	if _, err := os.Stat(filepath.Join(customDir, ".git")); err != nil {
		t.Errorf("explicit clone dir %s should contain .git/: %v", customDir, err)
	}

	// Config stores customDir, not workspace/<name>.
	out := mustSt(t, wsDir, "repo", "list")
	if !strings.Contains(out, customDir) {
		t.Errorf("repo list should show explicit dir %s:\n%s", customDir, out)
	}
	defaultDest := filepath.Join(wsDir, "explicit")
	if strings.Contains(out, defaultDest) {
		// This is actually fine if customDir happens to equal defaultDest, but
		// in this test they're different (customDir is t.TempDir()).
		if customDir != defaultDest {
			t.Errorf("repo list should NOT show default dest %s when explicit dir was provided:\n%s", defaultDest, out)
		}
	}
}
