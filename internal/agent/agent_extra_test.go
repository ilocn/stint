package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ilocn/stint/internal/agent"
	"github.com/ilocn/stint/internal/workspace"
)

// TestGetImplFallbackDefault verifies that Get("impl") returns a sensible
// definition when impl.md does not exist in the workspace (uses embedded built-in).
func TestGetImplFallbackDefault(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// workspace.Init no longer seeds agent files — impl comes from embedded FS.
	// Verify the workspace agents dir has no impl.md (confirming no seeding).
	implPath := filepath.Join(ws.AgentsDir(), "impl.md")
	if _, err := os.Stat(implPath); !os.IsNotExist(err) {
		t.Skip("impl.md unexpectedly exists in agents dir; skipping embedded fallback test")
	}

	def, err := agent.Get(ws, "impl")
	if err != nil {
		t.Fatalf("Get(impl) with no workspace file: %v", err)
	}
	if def.Name != "impl" {
		t.Errorf("Name = %q, want impl", def.Name)
	}
	if def.SystemPrompt == "" {
		t.Error("SystemPrompt should not be empty for default impl agent")
	}
	if def.MaxTurns == 0 {
		t.Error("MaxTurns should be nonzero for default impl agent")
	}
}

// TestGetReadError verifies that Get returns an error when a workspace agent file
// exists but cannot be read (permission denied, not just not-found).
// The workspace file takes priority over embedded; if it's unreadable, Get must fail.
func TestGetReadError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Create a custom impl.md in the workspace agents dir (workspace overrides embedded).
	implPath := filepath.Join(ws.AgentsDir(), "impl.md")
	if err := os.WriteFile(implPath, []byte("---\nname: impl\n---\nCustom impl.\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Remove read permission so os.ReadFile returns EACCES (not IsNotExist).
	if err := os.Chmod(implPath, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(implPath, 0644) //nolint:errcheck

	_, err := agent.Get(ws, "impl")
	if err == nil {
		t.Error("Get should fail when workspace agent file is unreadable, got nil")
	}
}

// TestListWithNonMDFile verifies that List skips non-.md files and subdirectories
// in the agents directory (the "continue" branch in the listing loop).
func TestListWithNonMDFile(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create a non-.md file and a subdirectory to trigger the skip path.
	if err := os.WriteFile(filepath.Join(ws.AgentsDir(), "README.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatalf("WriteFile README.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(ws.AgentsDir(), "subdir"), 0755); err != nil {
		t.Fatalf("MkdirAll subdir: %v", err)
	}

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Should have the seeded agents but NOT the README.txt or subdir.
	for _, d := range defs {
		if d.Name == "README" {
			t.Error("List should not return README.txt as an agent")
		}
		if d.Name == "subdir" {
			t.Error("List should not return a subdirectory as an agent")
		}
	}
}

// TestListReadDirError verifies that List returns an error when the agents
// directory is not readable (permission denied).
func TestListReadDirError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	if err := os.Chmod(ws.AgentsDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.AgentsDir(), 0755) //nolint:errcheck

	_, err := agent.List(ws)
	if err == nil {
		t.Error("List should fail when agents dir is unreadable, got nil")
	}
}

// TestParseDisallowedTools verifies that Write and Get correctly round-trip
// the DisallowedTools field through the frontmatter format.
func TestParseDisallowedTools(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Write a raw agent file with disallowedTools.
	content := `---
name: restricted
description: A restricted agent
disallowedTools: Write, Edit
model: inherit
---

You are restricted.
`
	path := filepath.Join(ws.AgentsDir(), "restricted.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "restricted")
	if err != nil {
		t.Fatalf("Get(restricted): %v", err)
	}
	if len(def.DisallowedTools) != 2 {
		t.Errorf("DisallowedTools = %v, want [Write, Edit]", def.DisallowedTools)
	}
}

// TestParsePermissionMode verifies that the permissionMode frontmatter key
// is parsed and surfaced on the Def struct.
func TestParsePermissionMode(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	content := `---
name: bypass
description: Bypass agent
permissionMode: bypassPermissions
model: inherit
---

You bypass.
`
	path := filepath.Join(ws.AgentsDir(), "bypass.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "bypass")
	if err != nil {
		t.Fatalf("Get(bypass): %v", err)
	}
	if def.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", def.PermissionMode)
	}
}

// TestParseMaxTurns verifies that the maxTurns frontmatter key is parsed and
// applied even when the Write helper omits it (value == 50 → no line written).
func TestParseMaxTurns(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	content := `---
name: custom-turns
description: Custom turns
maxTurns: 25
model: inherit
---

You have 25 turns.
`
	path := filepath.Join(ws.AgentsDir(), "custom-turns.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "custom-turns")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.MaxTurns != 25 {
		t.Errorf("MaxTurns = %d, want 25", def.MaxTurns)
	}
}

// TestParseNoFrontmatter verifies that an agent file without frontmatter
// is treated as a plain system prompt (entire content becomes SystemPrompt).
func TestParseNoFrontmatter(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	content := "You are a plain agent with no frontmatter."
	path := filepath.Join(ws.AgentsDir(), "plain.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "plain")
	if err != nil {
		t.Fatalf("Get(plain): %v", err)
	}
	if def.SystemPrompt != content {
		t.Errorf("SystemPrompt = %q, want %q", def.SystemPrompt, content)
	}
	// Defaults should still be applied.
	if def.Model != "inherit" {
		t.Errorf("Model = %q, want inherit", def.Model)
	}
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", def.MaxTurns)
	}
}

// TestParseUnclosedFrontmatter verifies that content starting with "---" but
// missing the closing "---" is treated as a plain system prompt (fallback path).
func TestParseUnclosedFrontmatter(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	content := "---\nname: broken\nno closing marker"
	path := filepath.Join(ws.AgentsDir(), "broken.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "broken")
	if err != nil {
		t.Fatalf("Get(broken): %v", err)
	}
	// When frontmatter is unclosed the whole content is the system prompt.
	if def.SystemPrompt == "" {
		t.Error("SystemPrompt should not be empty for unclosed frontmatter")
	}
}

// TestWriteDefaultModelAndMaxTurns verifies that Write applies sensible defaults
// when model is empty and maxTurns is 0. The defaults are "inherit" and 50.
func TestWriteDefaultModelAndMaxTurns(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	err := agent.Write(ws, "defaulter",
		"Uses defaults",
		"You use defaults.",
		nil,
		"", // empty model → should default to "inherit"
		0,  // zero maxTurns → should default to 50
	)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, err := agent.Get(ws, "defaulter")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.Model != "inherit" {
		t.Errorf("Model = %q, want inherit", def.Model)
	}
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", def.MaxTurns)
	}
}

// TestListSkipsUnreadableAgentFile verifies that List skips files that cannot
// be parsed (the "continue" path inside the for loop when Get fails).
func TestListSkipsUnreadableAgentFile(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newWS(t)

	// Create an unreadable agent file.
	badPath := filepath.Join(ws.AgentsDir(), "bad.md")
	if err := os.WriteFile(badPath, []byte("---\nname: bad\n---\ncontent"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(badPath, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(badPath, 0644) //nolint:errcheck

	// List should not fail — it should just skip the unreadable file.
	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// "bad" should not appear in the list.
	for _, d := range defs {
		if d.Name == "bad" {
			t.Error("List should skip unreadable agent file")
		}
	}
}

// TestGetFallsBackToImplForMissingCustomAgent verifies that Get("custom") when
// "custom.md" doesn't exist falls back to "impl" (the else branch in Get).
func TestGetFallsBackToImplForMissingCustomAgent(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	def, err := agent.Get(ws, "totally-made-up-agent-xyz")
	if err != nil {
		t.Fatalf("Get fallback: %v", err)
	}
	// Should have fallen back to impl.
	if def == nil {
		t.Fatal("Get fallback returned nil")
	}
	if def.Name != "impl" {
		t.Errorf("fallback Name = %q, want impl", def.Name)
	}
}

// TestParseToolsEmptyEntries verifies that tools with empty entries after split
// are filtered out (the `if t != "" { append }` inner branch).
func TestParseToolsEmptyEntries(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// "tools: Read, , Write" has an empty second element after split.
	content := `---
name: tool-test
description: Tool test
tools: Read, , Write
model: inherit
---

You have tools.
`
	path := filepath.Join(ws.AgentsDir(), "tool-test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "tool-test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Should have exactly 2 tools (empty entry filtered).
	if len(def.Tools) != 2 {
		t.Errorf("Tools = %v (len=%d), want [Read Write]", def.Tools, len(def.Tools))
	}
}

// TestListOnFreshWorkspace verifies that List returns the embedded built-in agents
// even when the workspace agents directory is empty (no user overrides).
func TestListOnFreshWorkspace(t *testing.T) {
	t.Parallel()
	// Create a raw workspace dir without seeding agents.
	dir := t.TempDir()
	stDir := filepath.Join(dir, ".st")
	agentsDir := filepath.Join(stDir, "agents")
	for _, sub := range []string{agentsDir, filepath.Join(stDir, "goals"),
		filepath.Join(stDir, "tasks", "pending"), filepath.Join(stDir, "tasks", "running"),
		filepath.Join(stDir, "tasks", "done"), filepath.Join(stDir, "tasks", "failed"),
		filepath.Join(stDir, "tasks", "cancelled"), filepath.Join(stDir, "tasks", "blocked"),
		filepath.Join(stDir, "workers"), filepath.Join(stDir, "worktrees"),
		filepath.Join(stDir, "logs"), filepath.Join(stDir, "heartbeats"),
	} {
		os.MkdirAll(sub, 0755) //nolint:errcheck
	}
	os.WriteFile(filepath.Join(stDir, "workspace.json"), []byte(`{"version":1,"repos":{}}`), 0644) //nolint:errcheck
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// List should now return the embedded built-in agents.
	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List on fresh workspace: %v", err)
	}
	// We have 8 embedded built-ins: impl, debug, test, docs, review, security, merge, merge-task.
	if len(defs) < 6 {
		t.Errorf("expected at least 6 embedded agents, got %d", len(defs))
	}
	// Verify no workspace file exists for any of them.
	for _, d := range defs {
		path := filepath.Join(agentsDir, d.Name+".md")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("agent %s.md should not exist as workspace file (uses embedded)", d.Name)
		}
	}
}

// TestParseFrontmatterLineWithoutColon verifies that frontmatter lines that
// do not contain a ":" separator are silently skipped (the len(parts) != 2 continue).
func TestParseFrontmatterLineWithoutColon(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// A frontmatter line without ":" to trigger the len(parts) != 2 path.
	content := `---
name: nocodon
this line has no colon separator
description: valid
model: inherit
---

System prompt.
`
	path := filepath.Join(ws.AgentsDir(), "nocodon.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "nocodon")
	if err != nil {
		t.Fatalf("Get(nocodon): %v", err)
	}
	// Valid lines should still be parsed correctly.
	if def.Name != "nocodon" {
		t.Errorf("Name = %q, want nocodon", def.Name)
	}
	if def.Description != "valid" {
		t.Errorf("Description = %q, want valid", def.Description)
	}
}
