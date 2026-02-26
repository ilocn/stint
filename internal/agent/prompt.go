package agent

import (
	"fmt"
	"strings"

	"github.com/ilocn/stint/internal/gitutil"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/workspace"
)

// BuildPrompt constructs the full prompt injected into the worker's claude session.
// It combines: agent system prompt + context from prior tasks + the task prompt + worker instructions.
func BuildPrompt(ws *workspace.Workspace, t *task.Task, def *Def) (string, error) {
	var b strings.Builder

	// 1. Agent system prompt.
	b.WriteString(def.SystemPrompt)
	b.WriteString("\n\n")

	// 2. Context from prior tasks.
	if len(t.ContextFrom) > 0 {
		b.WriteString("--- CONTEXT FROM PREVIOUS TASKS ---\n\n")
		for _, depID := range t.ContextFrom {
			dep, status, err := task.Get(ws, depID)
			if err != nil {
				continue // best-effort
			}
			b.WriteString(fmt.Sprintf("[Task: %s (%s)]\n", dep.Title, depID))
			if dep.Result != nil {
				b.WriteString(fmt.Sprintf("Status: %s\n", status))
				b.WriteString(fmt.Sprintf("Summary: %s\n", dep.Result.Summary))
				if dep.Result.Branch != "" && dep.GoalBranch != "" && t.Repo != "" {
					repoPath, ok := ws.Config.Repos[t.Repo]
					if ok && dep.Result.Branch != "" {
						// Use pre-computed diff when available (faster; avoids git call at spawn time).
						// Fall back to on-demand computation for tasks created before this feature.
						diff := dep.Result.Diff
						if diff == "" {
							diff, _ = gitutil.Diff(repoPath, dep.Result.Branch, dep.GoalBranch)
						}
						if diff != "" {
							b.WriteString("Git diff:\n")
							// Truncate very long diffs.
							if len(diff) > 8000 {
								diff = diff[:8000] + "\n... (truncated)"
							}
							b.WriteString(diff)
						}
					}
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("--- END CONTEXT ---\n\n")
	}

	// 3. The task-specific prompt.
	b.WriteString("--- YOUR TASK ---\n\n")
	b.WriteString(t.Prompt)
	b.WriteString("\n\n")

	// 4. Relay instructions.
	b.WriteString("--- WORKER INSTRUCTIONS ---\n\n")
	b.WriteString("You are running as a stint worker. Follow these instructions:\n\n")
	b.WriteString(fmt.Sprintf("- Every 2 minutes, run: st heartbeat %s\n", t.ID))
	b.WriteString(fmt.Sprintf("- When your work is complete, run: st task-done %s --summary \"<brief description of what you did>\"\n", t.ID))
	b.WriteString(fmt.Sprintf("- If you encounter an unrecoverable error, run: st task-fail %s --reason \"<explanation>\"\n", t.ID))
	b.WriteString("- Do NOT exit until you have run one of the above commands.\n")
	b.WriteString(fmt.Sprintf("- You are working in the git worktree at: %s\n", ws.WorktreePath(t.ID)))
	if t.Repo != "" {
		b.WriteString("- IMPORTANT: Before calling 'st task-done', you MUST commit all your changes to git:\n")
		b.WriteString("    git add -A && git commit -m \"<description of changes>\"\n")
		b.WriteString("  Uncommitted changes will be lost when the worktree is cleaned up. Do not skip this.\n")
	}
	if t.BranchName != "" {
		b.WriteString(fmt.Sprintf("- Your branch is: %s\n", t.BranchName))
	}
	if t.GoalBranch != "" {
		b.WriteString(fmt.Sprintf("- Goal integration branch: %s\n", t.GoalBranch))
	}

	return b.String(), nil
}

// BuildPlannerPrompt constructs the system prompt for the Planner.
func BuildPlannerPrompt(ws *workspace.Workspace, goalID, goalText string, hints []string) (string, error) {
	var b strings.Builder

	b.WriteString("You are a software planning agent. Your job is to decompose a Goal into Tasks and monitor their completion.\n\n")

	b.WriteString(fmt.Sprintf("## Goal (ID: %s)\n\n%s\n\n", goalID, goalText))

	if len(hints) > 0 {
		b.WriteString("## Hints (guidance, not rules)\n\n")
		for _, h := range hints {
			b.WriteString(fmt.Sprintf("- %s\n", h))
		}
		b.WriteString("\n")
	}

	// Available repos — include paths so planner knows what to pass to Explore subagents.
	if len(ws.Config.Repos) > 0 {
		b.WriteString("## Available Repos\n\n")
		for name, path := range ws.Config.Repos {
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", name, path))
		}
		b.WriteString("\n")
	}

	// Available agents.
	defs, err := List(ws)
	if err == nil && len(defs) > 0 {
		b.WriteString("## Available Agents\n\n")
		for _, d := range defs {
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", d.Name, d.Description))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Your Tools\n\n")
	b.WriteString(fmt.Sprintf("- `st task add --goal %s --title \"..\" --prompt \"..\" --repo <name> [--agent <name>] [--deps <id>,...] [--context-from <id>,...]`\n", goalID))
	b.WriteString(fmt.Sprintf("- `st task list --goal %s` — see current task state\n", goalID))
	b.WriteString(fmt.Sprintf("- `st status --goal %s` — overall goal progress\n", goalID))
	b.WriteString("- `st task retry <id>` — retry a failed task\n")
	b.WriteString("- Standard tools: Bash, Read, Glob, Grep to explore repos\n")
	b.WriteString("- Task tool: launch Explore subagents for parallel codebase exploration\n\n")

	b.WriteString("## Two-Level Merge Architecture\n\n")
	b.WriteString("Tasks run in isolated branches (st/tasks/t-xxx). To make their work visible to downstream tasks,\n")
	b.WriteString("each task must be followed by a **merge-task** step that integrates the branch into the goal branch\n")
	b.WriteString("(st/goals/g-xxx). Downstream tasks start from the goal branch, so they automatically pick up all\n")
	b.WriteString("prior integrated work. The final step uses the **merge** agent (goal-branch → main).\n\n")
	b.WriteString("Rules for inserting merge-task steps:\n")
	b.WriteString("1. After EVERY task that modifies the repo, insert a merge-task step:\n")
	b.WriteString("   - prompt: \"Merge task <task-id> into the goal branch. Run 'st task show <task-id>' to get the source branch.\"\n")
	b.WriteString("   - --deps <task-id>  (wait for the task to complete)\n")
	b.WriteString("   - --context-from <task-id>  (for reference)\n")
	b.WriteString("   - --agent merge-task\n")
	b.WriteString("   - --repo <same-repo>\n")
	b.WriteString("2. Downstream tasks depend on the merge-task, not the original task:\n")
	b.WriteString("   impl → merge-task-impl → test → merge-task-test → review → merge-task-review → merge\n")
	b.WriteString("3. For parallel tasks touching the same repo, serialize their merge-tasks to avoid goal-branch conflicts:\n")
	b.WriteString("   impl-A → merge-task-A \\                (merge-task-B also --deps merge-task-A)\n")
	b.WriteString("   impl-B → merge-task-B → test → ...\n")
	b.WriteString("4. Tasks across different repos can run fully in parallel (no deps between them)\n\n")

	b.WriteString("## Phase 1: Parallel Codebase Exploration\n\n")
	b.WriteString("Before creating any tasks, you MUST explore the repos thoroughly.\n")
	b.WriteString("Use the Task tool to launch Explore subagents IN PARALLEL (one per repo, all in a single message).\n")
	b.WriteString("Each subagent should understand: directory layout, key modules/packages, existing patterns and\n")
	b.WriteString("conventions, test structure, and anything relevant to the goal.\n\n")
	b.WriteString("For each repo, use:\n")
	b.WriteString("  subagent_type: \"Explore\"\n")
	b.WriteString(fmt.Sprintf("  prompt: \"Explore <repo_path>. Understand: directory layout, key modules/packages, existing patterns and conventions, test structure. Focus on what's relevant to: %s. Return a concise summary of findings.\"\n\n", goalText))
	if len(ws.Config.Repos) > 0 {
		b.WriteString("Repos to explore (name → path):\n")
		for name, path := range ws.Config.Repos {
			b.WriteString(fmt.Sprintf("- %s → %s\n", name, path))
		}
		b.WriteString("\n")
	}
	b.WriteString("Wait for ALL Explore results before proceeding to Phase 2.\n\n")

	b.WriteString("## Phase 2: Task Decomposition\n\n")
	b.WriteString("Based on your exploration findings, decompose the goal into tasks:\n")
	b.WriteString("1. Use `st task add` to create tasks. Use dep_task_ids for ordering. Use context_from so later agents see earlier agents' work.\n")
	b.WriteString("2. Typical chain: impl → merge-task → test → merge-task → review → merge-task → merge\n")
	b.WriteString("3. Independent sub-goals across different repos can run in parallel (no deps between them)\n")
	b.WriteString("4. Poll `st status` every few minutes to monitor progress\n")
	b.WriteString("5. On failure: retry or create a remediation task\n")
	b.WriteString("6. When all tasks are done: summarize the work and exit\n")

	return b.String(), nil
}

// BuildRemediationPlannerPrompt constructs the prompt for a remediation planner.
// It is spawned when a review task fails (verdict="fail") and instructs the planner
// to create a focused fix cycle and exit immediately (single-pass, no monitoring).
func BuildRemediationPlannerPrompt(ws *workspace.Workspace, goalID, remediationContext string) (string, error) {
	var b strings.Builder

	b.WriteString("You are a software remediation planning agent. A code review has failed and you must create fix tasks to address the identified issues.\n\n")

	b.WriteString("## Remediation Context\n\n")
	b.WriteString(remediationContext)
	b.WriteString("\n\n")

	// Available repos.
	if len(ws.Config.Repos) > 0 {
		b.WriteString("## Available Repos\n\n")
		for name, path := range ws.Config.Repos {
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", name, path))
		}
		b.WriteString("\n")
	}

	// Available agents.
	defs, err := List(ws)
	if err == nil && len(defs) > 0 {
		b.WriteString("## Available Agents\n\n")
		for _, d := range defs {
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", d.Name, d.Description))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Your Tools\n\n")
	b.WriteString(fmt.Sprintf("- `st task add --goal %s --title \"..\" --prompt \"..\" --repo <name> [--agent <name>] [--deps <id>,...] [--context-from <id>,...]`\n", goalID))
	b.WriteString(fmt.Sprintf("- `st task list --goal %s` — see current task state\n", goalID))
	b.WriteString(fmt.Sprintf("- `st status --goal %s` — overall goal progress\n", goalID))
	b.WriteString("\n")

	b.WriteString("## Instructions\n\n")
	b.WriteString("1. Create specific fix tasks targeting the identified issues.\n")
	b.WriteString("2. Use `--context-from <review-task-id>` on fix tasks so agents see the review findings.\n")
	b.WriteString("3. Follow the fix cycle: fix-impl → merge-task → re-review → merge-task.\n")
	b.WriteString("4. Each task that modifies the repo MUST be followed by a merge-task step.\n")
	b.WriteString("5. CRITICAL: Exit IMMEDIATELY after creating the fix tasks. Do NOT monitor progress.\n")
	b.WriteString("6. Do NOT run `st status` in a loop. Create the tasks and exit.\n")
	b.WriteString("7. Do NOT re-spawn another planner. Your job is only to create fix tasks.\n")

	return b.String(), nil
}
