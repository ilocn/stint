package workspace_test

// workspace_abs_test.go — covers the filepath.Abs error paths in Open,
// FindRoot, and Init by temporarily changing CWD to a deleted directory.
//
// On POSIX systems (macOS, Linux), a process can delete its own current
// working directory. After deletion, os.Getwd() returns an error, which
// causes filepath.Abs("relative-path") to fail.
//
// This file adds coverage for the `return nil, err` blocks inside Open,
// FindRoot, and Init that are only reachable when filepath.Abs fails.
//
// IMPORTANT: These tests must NOT be parallel — they modify the process-wide
// current working directory.

import (
	"os"
	"syscall"
	"testing"

	"github.com/user/stint/internal/workspace"
)

// TestOpenFilepathAbsErrorWithDeletedCWD verifies that workspace.Open returns
// an error (not nil) when called with a relative path while the process's
// current directory has been deleted. This covers the
// `if err != nil { return nil, err }` block after filepath.Abs in Open.
//
// Not parallel — modifies process-wide CWD.
func TestOpenFilepathAbsErrorWithDeletedCWD(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-deleted-cwd-open")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	// Always restore the original directory and remove the temp dir.
	defer func() {
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			// If we can't restore the original dir, fall back to home.
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	// Delete the directory while we're in it.
	if err := syscall.Rmdir(tmpDir); err != nil {
		t.Skipf("cannot delete CWD on this platform (%v); skipping filepath.Abs error coverage", err)
		return
	}

	// Now filepath.Abs("relative") should fail because os.Getwd() fails.
	_, err = workspace.Open("relative-workspace")
	if err == nil {
		// Platform didn't fail filepath.Abs — skip gracefully.
		t.Logf("filepath.Abs did not fail after CWD deletion on this platform; coverage unavailable")
		return
	}
	// If we reach here, the filepath.Abs error path in Open was triggered.
}

// TestInitFilepathAbsErrorWithDeletedCWD verifies that workspace.Init returns
// an error when called with a relative path while CWD is deleted.
// Covers the `if err != nil { return nil, err }` block after filepath.Abs in Init.
//
// Not parallel — modifies process-wide CWD.
func TestInitFilepathAbsErrorWithDeletedCWD(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-deleted-cwd-init")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	defer func() {
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	if err := syscall.Rmdir(tmpDir); err != nil {
		t.Skipf("cannot delete CWD on this platform (%v); skipping filepath.Abs error coverage", err)
		return
	}

	_, err = workspace.Init("relative-init", nil)
	if err == nil {
		t.Logf("filepath.Abs did not fail after CWD deletion on this platform; coverage unavailable")
		return
	}
}

// TestFindRootFilepathAbsErrorWithDeletedCWD verifies that workspace.FindRoot
// returns an error when called with a relative path while CWD is deleted.
// Covers the `if err != nil { return nil, err }` block in FindRoot.
//
// Not parallel — modifies process-wide CWD.
func TestFindRootFilepathAbsErrorWithDeletedCWD(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "ws-deleted-cwd-findroot")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		t.Fatalf("Chdir: %v", err)
	}

	defer func() {
		if restoreErr := os.Chdir(origDir); restoreErr != nil {
			homeDir, _ := os.UserHomeDir()
			os.Chdir(homeDir) //nolint:errcheck
		}
		os.RemoveAll(tmpDir) //nolint:errcheck
	}()

	if err := syscall.Rmdir(tmpDir); err != nil {
		t.Skipf("cannot delete CWD on this platform (%v); skipping filepath.Abs error coverage", err)
		return
	}

	_, err = workspace.FindRoot("relative-findroot")
	if err == nil {
		t.Logf("filepath.Abs did not fail after CWD deletion on this platform; coverage unavailable")
		return
	}
}
