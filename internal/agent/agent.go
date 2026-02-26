package agent

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ilocn/stint/internal/workspace"
)

//go:embed embedded/*.md
var embeddedAgents embed.FS

// Def is a parsed agent definition from a .md file.
// Follows the Claude Code sub-agents frontmatter format.
type Def struct {
	Name            string
	Description     string
	Tools           []string
	DisallowedTools []string
	Model           string // "sonnet", "opus", "haiku", "inherit" — defaults to "inherit"
	PermissionMode  string // "default", "acceptEdits", "dontAsk", "bypassPermissions", "plan"
	MaxTurns        int
	SystemPrompt    string // body of the .md file after frontmatter
}

// hardcodedDefaults provides built-in fallback definitions for well-known agents.
// These are returned when neither workspace nor embedded FS has a matching file.
var hardcodedDefaults = map[string]*Def{
	"default": {
		Name:           "default",
		Model:          "inherit",
		PermissionMode: "bypassPermissions",
		MaxTurns:       50,
		SystemPrompt:   "You are a software engineer. Complete the task.",
	},
	"impl": {
		Name:           "impl",
		Model:          "inherit",
		PermissionMode: "bypassPermissions",
		MaxTurns:       50,
		SystemPrompt:   "You are a software engineer. Complete the task described in the prompt.",
	},
}

// Get reads and parses an agent definition by name using a three-layer lookup:
//  1. Workspace file (.st/agents/<name>.md) — user override wins
//  2. Embedded binary (internal/agent/embedded/<name>.md) — built-in definitions
//  3. Hardcoded defaults map — last resort for "default" and "impl"
//
// Any other unknown agent falls back to Get(ws, "impl").
func Get(ws *workspace.Workspace, name string) (*Def, error) {
	// 1. Workspace file takes highest priority (user customization).
	path := filepath.Join(ws.AgentsDir(), name+".md")
	data, err := os.ReadFile(path)
	if err == nil {
		return parse(name, string(data))
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	// 2. Embedded built-in.
	embData, embErr := embeddedAgents.ReadFile("embedded/" + name + ".md")
	if embErr == nil {
		return parse(name, string(embData))
	}

	// 3. Hardcoded fallback for "default" and "impl".
	if def, ok := hardcodedDefaults[name]; ok {
		d := *def
		return &d, nil
	}

	// Unknown agent — fall back to "impl".
	return Get(ws, "impl")
}

// List returns all agent definitions available in the workspace.
// Workspace-local agents (user overrides) take priority over embedded built-ins.
// Embedded built-ins fill in any agent names not overridden locally.
func List(ws *workspace.Workspace) ([]*Def, error) {
	seen := make(map[string]bool)
	var defs []*Def

	// Workspace-local agents first (higher priority / user overrides).
	entries, err := os.ReadDir(ws.AgentsDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		name := e.Name()[:len(e.Name())-3]
		def, err := Get(ws, name)
		if err != nil {
			continue
		}
		defs = append(defs, def)
		seen[name] = true
	}

	// Embedded built-ins — include any not already overridden by workspace.
	embEntries, _ := embeddedAgents.ReadDir("embedded")
	for _, e := range embEntries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		if seen[name] {
			continue
		}
		def, err := Get(ws, name)
		if err != nil {
			continue
		}
		defs = append(defs, def)
		seen[name] = true
	}

	return defs, nil
}

// Write saves an agent definition as a .md file using the Claude Code sub-agents format.
func Write(ws *workspace.Workspace, name, description, systemPrompt string, tools []string, model string, maxTurns int) error {
	if model == "" {
		model = "inherit"
	}
	if maxTurns == 0 {
		maxTurns = 50
	}
	var lines []string
	lines = append(lines, "---")
	lines = append(lines, fmt.Sprintf("name: %s", name))
	lines = append(lines, fmt.Sprintf("description: %s", description))
	if len(tools) > 0 {
		lines = append(lines, fmt.Sprintf("tools: %s", strings.Join(tools, ", ")))
	}
	lines = append(lines, fmt.Sprintf("model: %s", model))
	if maxTurns != 50 {
		lines = append(lines, fmt.Sprintf("maxTurns: %d", maxTurns))
	}
	lines = append(lines, "---")
	lines = append(lines, "")
	lines = append(lines, systemPrompt)
	lines = append(lines, "")

	content := strings.Join(lines, "\n")
	path := filepath.Join(ws.AgentsDir(), name+".md")
	return os.WriteFile(path, []byte(content), 0644)
}

// EmbeddedNames returns the names of all agents embedded in the binary.
// Useful for inspection commands like "st agent show".
func EmbeddedNames() []string {
	entries, _ := embeddedAgents.ReadDir("embedded")
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return names
}

// EmbeddedContent returns the raw content of an embedded agent file by name.
// Returns an error if the agent is not embedded.
func EmbeddedContent(name string) (string, error) {
	data, err := embeddedAgents.ReadFile("embedded/" + name + ".md")
	if err != nil {
		if isNotExist(err) {
			return "", fmt.Errorf("no embedded agent named %q", name)
		}
		return "", err
	}
	return string(data), nil
}

// isNotExist reports whether err is a "not found" error from the embed FS.
func isNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || strings.Contains(err.Error(), "file does not exist") || strings.Contains(err.Error(), "no such"))
}

// parse parses the YAML frontmatter + markdown body.
// Format:
//
//	---
//	key: value
//	---
//
//	body text
func parse(name, content string) (*Def, error) {
	def := &Def{Name: name, Model: "inherit", MaxTurns: 50}

	if !strings.HasPrefix(content, "---") {
		def.SystemPrompt = strings.TrimSpace(content)
		return def, nil
	}

	// Find closing ---.
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		def.SystemPrompt = strings.TrimSpace(content)
		return def, nil
	}

	frontmatter := rest[:end]
	body := strings.TrimSpace(rest[end+4:])
	def.SystemPrompt = body

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "name":
			def.Name = val
		case "description":
			def.Description = val
		case "tools":
			for _, t := range strings.Split(val, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					def.Tools = append(def.Tools, t)
				}
			}
		case "disallowedTools":
			for _, t := range strings.Split(val, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					def.DisallowedTools = append(def.DisallowedTools, t)
				}
			}
		case "model":
			def.Model = val
		case "permissionMode":
			def.PermissionMode = val
		case "maxTurns":
			if n, err := strconv.Atoi(val); err == nil {
				def.MaxTurns = n
			}
		}
	}
	return def, nil
}
