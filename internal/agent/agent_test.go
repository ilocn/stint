package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/user/stint/internal/agent"
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

// TestGetBuiltinAgents verifies all seeded agents have required fields.
// Uses agent.List() so new builtins are automatically covered.
func TestGetBuiltinAgents(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(defs) < 6 {
		t.Fatalf("expected at least 6 built-in agents, got %d", len(defs))
	}

	for _, def := range defs {
		def := def
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			if def.Name == "" {
				t.Error("Name is empty")
			}
			if def.SystemPrompt == "" {
				t.Errorf("%s has empty system prompt", def.Name)
			}
			if def.Model == "" {
				t.Errorf("%s has empty model", def.Name)
			}
			if def.MaxTurns == 0 {
				t.Errorf("%s has 0 maxTurns", def.Name)
			}
		})
	}
}

func TestGetFallsBackToDefault(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// "nonexistent" doesn't exist, should fall back to default.
	def, err := agent.Get(ws, "nonexistent")
	if err != nil {
		t.Fatalf("Get(nonexistent): %v", err)
	}
	if def == nil {
		t.Fatal("expected fallback agent, got nil")
	}
}

func TestWriteAndRead(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := agent.Write(ws, "analyst",
		"Analyzes performance",
		"You are a performance analyst.",
		[]string{"Read", "Bash"},
		"opus",
		40,
	)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, err := agent.Get(ws, "analyst")
	if err != nil {
		t.Fatalf("Get(analyst): %v", err)
	}
	if def.Name != "analyst" {
		t.Errorf("Name = %s", def.Name)
	}
	if def.Description != "Analyzes performance" {
		t.Errorf("Description = %s", def.Description)
	}
	if def.SystemPrompt != "You are a performance analyst." {
		t.Errorf("SystemPrompt = %s", def.SystemPrompt)
	}
	if len(def.Tools) != 2 || def.Tools[0] != "Read" {
		t.Errorf("Tools = %v", def.Tools)
	}
	if def.Model != "opus" {
		t.Errorf("Model = %s", def.Model)
	}
	if def.MaxTurns != 40 {
		t.Errorf("MaxTurns = %d", def.MaxTurns)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Should have 6 built-ins.
	if len(defs) < 6 {
		t.Errorf("List returned %d agents, want at least 6", len(defs))
	}
}

func TestSecurityAgentHasRestrictedTools(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	def, err := agent.Get(ws, "security")
	if err != nil {
		t.Fatalf("Get(security): %v", err)
	}
	// Security agent should have tools listed (read-only tools).
	if len(def.Tools) == 0 {
		t.Error("security agent should have tools defined")
	}
	// Should NOT have Write or Edit tools (read-only review).
	for _, tool := range def.Tools {
		if tool == "Write" || tool == "Edit" {
			t.Errorf("security agent should not have %s tool", tool)
		}
	}
}

func TestWriteAgentWithURLInDescription(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Description contains a colon (URL) — old IndexByte parser would break this.
	desc := "See http://example.com for details"
	err := agent.Write(ws, "urltest", desc, "You do things.", nil, "inherit", 10)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	def, err := agent.Get(ws, "urltest")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.Description != desc {
		t.Errorf("Description = %q, want %q", def.Description, desc)
	}
}

// TestWriteDefaultModel verifies that passing an empty model string defaults to "inherit".
func TestWriteDefaultModel(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := agent.Write(ws, "empty-model-agent", "desc", "You do things.", nil, "", 10); err != nil {
		t.Fatalf("Write: %v", err)
	}
	def, err := agent.Get(ws, "empty-model-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.Model != "inherit" {
		t.Errorf("Model = %q, want \"inherit\"", def.Model)
	}
}

// TestWriteDefaultMaxTurns verifies that passing maxTurns=0 defaults to 50.
func TestWriteDefaultMaxTurns(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := agent.Write(ws, "zero-turns-agent", "desc", "You do things.", nil, "sonnet", 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	def, err := agent.Get(ws, "zero-turns-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", def.MaxTurns)
	}
}

// TestParseDisallowedToolsAndPermissionMode verifies that the disallowedTools
// and permissionMode YAML keys are parsed correctly, and that an invalid
// maxTurns value is silently ignored (keeping the default of 50).
func TestParseDisallowedToolsAndPermissionMode(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	content := "---\nname: restricted2\ndescription: restricted\ntools: Read\ndisallowedTools: Bash, Write\npermissionMode: acceptEdits\nmaxTurns: notanumber\n---\n\nYou are restricted.\n"
	agentPath := filepath.Join(ws.AgentsDir(), "restricted2.md")
	if err := os.WriteFile(agentPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	def, err := agent.Get(ws, "restricted2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(def.DisallowedTools) == 0 {
		t.Error("expected DisallowedTools to be non-empty")
	}
	if def.DisallowedTools[0] != "Bash" {
		t.Errorf("DisallowedTools[0] = %q, want \"Bash\"", def.DisallowedTools[0])
	}
	if def.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode = %q, want \"acceptEdits\"", def.PermissionMode)
	}
	// Invalid maxTurns "notanumber" should be ignored; default 50 is kept.
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50 (invalid value should keep default)", def.MaxTurns)
	}
}

// TestGetDefaultAgentHardcodedFallback verifies that Get("default") returns the
// hardcoded built-in default when no default.md file exists.
func TestGetDefaultAgentHardcodedFallback(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// default.md is not seeded by workspace.Init, so Get("default") must fall
	// back to the hardcodedDefaults map directly.
	def, err := agent.Get(ws, "default")
	if err != nil {
		t.Fatalf("Get(default) should succeed even when default.md is absent: %v", err)
	}
	if def.Name != "default" {
		t.Errorf("Name = %q, want \"default\"", def.Name)
	}
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", def.MaxTurns)
	}
	if def.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want \"bypassPermissions\"", def.PermissionMode)
	}
}

// TestListSkipsDirectories verifies that List ignores subdirectories inside the
// agents dir (they are not agent definitions).
func TestListSkipsDirectories(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a subdirectory inside agents dir.
	subdir := filepath.Join(ws.AgentsDir(), "notanagent")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range defs {
		if d.Name == "notanagent" {
			t.Error("List should not include directories as agent definitions")
		}
	}
}

// TestListSkipsUnreadableAgents verifies that List continues gracefully when
// one agent file causes a read error (e.g., path is a directory).
func TestListSkipsUnreadableAgents(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a directory named "broken.md" so Get("broken") returns an error.
	agentPath := filepath.Join(ws.AgentsDir(), "broken.md")
	if err := os.MkdirAll(agentPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// List should still succeed, skipping the broken entry.
	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List should not fail for unreadable agents: %v", err)
	}
	// At least the seeded built-in agents should still be returned.
	if len(defs) < 6 {
		t.Errorf("List returned %d agents, want at least 6", len(defs))
	}
	for _, d := range defs {
		if d.Name == "broken" {
			t.Error("List should not include the broken agent")
		}
	}
}

// TestParseLineWithNoColon verifies that parse gracefully skips frontmatter
// lines that contain no colon (no key:value structure), hitting the continue
// path rather than panicking or erroring.
func TestParseLineWithNoColon(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Include a frontmatter line without a colon after the name key.
	content := "---\nname: nocolon\nthislinehasnocolonatall\n---\n\nSystem prompt here.\n"
	agentPath := filepath.Join(ws.AgentsDir(), "nocolon.md")
	if err := os.WriteFile(agentPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	def, err := agent.Get(ws, "nocolon")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if def.Name != "nocolon" {
		t.Errorf("Name = %q, want \"nocolon\"", def.Name)
	}
	if def.SystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
}

// TestListSkipsUnreadableAgentFiles verifies that List skips agent .md files
// that exist but cannot be read (permission denied), covering the
// "if err != nil { continue }" path in the list loop.
func TestListSkipsUnreadableAgentFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test cannot run as root")
	}
	t.Parallel()
	ws := newWS(t)

	// Create a .md file then remove read permission so os.ReadFile returns
	// "permission denied" (not IsNotExist) → Get returns an error → List skips it.
	agentPath := filepath.Join(ws.AgentsDir(), "noperm.md")
	if err := os.WriteFile(agentPath, []byte("name: noperm\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(agentPath, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		// Restore permission so t.TempDir cleanup can delete the file.
		os.Chmod(agentPath, 0644) //nolint:errcheck
	})

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List must not fail for permission-denied agent file: %v", err)
	}
	for _, d := range defs {
		if d.Name == "noperm" {
			t.Error("List should skip agents it cannot read")
		}
	}
}

// ── Embedded agent tests ─────────────────────────────────────────────────────

// TestGetFallsBackToEmbedded verifies that Get() returns an embedded built-in
// definition when no workspace override file exists.
func TestGetFallsBackToEmbedded(t *testing.T) {
	t.Parallel()
	ws := newWS(t) // workspace with empty agents dir (no seeded files)

	// "impl" should come from the embedded FS, not the workspace.
	def, err := agent.Get(ws, "impl")
	if err != nil {
		t.Fatalf("Get(impl) should succeed via embedded fallback: %v", err)
	}
	if def.Name != "impl" {
		t.Errorf("Name = %q, want impl", def.Name)
	}
	if def.SystemPrompt == "" {
		t.Error("embedded impl agent should have a non-empty SystemPrompt")
	}
	// Make sure no impl.md exists in workspace (confirming embedded path was used).
	if _, err := os.Stat(filepath.Join(ws.AgentsDir(), "impl.md")); !os.IsNotExist(err) {
		t.Skip("impl.md exists in workspace — embedded fallback not exercised")
	}
}

// TestGetWorkspaceOverridesEmbedded verifies that a workspace-local agent file
// takes priority over the embedded built-in for the same agent name.
func TestGetWorkspaceOverridesEmbedded(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Write a custom impl.md to the workspace agents dir.
	customContent := "---\nname: impl\ndescription: custom impl\nmodel: inherit\n---\n\nCustom implementation agent.\n"
	implPath := filepath.Join(ws.AgentsDir(), "impl.md")
	if err := os.WriteFile(implPath, []byte(customContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	def, err := agent.Get(ws, "impl")
	if err != nil {
		t.Fatalf("Get(impl): %v", err)
	}
	if def.Description != "custom impl" {
		t.Errorf("Description = %q, want 'custom impl' (workspace should override embedded)", def.Description)
	}
	if def.SystemPrompt != "Custom implementation agent." {
		t.Errorf("SystemPrompt = %q, want custom prompt", def.SystemPrompt)
	}
}

// TestListIncludesEmbedded verifies that List() includes embedded built-in agents
// even when the workspace agents directory is empty.
func TestListIncludesEmbedded(t *testing.T) {
	t.Parallel()
	ws := newWS(t) // workspace with no seeded files

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// All 8 embedded agents should appear.
	expected := []string{"impl", "debug", "test", "docs", "review", "security", "merge", "merge-task"}
	found := make(map[string]bool)
	for _, d := range defs {
		found[d.Name] = true
	}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("List should include embedded agent %q", name)
		}
	}
}

// TestListWorkspaceOverridesEmbeddedInList verifies that when a workspace file
// exists for an embedded agent, List() returns the workspace version (not both).
func TestListWorkspaceOverridesEmbeddedInList(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Write a custom impl.md to workspace.
	customContent := "---\nname: impl\ndescription: workspace-impl\nmodel: inherit\n---\n\nWorkspace version.\n"
	implPath := filepath.Join(ws.AgentsDir(), "impl.md")
	if err := os.WriteFile(implPath, []byte(customContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	defs, err := agent.List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Count occurrences of "impl" — should be exactly 1.
	implCount := 0
	var implDef *agent.Def
	for _, d := range defs {
		if d.Name == "impl" {
			implCount++
			implDef = d
		}
	}
	if implCount != 1 {
		t.Errorf("impl should appear exactly once in List(), got %d", implCount)
	}
	if implDef != nil && implDef.Description != "workspace-impl" {
		t.Errorf("List should return workspace version of impl, got description=%q", implDef.Description)
	}
}
