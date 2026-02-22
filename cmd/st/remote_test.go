package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── TestParseGitHubSource ────────────────────────────────────────────────────

func TestParseGitHubSource(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantPath  string
		wantRef   string
		wantErr   bool
	}{
		{
			input:     "owner/repo/path/to/file.md",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "path/to/file.md",
		},
		{
			input:     "owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "",
		},
		{
			input:     "owner/repo/agents/python-expert.md",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "agents/python-expert.md",
		},
		{
			input:     "github:owner/repo/path",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "path",
		},
		{
			input:     "github:owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "",
		},
		{
			input:     "owner/repo/path@mybranch",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "path",
			wantRef:   "mybranch",
		},
		{
			input:     "owner/repo/path@v1.0.0",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "path",
			wantRef:   "v1.0.0",
		},
		{
			input:     "github:owner/repo/skills/golang-pro@main",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantPath:  "skills/golang-pro",
			wantRef:   "main",
		},
		{input: "owner", wantErr: true},
		{input: "", wantErr: true},
		{input: "github:", wantErr: true},
		{input: "/repo/path", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			owner, repo, path, ref, err := parseGitHubSource(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseGitHubSource(%q): expected error, got nil (owner=%q repo=%q path=%q ref=%q)",
						tc.input, owner, repo, path, ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitHubSource(%q): unexpected error: %v", tc.input, err)
			}
			if owner != tc.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tc.wantOwner)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tc.wantRepo)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			if ref != tc.wantRef {
				t.Errorf("ref = %q, want %q", ref, tc.wantRef)
			}
		})
	}
}

// ─── TestExtractFrontmatter ───────────────────────────────────────────────────

func TestExtractFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantName    string
		wantDesc    string
		wantBodyHas string
		wantBodyNot string
	}{
		{
			name: "full frontmatter with name and description",
			input: `---
name: my-agent
description: Does cool things
---
You are a helpful assistant.
More instructions here.`,
			wantName:    "my-agent",
			wantDesc:    "Does cool things",
			wantBodyHas: "You are a helpful assistant.",
		},
		{
			name:        "no frontmatter — entire content is body",
			input:       "Just a plain body without frontmatter.",
			wantBodyHas: "Just a plain body",
		},
		{
			name: "frontmatter without name",
			input: `---
description: Only a description
---

Body text here.`,
			wantDesc:    "Only a description",
			wantBodyHas: "Body text here.",
		},
		{
			name: "multiline body is preserved",
			input: `---
name: test-agent
---
Line one.
Line two.
Line three.`,
			wantName:    "test-agent",
			wantBodyHas: "Line one.",
		},
		{
			name:  "empty content",
			input: "",
		},
		{
			name: "frontmatter with extra fields is tolerated",
			input: `---
name: extra-agent
description: extra desc
model: opus
maxTurns: 30
tools: bash, read
---
Extra agent body.`,
			wantName:    "extra-agent",
			wantDesc:    "extra desc",
			wantBodyHas: "Extra agent body.",
		},
		{
			name: "unclosed frontmatter treated as body",
			input: `---
name: unclosed
this has no closing marker`,
			wantBodyHas: "name: unclosed",
		},
		{
			name: "body does not include frontmatter lines",
			input: `---
name: clean-agent
description: clean
---
Clean body only.`,
			wantName:    "clean-agent",
			wantDesc:    "clean",
			wantBodyHas: "Clean body only.",
			wantBodyNot: "name: clean-agent",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			name, desc, body := extractFrontmatter([]byte(tc.input))
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if desc != tc.wantDesc {
				t.Errorf("description = %q, want %q", desc, tc.wantDesc)
			}
			if tc.wantBodyHas != "" && !strings.Contains(body, tc.wantBodyHas) {
				t.Errorf("body = %q, want it to contain %q", body, tc.wantBodyHas)
			}
			if tc.wantBodyNot != "" && strings.Contains(body, tc.wantBodyNot) {
				t.Errorf("body = %q, must NOT contain %q", body, tc.wantBodyNot)
			}
		})
	}
}

// ─── TestBuildSkillPrompt ─────────────────────────────────────────────────────

func TestBuildSkillPrompt(t *testing.T) {
	t.Run("SKILL.md body first then others sorted alphabetically", func(t *testing.T) {
		dir := t.TempDir()

		writeTestFile(t, filepath.Join(dir, "SKILL.md"), `---
name: my-skill
description: A skill for testing
---
Main skill instructions here.`)
		writeTestFile(t, filepath.Join(dir, "a-reference.md"), `---
name: ref-a
---
Reference A content.`)
		writeTestFile(t, filepath.Join(dir, "b-reference.md"), "Reference B content without frontmatter.")

		name, desc, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		if name != "my-skill" {
			t.Errorf("name = %q, want %q", name, "my-skill")
		}
		if desc != "A skill for testing" {
			t.Errorf("description = %q, want %q", desc, "A skill for testing")
		}
		idxSkill := strings.Index(body, "Main skill instructions here.")
		idxA := strings.Index(body, "Reference A content.")
		idxB := strings.Index(body, "Reference B content without frontmatter.")
		if idxSkill == -1 || idxA == -1 || idxB == -1 {
			t.Fatalf("body missing expected content; body = %q", body)
		}
		if idxSkill > idxA {
			t.Error("SKILL.md body should appear before a-reference.md")
		}
		if idxA > idxB {
			t.Error("a-reference.md should appear before b-reference.md (alphabetical)")
		}
	})

	t.Run("non-.md files are skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "SKILL.md"), `---
name: test-skill
---
Skill body.`)
		writeTestFile(t, filepath.Join(dir, "script.py"), "python code here")
		writeTestFile(t, filepath.Join(dir, "helper.sh"), "#!/bin/bash")
		writeTestFile(t, filepath.Join(dir, "util.js"), "console.log('hi')")

		_, _, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		for _, forbidden := range []string{"python code here", "#!/bin/bash", "console.log"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("body should not contain non-.md content %q", forbidden)
			}
		}
		if !strings.Contains(body, "Skill body.") {
			t.Errorf("body missing SKILL.md content: %q", body)
		}
	})

	t.Run("alphabetical order z before a test", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "SKILL.md"), `---
name: sorted-skill
---
SKILL BODY`)
		writeTestFile(t, filepath.Join(dir, "z-last.md"), "Z FILE CONTENT")
		writeTestFile(t, filepath.Join(dir, "a-first.md"), "A FILE CONTENT")

		_, _, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		idxSkill := strings.Index(body, "SKILL BODY")
		idxA := strings.Index(body, "A FILE CONTENT")
		idxZ := strings.Index(body, "Z FILE CONTENT")
		if idxSkill == -1 || idxA == -1 || idxZ == -1 {
			t.Fatalf("body missing expected content; body = %q", body)
		}
		if idxSkill > idxA {
			t.Error("SKILL BODY should appear before A FILE CONTENT")
		}
		if idxA > idxZ {
			t.Error("A FILE CONTENT should appear before Z FILE CONTENT (alphabetical order)")
		}
	})

	t.Run("no SKILL.md: only reference files sorted", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "beta.md"), "BETA CONTENT")
		writeTestFile(t, filepath.Join(dir, "alpha.md"), "ALPHA CONTENT")

		name, desc, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		if name != "" || desc != "" {
			t.Errorf("expected empty name/desc without SKILL.md, got name=%q desc=%q", name, desc)
		}
		idxA := strings.Index(body, "ALPHA CONTENT")
		idxB := strings.Index(body, "BETA CONTENT")
		if idxA == -1 || idxB == -1 {
			t.Fatalf("body missing content: %q", body)
		}
		if idxA > idxB {
			t.Error("alpha.md should appear before beta.md")
		}
	})

	t.Run("subdirectory .md files included sorted by relative path", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "SKILL.md"), `---
name: nested-skill
---
SKILL INTRO`)
		if err := os.MkdirAll(filepath.Join(dir, "references"), 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		writeTestFile(t, filepath.Join(dir, "references", "channels.md"), "CHANNELS CONTENT")
		writeTestFile(t, filepath.Join(dir, "references", "goroutines.md"), "GOROUTINES CONTENT")

		name, _, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		if name != "nested-skill" {
			t.Errorf("name = %q, want nested-skill", name)
		}
		idxC := strings.Index(body, "CHANNELS CONTENT")
		idxG := strings.Index(body, "GOROUTINES CONTENT")
		if idxC == -1 || idxG == -1 {
			t.Fatalf("body missing content: %q", body)
		}
		if idxC > idxG {
			t.Error("channels.md should appear before goroutines.md (alphabetical)")
		}
	})

	t.Run("empty directory returns empty results", func(t *testing.T) {
		dir := t.TempDir()
		name, desc, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt empty dir: %v", err)
		}
		if name != "" || desc != "" || body != "" {
			t.Errorf("expected all empty for empty dir, got name=%q desc=%q body=%q", name, desc, body)
		}
	})

	t.Run("bodies separated by double newline", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "SKILL.md"), `---
name: sep-skill
---
PART ONE`)
		writeTestFile(t, filepath.Join(dir, "extra.md"), "PART TWO")

		_, _, body, err := buildSkillPrompt(dir)
		if err != nil {
			t.Fatalf("buildSkillPrompt: %v", err)
		}
		if !strings.Contains(body, "PART ONE\n\nPART TWO") {
			t.Errorf("parts should be separated by double newline, got: %q", body)
		}
	})
}

// ─── TestAgentCreateCmd ────────────────────────────────────────────────────

func TestAgentCreateCmdRun(t *testing.T) {
	_, g := initTestWS(t)
	c := &AgentCreateCmd{
		Name:        "remote-create-agent",
		Prompt:      "You are a test agent.",
		Description: "test agent",
		Model:       "sonnet",
		MaxTurns:    30,
	}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentCreateCmd.Run: %v", err)
	}
}

// ─── TestAgentRemoveCmd ───────────────────────────────────────────────────────

func TestAgentRemoveCmdRunSuccess(t *testing.T) {
	ws, g := initTestWS(t)

	// Create an agent to remove.
	create := &AgentCreateCmd{Name: "to-remove", Prompt: "temporary agent"}
	if err := create.Run(g); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	agentPath := filepath.Join(ws.AgentsDir(), "to-remove.md")
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("agent file missing before remove: %v", err)
	}

	c := &AgentRemoveCmd{Name: "to-remove"}
	if err := c.Run(g); err != nil {
		t.Fatalf("AgentRemoveCmd.Run: %v", err)
	}

	if _, err := os.Stat(agentPath); err == nil {
		t.Error("agent file still exists after remove")
	}
}

func TestAgentRemoveCmdRunNotFound(t *testing.T) {
	_, g := initTestWS(t)
	err := (&AgentRemoveCmd{Name: "no-such-agent"}).Run(g)
	if err == nil {
		t.Fatal("expected error removing non-existent agent, got nil")
	}
	if !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("error %q should contain 'agent not found'", err.Error())
	}
}

// ─── helper ───────────────────────────────────────────────────────────────────

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeTestFile(%s): %v", path, err)
	}
}
