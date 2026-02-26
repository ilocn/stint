package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ilocn/stint/internal/agent"
	"github.com/ilocn/stint/internal/workspace"
)

// fetchAndInstall downloads an agent or skill from source and writes it to
// ws.AgentsDir()/<name>.md.  source can be:
//   - owner/repo/path[@ref] or github:owner/repo/path[@ref]  → git clone
//   - https://... or http://...                              → direct HTTP GET
//
// nameOverride, if non-empty, overrides the name derived from frontmatter.
func fetchAndInstall(ws *workspace.Workspace, source, nameOverride string) error {
	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
		return fetchFromURL(ws, source, nameOverride)
	}

	owner, repo, path, ref, err := parseGitHubSource(source)
	if err != nil {
		return fmt.Errorf("parsing source %q: %w", source, err)
	}
	if path == "" {
		return fmt.Errorf("source %q has no path after owner/repo; specify a file or directory", source)
	}

	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	fmt.Printf("cloning %s/%s...\n", owner, repo)
	tmpDir, err := gitCloneTemp(repoURL, ref)
	if err != nil {
		return fmt.Errorf("cloning %s/%s: %w", owner, repo, err)
	}
	defer os.RemoveAll(tmpDir)

	targetPath := filepath.Join(tmpDir, filepath.FromSlash(path))
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("path %q not found in repo %s/%s: %w", path, owner, repo, err)
	}

	if info.IsDir() {
		// Skill mode: directory containing SKILL.md + reference .md files.
		name, description, body, err := buildSkillPrompt(targetPath)
		if err != nil {
			return fmt.Errorf("building skill prompt from %q: %w", path, err)
		}
		if nameOverride != "" {
			name = nameOverride
		}
		if name == "" {
			name = filepath.Base(path)
		}
		fmt.Printf("installing skill %s...\n", name)
		if err := agent.Write(ws, name, description, body, nil, "inherit", 50); err != nil {
			return fmt.Errorf("writing agent %s: %w", name, err)
		}
		fmt.Printf("installed agent %s\n", name)
		return nil
	}

	// Agent mode: single .md file — write content as-is.
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}
	fallback := strings.TrimSuffix(filepath.Base(path), ".md")
	return installAgentFromContent(ws, data, nameOverride, fallback)
}

// fetchFromURL downloads a single .md file via HTTP and installs it as an agent.
func fetchFromURL(ws *workspace.Workspace, url, nameOverride string) error {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s: %w", url, err)
	}
	// Derive fallback name from the last path segment of the URL.
	urlBase := url
	if idx := strings.LastIndex(urlBase, "/"); idx >= 0 {
		urlBase = urlBase[idx+1:]
	}
	urlBase = strings.TrimSuffix(urlBase, ".md")
	return installAgentFromContent(ws, data, nameOverride, urlBase)
}

// installAgentFromContent writes raw .md content to the workspace agents dir.
// Name resolution order: nameOverride → frontmatter name: → fallbackName.
func installAgentFromContent(ws *workspace.Workspace, content []byte, nameOverride, fallbackName string) error {
	name, _, _ := extractFrontmatter(content)
	if nameOverride != "" {
		name = nameOverride
	}
	if name == "" {
		name = strings.TrimSuffix(fallbackName, ".md")
	}
	if name == "" {
		return fmt.Errorf("could not determine agent name from content or source path")
	}
	fmt.Printf("installing agent %s...\n", name)
	agentPath := filepath.Join(ws.AgentsDir(), name+".md")
	if err := os.WriteFile(agentPath, content, 0644); err != nil {
		return fmt.Errorf("writing agent %s: %w", name, err)
	}
	fmt.Printf("installed agent %s\n", name)
	return nil
}

// parseGitHubSource parses a GitHub source string in these forms:
//   - owner/repo/path[@ref]
//   - github:owner/repo/path[@ref]
//
// Returns owner, repo, path, ref.  ref and path are empty when absent.
func parseGitHubSource(s string) (owner, repo, path, ref string, err error) {
	s = strings.TrimPrefix(s, "github:")

	// Split off @ref suffix.
	if idx := strings.Index(s, "@"); idx >= 0 {
		ref = s[idx+1:]
		s = s[:idx]
	}

	// s is now owner/repo[/path].
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		err = fmt.Errorf("expected owner/repo[/path], got %q", s)
		return
	}
	owner = parts[0]
	repo = parts[1]
	if len(parts) == 3 {
		path = parts[2]
	}
	if owner == "" || repo == "" {
		err = fmt.Errorf("owner and repo must not be empty in %q", s)
		return
	}
	return
}

// gitCloneTemp shallow-clones repoURL into a new OS temp directory and returns
// its path.  The caller must call os.RemoveAll on the returned path.
func gitCloneTemp(repoURL, ref string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "stint-fetch-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, tmpDir)
	cmd := exec.Command("git", args...)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("git clone: %w\n%s", runErr, string(out))
	}
	return tmpDir, nil
}

// extractFrontmatter parses optional YAML frontmatter from .md content.
// Returns (name, description, body) where body is everything after the --- block.
// If there is no frontmatter all content is treated as the body.
func extractFrontmatter(content []byte) (name, description, body string) {
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		body = strings.TrimSpace(s)
		return
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		body = strings.TrimSpace(s)
		return
	}
	frontmatter := rest[:end]
	body = strings.TrimSpace(rest[end+4:])
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
			name = val
		case "description":
			description = val
		}
	}
	return
}

// buildSkillPrompt walks a skill directory, collects only .md files, strips
// their frontmatter, and concatenates the bodies.  SKILL.md body comes first;
// remaining files are sorted alphabetically by relative path.
// Returns (name, description, concatenatedBody).
func buildSkillPrompt(dir string) (name, description, body string, err error) {
	var skillBody string
	var otherPaths []string

	walkErr := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".md" {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "SKILL.md" {
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return fmt.Errorf("reading SKILL.md: %w", readErr)
			}
			n, d, b := extractFrontmatter(data)
			if n != "" {
				name = n
			}
			if d != "" {
				description = d
			}
			skillBody = b
		} else {
			otherPaths = append(otherPaths, rel)
		}
		return nil
	})
	if walkErr != nil {
		err = walkErr
		return
	}

	sort.Strings(otherPaths)

	var parts []string
	if skillBody != "" {
		parts = append(parts, skillBody)
	}
	for _, rel := range otherPaths {
		data, readErr := os.ReadFile(filepath.Join(dir, rel))
		if readErr != nil {
			err = fmt.Errorf("reading %s: %w", rel, readErr)
			return
		}
		_, _, b := extractFrontmatter(data)
		if b != "" {
			parts = append(parts, b)
		}
	}

	body = strings.Join(parts, "\n\n")
	return
}
