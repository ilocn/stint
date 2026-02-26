package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"
	"github.com/ilocn/stint/internal/agent"
	"github.com/ilocn/stint/internal/goal"
	"github.com/ilocn/stint/internal/logger"
	"github.com/ilocn/stint/internal/recovery"
	"github.com/ilocn/stint/internal/supervisor"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/worker"
	"github.com/ilocn/stint/internal/workspace"
)

var version = "dev" // injected via ldflags at build time

// Globals holds shared state injected into Run methods that need a workspace.
type Globals struct {
	once sync.Once
	ws   *workspace.Workspace
}

// WS lazily opens the workspace on first call.
// Commands that don't need a workspace (init, version) must not call this.
func (g *Globals) WS() *workspace.Workspace {
	g.once.Do(func() {
		g.ws = openWS()
	})
	return g.ws
}

// ─── Top-level CLI struct ────────────────────────────────────────────────────

type CLI struct {
	Init       InitCmd          `cmd:"" group:"workspace" help:"Create a new workspace."`
	Repo       RepoCmd          `cmd:"" group:"workspace" help:"Manage repos (add/clone/delete/list)."`
	Goal       GoalCmd          `cmd:"" group:"goals"     help:"Manage goals (add/list/show/cancel)."`
	Plan       PlanCmd          `cmd:"" group:"execution" help:"Interactive planner (foreground)."`
	Run        RunCmd           `cmd:"" group:"execution" help:"Plan and execute a goal end-to-end."`
	Supervisor SupervisorCmd    `cmd:"" group:"execution" help:"Run dispatch loop with live status."`
	Task       TaskCmd          `cmd:"" group:"tasks"     help:"Manage tasks (add/list/show/retry/skip/cancel)."`
	Agent      AgentCmd         `cmd:"" group:"agents"    help:"Manage agents (create/fetch/remove/list/show)."`
	Status     StatusCmd        `cmd:"" group:"observe"   help:"Show overall goal and task status."`
	Log        LogCmd           `cmd:"" group:"observe"   help:"Print task log."`
	Worker     WorkerListTopCmd `cmd:"" group:"observe"   help:"List active workers with heartbeat status."`
	Recover    RecoverCmd       `cmd:"" group:"maint"     help:"Post-crash reset: reclaim stale running tasks."`
	Version    VersionCmd       `cmd:"" group:"maint"     help:"Print version and platform info."`
	Clean      CleanCmd         `cmd:"" group:"maint"     help:"Remove goals, tasks, logs, heartbeats, and worktrees."`
	Heartbeat  HeartbeatCmd     `cmd:"" group:"protocol"  help:"Record a worker heartbeat. (Called by worker agents.)"`
	TaskDone   TaskDoneCmd      `cmd:"task-done"  group:"protocol"  help:"Mark a task complete. (Called by worker agents.)"`
	TaskFail   TaskFailCmd      `cmd:"task-fail"  group:"protocol"  help:"Mark a task failed. (Called by worker agents.)"`
}

// ─── init ────────────────────────────────────────────────────────────────────

type InitCmd struct {
	Dir  string   `arg:"" help:"Directory to initialize."`
	Repo []string `name:"repo" help:"Add a repo as name=path (repeatable)."`
}

func (c *InitCmd) Run() error {
	repos := make(map[string]string)
	for _, r := range c.Repo {
		parts := strings.SplitN(r, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("--repo must be name=path, got: %s", r)
		}
		abs, err := filepath.Abs(parts[1])
		if err != nil {
			return fmt.Errorf("invalid path: %v", err)
		}
		repos[parts[0]] = abs
	}

	ws, err := workspace.Init(c.Dir, repos)
	if err != nil {
		return fmt.Errorf("init failed: %v", err)
	}
	fmt.Printf("initialized stint workspace at %s\n", ws.Root)
	fmt.Printf("built-in agents: %s\n", strings.Join(agent.EmbeddedNames(), ", "))
	if len(repos) > 0 {
		for name, path := range ws.Config.Repos {
			fmt.Printf("repo: %s → %s\n", name, path)
		}
	} else {
		fmt.Println("tip: add repos with: st repo add <name> <path>")
	}
	return nil
}

// ─── repo ────────────────────────────────────────────────────────────────────

type RepoCmd struct {
	Add    RepoAddCmd    `cmd:"" help:"Copy/clone a local repo into the workspace."`
	Clone  RepoCloneCmd  `cmd:"" help:"Clone a remote repo and register it."`
	Delete RepoDeleteCmd `cmd:"" help:"Remove a repo from workspace config."`
	List   RepoListCmd   `cmd:"" help:"List configured repos."`
}

type RepoAddCmd struct {
	Name string `arg:"" help:"Name for the repo."`
	Path string `arg:"" help:"Local path to copy/clone."`
}

func (c *RepoAddCmd) Run(g *Globals) error {
	ws := g.WS()
	abs, err := filepath.Abs(c.Path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("path does not exist: %s", abs)
	}
	dest := filepath.Join(ws.Root, c.Name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination already exists: %s\nremove it first or choose a different name", dest)
	}
	if ws.Config.Repos == nil {
		ws.Config.Repos = make(map[string]string)
	}
	isGit := false
	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		isGit = true
	}
	if isGit {
		cmd := exec.Command("git", "clone", abs, dest)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git clone failed: %v", err)
		}
		fmt.Printf("cloned %s → %s\n", abs, dest)
	} else {
		if err := copyDirRecursive(abs, dest); err != nil {
			return fmt.Errorf("copying directory: %v", err)
		}
		fmt.Printf("copied %s → %s\n", abs, dest)
	}
	ws.Config.Repos[c.Name] = dest
	if err := ws.SaveConfig(); err != nil {
		return fmt.Errorf("saving config: %v", err)
	}
	return nil
}

type RepoCloneCmd struct {
	Name string `arg:"" help:"Name for the repo."`
	URL  string `arg:"" help:"Remote URL."`
	Dir  string `arg:"" optional:"" help:"Clone directory (default: <workspace>/<name>)."`
}

func (c *RepoCloneCmd) Run(g *Globals) error {
	ws := g.WS()
	cloneDir := filepath.Join(ws.Root, c.Name)
	if c.Dir != "" {
		var err error
		cloneDir, err = filepath.Abs(c.Dir)
		if err != nil {
			return err
		}
	}
	fmt.Printf("cloning %s into %s...\n", c.URL, cloneDir)
	cmd := exec.Command("git", "clone", c.URL, cloneDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %v", err)
	}
	if ws.Config.Repos == nil {
		ws.Config.Repos = make(map[string]string)
	}
	ws.Config.Repos[c.Name] = cloneDir
	if err := ws.SaveConfig(); err != nil {
		return fmt.Errorf("saving config: %v", err)
	}
	fmt.Printf("added repo %s → %s\n", c.Name, cloneDir)
	return nil
}

type RepoDeleteCmd struct {
	Name string `arg:"" help:"Name of the repo to delete."`
}

func (c *RepoDeleteCmd) Run(g *Globals) error {
	ws := g.WS()
	if ws.Config.Repos == nil {
		return fmt.Errorf("repo not found: %s", c.Name)
	}
	repoPath, ok := ws.Config.Repos[c.Name]
	if !ok {
		return fmt.Errorf("repo not found: %s", c.Name)
	}
	delete(ws.Config.Repos, c.Name)
	if err := ws.SaveConfig(); err != nil {
		return fmt.Errorf("saving config: %v", err)
	}
	if rel, err := filepath.Rel(ws.Root, repoPath); err == nil && !strings.HasPrefix(rel, "..") {
		if err := os.RemoveAll(repoPath); err != nil {
			slog.Warn("could not remove repo", slog.String("path", repoPath), slog.Any("error", err))
		}
	}
	fmt.Printf("deleted repo %s\n", c.Name)
	return nil
}

type RepoListCmd struct{}

func (c *RepoListCmd) Run(g *Globals) error {
	ws := g.WS()
	if len(ws.Config.Repos) == 0 {
		fmt.Println("no repos configured")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH")
	names := sortedKeys(ws.Config.Repos)
	for _, name := range names {
		fmt.Fprintf(w, "%s\t%s\n", name, ws.Config.Repos[name])
	}
	w.Flush()
	return nil
}

// ─── goal ────────────────────────────────────────────────────────────────────

type GoalCmd struct {
	Add    GoalAddCmd    `cmd:"" help:"Queue a new goal."`
	List   GoalListCmd   `cmd:"" help:"List all goals."`
	Show   GoalShowCmd   `cmd:"" help:"Show goal details."`
	Cancel GoalCancelCmd `cmd:"" help:"Cancel a goal."`
}

type GoalAddCmd struct {
	Text  string   `arg:"" help:"Goal text."`
	Hints []string `name:"hint" help:"Soft guidance hint (repeatable)."`
	Repos string   `name:"repos" help:"Comma-separated repo names this goal touches."`
}

func (c *GoalAddCmd) Run(g *Globals) error {
	ws := g.WS()
	var repos []string
	if c.Repos != "" {
		for _, r := range strings.Split(c.Repos, ",") {
			if r = strings.TrimSpace(r); r != "" {
				repos = append(repos, r)
			}
		}
	}
	gObj, err := goal.Create(ws, c.Text, c.Hints, repos)
	if errors.Is(err, goal.ErrDuplicate) {
		fmt.Printf("skipped: goal already exists %s: %s\n", gObj.ID, gObj.Text)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating goal: %v", err)
	}
	fmt.Printf("queued goal %s: %s\n", gObj.ID, gObj.Text)
	if !supervisor.IsSupervisorRunning(ws) {
		fmt.Println("start the supervisor to process it: st supervisor")
	}
	return nil
}

type GoalListCmd struct{}

func (c *GoalListCmd) Run(g *Globals) error {
	ws := g.WS()
	goals, err := goal.List(ws)
	if err != nil {
		return err
	}
	if len(goals) == 0 {
		fmt.Println("no goals")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tTEXT")
	for _, gItem := range goals {
		text := gItem.Text
		if len(text) > 60 {
			text = text[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", gItem.ID, gItem.Status, text)
	}
	w.Flush()
	return nil
}

type GoalShowCmd struct {
	ID string `arg:"" help:"Goal ID."`
}

func (c *GoalShowCmd) Run(g *Globals) error {
	ws := g.WS()
	gObj, err := goal.Get(ws, c.ID)
	if err != nil {
		return err
	}
	fmt.Printf("ID:      %s\n", gObj.ID)
	fmt.Printf("Status:  %s\n", gObj.Status)
	fmt.Printf("Text:    %s\n", gObj.Text)
	if len(gObj.Hints) > 0 {
		fmt.Printf("Hints:   %s\n", strings.Join(gObj.Hints, "; "))
	}
	if len(gObj.Repos) > 0 {
		fmt.Printf("Repos:   %s\n", strings.Join(gObj.Repos, ", "))
	}
	if gObj.Branch != "" {
		fmt.Printf("Branch:  %s\n", gObj.Branch)
	}
	fmt.Printf("Created: %s\n", fmtTime(gObj.CreatedAt))
	return nil
}

type GoalCancelCmd struct {
	ID string `arg:"" help:"Goal ID."`
}

func (c *GoalCancelCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := goal.UpdateStatus(ws, c.ID, "failed"); err != nil {
		return err
	}
	fmt.Printf("cancelled goal %s\n", c.ID)
	return nil
}

// ─── plan ────────────────────────────────────────────────────────────────────

type PlanCmd struct {
	Text   string   `arg:"" optional:"" help:"Goal text."`
	Hints  []string `name:"hint" help:"Soft guidance hint (repeatable)."`
	Resume string   `help:"Resume an existing goal by ID."`
}

func (c *PlanCmd) Run(g *Globals) error {
	if c.Resume == "" && c.Text == "" {
		return fmt.Errorf("usage: st plan \"<goal>\" [--hint ...]")
	}
	ws := g.WS()

	var goalID, goalText string
	var goalHints []string

	if c.Resume != "" {
		gObj, err := goal.Get(ws, c.Resume)
		if err != nil {
			return err
		}
		goalID = gObj.ID
		goalText = gObj.Text
		goalHints = gObj.Hints
		if err := goal.UpdateStatus(ws, goalID, goal.StatusPlanning); err != nil {
			return fmt.Errorf("updating goal status: %v", err)
		}
	} else {
		gObj, err := goal.Create(ws, c.Text, c.Hints, nil)
		if err != nil {
			return fmt.Errorf("creating goal: %v", err)
		}
		if err := goal.UpdateStatus(ws, gObj.ID, goal.StatusPlanning); err != nil {
			return fmt.Errorf("updating goal status: %v", err)
		}
		goalID = gObj.ID
		goalText = gObj.Text
		goalHints = gObj.Hints
	}

	fmt.Printf("planning goal %s: %s\n", goalID, goalText)
	fmt.Println("(running interactive planner — start supervisor in another terminal)")
	fmt.Println()

	planErr := agent.SpawnPlannerInteractive(ws, goalID, goalText, goalHints)
	return updateGoalStatusAfterPlanner(ws, goalID, planErr)
}

// updateGoalStatusAfterPlanner mirrors supervisor's handlePlannerSuccess /
// handlePlannerError logic: it transitions the goal to the correct terminal or
// execution state based on whether the planner errored and how many tasks it
// created.
//
//   - success + tasks  → StatusActive  (supervisor will dispatch them)
//   - success + no tasks → StatusDone  (nothing to do)
//   - error   + tasks  → StatusActive  (partial work can still run) + return error
//   - error   + no tasks → StatusFailed + return error
func updateGoalStatusAfterPlanner(ws *workspace.Workspace, goalID string, planErr error) error {
	tasks, listErr := task.ListForGoal(ws, goalID)
	tasksExist := listErr == nil && len(tasks) > 0

	if planErr == nil {
		if tasksExist {
			fmt.Printf("planner created %d task(s); transitioning goal to active\n", len(tasks))
			if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
				slog.Warn("failed to mark goal active", slog.Any("error", err))
			}
		} else {
			fmt.Println("planner created no tasks; marking goal done")
			if err := goal.UpdateStatus(ws, goalID, goal.StatusDone); err != nil {
				slog.Warn("failed to mark goal done", slog.Any("error", err))
			}
		}
		return nil
	}

	if tasksExist {
		fmt.Printf("planner exited with error but created %d task(s); transitioning goal to active\n", len(tasks))
		if err := goal.UpdateStatus(ws, goalID, goal.StatusActive); err != nil {
			slog.Warn("failed to mark goal active", slog.Any("error", err))
		}
	} else {
		if err := goal.UpdateStatus(ws, goalID, goal.StatusFailed); err != nil {
			slog.Warn("failed to mark goal failed", slog.Any("error", err))
		}
	}
	return fmt.Errorf("planner exited with error: %v", planErr)
}

// ─── run ─────────────────────────────────────────────────────────────────────

type RunCmd struct {
	Text          string   `arg:"" help:"Goal text."`
	Hints         []string `name:"hint" help:"Soft guidance hint (repeatable)."`
	Repos         string   `name:"repos" help:"Comma-separated repo names."`
	MaxConcurrent int      `default:"4" name:"max-concurrent" help:"Max parallel workers (default 4)."`
	Port          int      `default:"8080" help:"Web dashboard port (default 8080; 0 = disabled)."`
	RandomPort    bool     `name:"random-port" help:"Use a random free OS port for web dashboard."`
}

func (c *RunCmd) Run(g *Globals) error {
	port := c.Port
	if c.RandomPort {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			return fmt.Errorf("finding free port: %v", err)
		}
		port = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
	}

	ws := g.WS()

	var repos []string
	if c.Repos != "" {
		for _, r := range strings.Split(c.Repos, ",") {
			if r = strings.TrimSpace(r); r != "" {
				repos = append(repos, r)
			}
		}
	}

	gObj, err := goal.Create(ws, c.Text, c.Hints, repos)
	if err != nil {
		return fmt.Errorf("creating goal: %v", err)
	}
	fmt.Printf("goal %s: %s\n", gObj.ID, gObj.Text)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := supervisor.RunGoal(ctx, ws, gObj.ID, c.MaxConcurrent, port); err != nil {
		return err
	}
	return nil
}

// ─── supervisor ──────────────────────────────────────────────────────────────

type SupervisorCmd struct {
	MaxConcurrent int  `default:"4" name:"max-concurrent" help:"Max parallel workers (default 4)."`
	Port          int  `default:"8080" help:"Web dashboard port (default 8080; 0 = disabled)."`
	RandomPort    bool `name:"random-port" help:"Use a random free OS port for web dashboard."`
}

func (c *SupervisorCmd) Run(g *Globals) error {
	port := c.Port
	if c.RandomPort {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			return fmt.Errorf("finding free port: %v", err)
		}
		port = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
	}

	ws := g.WS()

	if supervisor.IsSupervisorRunning(ws) {
		slog.Warn("supervisor appears to be already running, continuing anyway")
	}

	// Write PID file so other commands can detect a running supervisor.
	pidPath := ws.SupervisorPIDPath()
	myPID := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(pidPath, []byte(myPID), 0644); err != nil {
		slog.Warn("failed to write supervisor pid file", slog.Any("error", err))
	}
	defer func() {
		// Only remove the PID file if it still contains our own PID.
		if current, err := os.ReadFile(pidPath); err == nil &&
			strings.TrimSpace(string(current)) == myPID {
			os.Remove(pidPath)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Println("stint supervisor running. ctrl+c to exit.")

	if err := supervisor.Run(ctx, ws, c.MaxConcurrent, port); err != nil {
		return fmt.Errorf("supervisor: %v", err)
	}
	return nil
}

// ─── recover ─────────────────────────────────────────────────────────────────

type RecoverCmd struct{}

func (c *RecoverCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := recovery.Recover(ws, recovery.DefaultStaleThresholdSecs); err != nil {
		return fmt.Errorf("recover: %v", err)
	}
	fmt.Println("recovery complete")
	return nil
}

// ─── task ────────────────────────────────────────────────────────────────────

type TaskCmd struct {
	Add         TaskAddCmd         `cmd:"" help:"Create a task (used by planners and humans)."`
	List        TaskListCmd        `cmd:"" help:"List tasks."`
	Show        TaskShowCmd        `cmd:"" help:"Show full task details."`
	Retry       TaskRetryCmd       `cmd:"" help:"Reset a failed task to pending."`
	RetryFailed TaskRetryFailedCmd `cmd:"retry-failed" help:"Bulk-retry all failed tasks (optionally scoped to a goal)."`
	Skip        TaskSkipCmd        `cmd:"" help:"Skip a failed or blocked task (marks it done)."`
	Cancel      TaskCancelCmd      `cmd:"" help:"Cancel a task."`
}

type TaskAddCmd struct {
	GoalID      string `required:"" name:"goal"         help:"Goal ID (required)."`
	Title       string `required:""                      help:"Short title (required)."`
	Prompt      string `required:""                      help:"Instruction for the agent (required)."`
	Repo        string `                                  help:"Repo name."`
	Agent       string `default:"impl"                    help:"Agent to use (default: impl; available: impl, debug, test, docs, review, security, merge, merge-task)."`
	Deps        string `name:"deps"                       help:"Comma-separated dependency task IDs."`
	ContextFrom string `name:"context-from"               help:"Task IDs whose output to include as context."`
	GoalBranch  string `name:"goal-branch"                help:"Goal integration branch."`
	Branch      string `                                  help:"Task branch name (auto-generated if empty)."`
}

func (c *TaskAddCmd) Run(g *Globals) error {
	ws := g.WS()

	var deps, contextFrom []string
	if c.Deps != "" {
		for _, d := range strings.Split(c.Deps, ",") {
			if d = strings.TrimSpace(d); d != "" {
				deps = append(deps, d)
			}
		}
	}
	if c.ContextFrom != "" {
		for _, cf := range strings.Split(c.ContextFrom, ",") {
			if cf = strings.TrimSpace(cf); cf != "" {
				contextFrom = append(contextFrom, cf)
			}
		}
	}

	seq, _ := task.NextSeq(ws, c.GoalID)

	branchName := c.Branch
	goalBranch := c.GoalBranch
	if branchName == "" && c.Repo != "" {
		branchName = "st/tasks/" + task.NewID()
	}
	if goalBranch == "" && c.Repo != "" {
		goalBranch = "st/goals/" + c.GoalID
	}

	t := &task.Task{
		ID:          task.NewID(),
		GoalID:      c.GoalID,
		Seq:         seq,
		Title:       c.Title,
		Agent:       c.Agent,
		Prompt:      c.Prompt,
		Repo:        c.Repo,
		GoalBranch:  goalBranch,
		BranchName:  branchName,
		ContextFrom: contextFrom,
		DepTaskIDs:  deps,
	}

	if err := task.Create(ws, t); err != nil {
		return fmt.Errorf("creating task: %v", err)
	}
	fmt.Printf("created task %s: %s\n", t.ID, t.Title)
	if len(deps) > 0 {
		fmt.Printf("  deps: %s\n", strings.Join(deps, ", "))
	}
	return nil
}

type TaskListCmd struct {
	GoalID string `name:"goal"   help:"Filter by goal ID."`
	Status string `name:"status" help:"Filter by status."`
}

func (c *TaskListCmd) Run(g *Globals) error {
	ws := g.WS()

	var (
		tasks []*task.TaskWithStatus
		err   error
	)
	if c.GoalID != "" {
		tasks, err = task.ListForGoal(ws, c.GoalID)
	} else if c.Status != "" {
		ts, e := task.ListByStatus(ws, c.Status)
		if e != nil {
			return e
		}
		for _, t := range ts {
			tasks = append(tasks, &task.TaskWithStatus{Task: t, Status: c.Status})
		}
	} else {
		tasks, err = task.ListAll(ws)
	}
	if err != nil {
		return err
	}

	if len(tasks) == 0 {
		fmt.Println("no tasks")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tREPO\tTITLE")
	for _, ts := range tasks {
		t := ts.Task
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, ts.Status, t.Agent, t.Repo, t.Title)
	}
	w.Flush()
	return nil
}

type TaskShowCmd struct {
	ID string `arg:"" help:"Task ID."`
}

func (c *TaskShowCmd) Run(g *Globals) error {
	ws := g.WS()
	t, status, err := task.Get(ws, c.ID)
	if err != nil {
		return err
	}
	fmt.Printf("ID:      %s\n", t.ID)
	fmt.Printf("Status:  %s\n", status)
	fmt.Printf("Goal:    %s\n", t.GoalID)
	fmt.Printf("Seq:     %d\n", t.Seq)
	fmt.Printf("Title:   %s\n", t.Title)
	fmt.Printf("Agent:   %s\n", t.Agent)
	if t.Repo != "" {
		fmt.Printf("Repo:    %s\n", t.Repo)
	}
	if t.BranchName != "" {
		fmt.Printf("Branch:  %s\n", t.BranchName)
	}
	if len(t.DepTaskIDs) > 0 {
		fmt.Printf("Deps:    %s\n", strings.Join(t.DepTaskIDs, ", "))
	}
	if len(t.ContextFrom) > 0 {
		fmt.Printf("Context: %s\n", strings.Join(t.ContextFrom, ", "))
	}
	fmt.Printf("Created: %s\n", fmtTime(t.CreatedAt))
	if t.Result != nil {
		fmt.Printf("Summary: %s\n", t.Result.Summary)
	}
	if t.ErrorMsg != "" {
		fmt.Printf("Error:   %s\n", t.ErrorMsg)
	}
	fmt.Printf("\nPrompt:\n%s\n", t.Prompt)
	return nil
}

type TaskRetryCmd struct {
	ID string `arg:"" help:"Task ID."`
}

func (c *TaskRetryCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.Retry(ws, c.ID); err != nil {
		return err
	}
	fmt.Printf("task %s reset to pending\n", c.ID)
	return nil
}

// TaskRetryFailedCmd bulk-retries all failed tasks, optionally scoped to a goal.
type TaskRetryFailedCmd struct {
	GoalID string `name:"goal" help:"Scope to a specific goal ID (optional)."`
}

func (c *TaskRetryFailedCmd) Run(g *Globals) error {
	ws := g.WS()

	failed, err := task.ListByStatus(ws, task.StatusFailed)
	if err != nil {
		return fmt.Errorf("listing failed tasks: %v", err)
	}

	if c.GoalID != "" {
		var filtered []*task.Task
		for _, t := range failed {
			if t.GoalID == c.GoalID {
				filtered = append(filtered, t)
			}
		}
		failed = filtered
	}

	if len(failed) == 0 {
		if c.GoalID != "" {
			fmt.Printf("no failed tasks for goal %s\n", c.GoalID)
		} else {
			fmt.Println("no failed tasks")
		}
		return nil
	}

	retried := 0
	for _, t := range failed {
		if err := task.Retry(ws, t.ID); err != nil {
			slog.Warn("retry failed", slog.String("task_id", t.ID), slog.Any("error", err))
			continue
		}
		retried++
	}

	if c.GoalID != "" {
		fmt.Printf("retried %d failed task(s) for goal %s\n", retried, c.GoalID)
	} else {
		fmt.Printf("retried %d failed task(s)\n", retried)
	}
	return nil
}

type TaskSkipCmd struct {
	ID     string `arg:"" help:"Task ID."`
	Reason string `help:"Reason for skipping." default:"manually skipped"`
}

func (c *TaskSkipCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.Skip(ws, c.ID, c.Reason); err != nil {
		return err
	}
	fmt.Printf("task %s skipped\n", c.ID)
	return nil
}

type TaskCancelCmd struct {
	ID string `arg:"" help:"Task ID."`
}

func (c *TaskCancelCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.Cancel(ws, c.ID); err != nil {
		return err
	}
	fmt.Printf("task %s cancelled\n", c.ID)
	return nil
}

// ─── worker protocol commands (called by worker agents, not humans) ───────────

type TaskDoneCmd struct {
	ID      string `arg:"" help:"Task ID."`
	Summary string `required:"" help:"Brief description of what was done (required)."`
	Branch  string `help:"Branch name (optional)."`
}

func (c *TaskDoneCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.Done(ws, c.ID, c.Summary, c.Branch); err != nil {
		return err
	}
	fmt.Printf("task %s marked done\n", c.ID)
	return nil
}

type TaskFailCmd struct {
	ID     string `arg:"" help:"Task ID."`
	Reason string `required:"" help:"Explanation (required)."`
}

func (c *TaskFailCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.Fail(ws, c.ID, c.Reason); err != nil {
		return err
	}
	fmt.Printf("task %s marked failed\n", c.ID)
	return nil
}

// ─── heartbeat ───────────────────────────────────────────────────────────────

type HeartbeatCmd struct {
	ID string `arg:"" help:"Task ID."`
}

func (c *HeartbeatCmd) Run(g *Globals) error {
	ws := g.WS()
	if err := task.WriteHeartbeat(ws, c.ID); err != nil {
		return fmt.Errorf("writing heartbeat: %v", err)
	}
	return nil
}

// ─── agent ───────────────────────────────────────────────────────────────────

type AgentCmd struct {
	Create AgentCreateCmd `cmd:"" help:"Create a local agent from a prompt."`
	Fetch  AgentFetchCmd  `cmd:"" help:"Fetch an agent or skill from GitHub or a URL."`
	Remove AgentRemoveCmd `cmd:"" help:"Remove an installed agent."`
	List   AgentListCmd   `cmd:"" help:"List all agents."`
	Show   AgentShowCmd   `cmd:"" help:"Show agent configuration."`
}

// AgentCreateCmd creates a local agent from a prompt.
type AgentCreateCmd struct {
	Name        string `arg:"" help:"Agent name."`
	Prompt      string `required:"" help:"System prompt (required)."`
	Tools       string `help:"Comma-separated tool list."`
	Model       string `default:"sonnet" help:"Model to use (default: sonnet)."`
	MaxTurns    int    `default:"50" name:"max-turns" help:"Max turns (default: 50)."`
	Description string `help:"Agent description."`
}

func (c *AgentCreateCmd) Run(g *Globals) error {
	ws := g.WS()
	var tools []string
	if c.Tools != "" {
		for _, t := range strings.Split(c.Tools, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tools = append(tools, t)
			}
		}
	}
	if err := agent.Write(ws, c.Name, c.Description, c.Prompt, tools, c.Model, c.MaxTurns); err != nil {
		return fmt.Errorf("creating agent: %v", err)
	}
	fmt.Printf("created agent %s\n", c.Name)
	return nil
}

// AgentFetchCmd downloads an agent or skill from GitHub or a direct URL.
type AgentFetchCmd struct {
	Source string `arg:"" help:"GitHub path (owner/repo/path[@ref]) or https:// URL."`
	Name   string `help:"Override installed name (default: from frontmatter or filename)."`
}

func (c *AgentFetchCmd) Run(g *Globals) error {
	return fetchAndInstall(g.WS(), c.Source, c.Name)
}

// AgentRemoveCmd deletes an installed agent file.
type AgentRemoveCmd struct {
	Name string `arg:"" help:"Agent name."`
}

func (c *AgentRemoveCmd) Run(g *Globals) error {
	ws := g.WS()
	path := filepath.Join(ws.AgentsDir(), c.Name+".md")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("agent not found: %s", c.Name)
		}
		return err
	}
	fmt.Printf("removed agent %s\n", c.Name)
	return nil
}

type AgentListCmd struct{}

func (c *AgentListCmd) Run(g *Globals) error {
	ws := g.WS()
	defs, err := agent.List(ws)
	if err != nil {
		return err
	}
	if len(defs) == 0 {
		fmt.Println("no agents")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMODEL\tMAX-TURNS\tDESCRIPTION")
	for _, d := range defs {
		desc := d.Description
		if len(desc) > 55 {
			desc = desc[:52] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", d.Name, d.Model, d.MaxTurns, desc)
	}
	w.Flush()
	return nil
}

type AgentShowCmd struct {
	Name string `arg:"" help:"Agent name."`
}

func (c *AgentShowCmd) Run(g *Globals) error {
	ws := g.WS()
	// Check existence explicitly: agent.Get silently falls back to impl for
	// unknown names (a feature for worker dispatch), but AgentShow should
	// surface a clear error when the caller names a non-existent agent.
	// First check workspace file, then embedded binary, then give up.
	agentPath := filepath.Join(ws.AgentsDir(), c.Name+".md")
	_, wsErr := os.Stat(agentPath)
	_, embErr := agent.EmbeddedContent(c.Name)
	if os.IsNotExist(wsErr) && embErr != nil {
		return fmt.Errorf("agent not found: %s", c.Name)
	}
	def, err := agent.Get(ws, c.Name)
	if err != nil {
		return err
	}
	fmt.Printf("Name:        %s\n", def.Name)
	fmt.Printf("Model:       %s\n", def.Model)
	fmt.Printf("MaxTurns:    %d\n", def.MaxTurns)
	fmt.Printf("Description: %s\n", def.Description)
	if len(def.Tools) > 0 {
		fmt.Printf("Tools:       %s\n", strings.Join(def.Tools, ", "))
	}
	fmt.Printf("\nSystem Prompt:\n%s\n", def.SystemPrompt)
	return nil
}

// ─── worker list (top-level observe group) ───────────────────────────────────

// WorkerListTopCmd is the top-level `st worker` command, now in the MONITORING
// group. It directly lists active workers (no sub-command wrapper needed).
type WorkerListTopCmd struct{}

func (c *WorkerListTopCmd) Run(g *Globals) error {
	ws := g.WS()
	workers, err := worker.List(ws)
	if err != nil {
		return err
	}
	if len(workers) == 0 {
		fmt.Println("no active workers")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPID\tTASK\tAGENT\tALIVE\tHEARTBEAT")
	for _, wk := range workers {
		alive := "yes"
		if !worker.IsAlive(wk.PID) {
			alive = "no"
		}
		hb := "none"
		if wk.HeartbeatAt > 0 {
			hb = fmtAge(wk.HeartbeatAt) + " ago"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", wk.ID, wk.PID, wk.TaskID, wk.Agent, alive, hb)
	}
	w.Flush()
	return nil
}

// ─── status ──────────────────────────────────────────────────────────────────

// StatusCmd prints goal and task status. Without --goal it shows all goals and
// tasks; with --goal it shows tasks for that specific goal.
type StatusCmd struct {
	Goal string `short:"g" optional:"" help:"Filter by goal ID."`
}

func (c *StatusCmd) Run(g *Globals) error {
	ws := g.WS()
	if c.Goal != "" {
		supervisor.PrintGoalStatus(ws, c.Goal)
	} else {
		supervisor.PrintAllStatus(ws)
	}
	return nil
}

// ─── log ─────────────────────────────────────────────────────────────────────

type LogCmd struct {
	ID   string `arg:"" help:"Task ID."`
	Tail int    `default:"0" help:"Show last N lines (0 = all)."`
}

func (c *LogCmd) Run(g *Globals) error {
	ws := g.WS()
	data, err := os.ReadFile(ws.LogPath(c.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no log found for %s", c.ID)
		}
		return err
	}

	content := string(data)
	if c.Tail > 0 {
		// Trim trailing newline before splitting so an empty last element
		// from a trailing '\n' doesn't offset the tail count.
		trimmed := strings.TrimRight(content, "\n")
		lines := strings.Split(trimmed, "\n")
		if c.Tail < len(lines) {
			lines = lines[len(lines)-c.Tail:]
		}
		content = strings.Join(lines, "\n") + "\n"
	}
	fmt.Print(content)
	return nil
}

// ─── version ─────────────────────────────────────────────────────────────────

type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Printf("st %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	return nil
}

// ─── clean ───────────────────────────────────────────────────────────────────

type CleanCmd struct {
	Incomplete bool   `help:"Remove only non-done items (queued, planning, active, failed)."`
	GoalID     string `name:"goal" help:"Scope to a specific goal and its tasks only."`
	Yes        bool   `help:"Skip confirmation prompt."`
}

func (c *CleanCmd) Run(g *Globals) error {
	ws := g.WS()

	// incompleteGoalStatus defines which goal statuses are considered incomplete.
	incompleteGoalStatus := map[string]bool{
		goal.StatusQueued:   true,
		goal.StatusPlanning: true,
		goal.StatusActive:   true,
		goal.StatusFailed:   true,
	}
	// incompleteTaskStatus defines which task statuses are considered incomplete.
	incompleteTaskStatus := map[string]bool{
		task.StatusPending:   true,
		task.StatusRunning:   true,
		task.StatusBlocked:   true,
		task.StatusFailed:    true,
		task.StatusCancelled: true,
	}

	var goalsToRemove []*goal.Goal
	var tasksToRemove []*task.TaskWithStatus

	if c.GoalID != "" {
		gObj, err := goal.Get(ws, c.GoalID)
		if err != nil {
			return err
		}
		if !c.Incomplete || incompleteGoalStatus[gObj.Status] {
			goalsToRemove = append(goalsToRemove, gObj)
		}
		tasks, err := task.ListForGoal(ws, c.GoalID)
		if err != nil {
			return err
		}
		for _, ts := range tasks {
			if !c.Incomplete || incompleteTaskStatus[ts.Status] {
				tasksToRemove = append(tasksToRemove, ts)
			}
		}
	} else {
		allGoals, err := goal.List(ws)
		if err != nil {
			return err
		}
		for _, gObj := range allGoals {
			if !c.Incomplete || incompleteGoalStatus[gObj.Status] {
				goalsToRemove = append(goalsToRemove, gObj)
			}
		}
		allTasks, err := task.ListAll(ws)
		if err != nil {
			return err
		}
		for _, ts := range allTasks {
			if !c.Incomplete || incompleteTaskStatus[ts.Status] {
				tasksToRemove = append(tasksToRemove, ts)
			}
		}
	}

	if len(goalsToRemove) == 0 && len(tasksToRemove) == 0 {
		fmt.Println("nothing to clean")
		return nil
	}

	// Describe what will be removed.
	if c.GoalID != "" {
		fmt.Printf("will remove %d goal(s) and %d task(s) for goal %s\n",
			len(goalsToRemove), len(tasksToRemove), c.GoalID)
	} else if c.Incomplete {
		fmt.Printf("will remove %d non-done goal(s) and %d non-done task(s)\n",
			len(goalsToRemove), len(tasksToRemove))
	} else {
		fmt.Printf("will remove %d goal(s) and %d task(s) (and associated logs, heartbeats, worktrees)\n",
			len(goalsToRemove), len(tasksToRemove))
	}

	// Confirmation prompt unless --yes.
	if !c.Yes {
		fmt.Print("confirm? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))
		if input != "y" {
			fmt.Println("aborted")
			return nil
		}
	}

	// Remove goals.
	removedGoals := 0
	for _, gObj := range goalsToRemove {
		if err := os.Remove(ws.GoalPath(gObj.ID)); err != nil && !os.IsNotExist(err) {
			slog.Warn("removing goal failed", slog.String("goal_id", gObj.ID), slog.Any("error", err))
		} else {
			removedGoals++
		}
	}

	// Remove tasks with their associated logs, heartbeats, and worktrees.
	removedTasks := 0
	for _, ts := range tasksToRemove {
		taskPath := filepath.Join(ws.StatusDir(ts.Status), ts.Task.ID+".json")
		if err := os.Remove(taskPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("removing task failed", slog.String("task_id", ts.Task.ID), slog.Any("error", err))
		} else {
			removedTasks++
		}
		// Best-effort cleanup of associated files.
		os.Remove(ws.LogPath(ts.Task.ID))
		os.Remove(ws.HeartbeatPath(ts.Task.ID))
		os.RemoveAll(ws.WorktreePath(ts.Task.ID))
	}

	// In a full clean (not scoped, not incomplete), sweep orphaned files too.
	if c.GoalID == "" && !c.Incomplete {
		cleanDirContents(ws.LogsDir())
		cleanDirContents(ws.HeartbeatsDir())
		cleanDirContents(ws.WorktreesDir())
	}

	fmt.Printf("removed %d goals and %d tasks\n", removedGoals, removedTasks)
	return nil
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	logger.Init()

	var cli CLI
	globals := &Globals{}

	ctx := kong.Parse(&cli,
		kong.Name("st"),
		kong.Description("stint — multi-agent AI task orchestration\n\nCoordinate AI workers: plan tasks, dispatch agents, track execution progress.\n\nUSAGE:  st <command> [arguments]"),
		kong.UsageOnError(),
		kong.Bind(globals),
		kong.ExplicitGroups([]kong.Group{
			{Key: "workspace", Title: "── WORKSPACE ────────────────────────────────────────────────────────────────────"},
			{Key: "goals", Title: "── GOALS ─────────────────────────────────────────────────────────────────────────"},
			{Key: "execution", Title: "── EXECUTION ─────────────────────────────────────────────────────────────────────"},
			{Key: "tasks", Title: "── TASKS ─────────────────────────────────────────────────────────────────────────"},
			{Key: "agents", Title: "── AGENTS ────────────────────────────────────────────────────────────────────────"},
			{Key: "observe", Title: "── MONITORING ────────────────────────────────────────────────────────────────────"},
			{Key: "maint", Title: "── MAINTENANCE ───────────────────────────────────────────────────────────────────"},
			{Key: "protocol", Title: "── WORKER PROTOCOL ───────────────────────────────────────────────────────────────"},
		}),
	)

	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// openWS finds the workspace from ST_WORKSPACE env var (preferred) or CWD.
// ST_WORKSPACE takes priority so that subprocesses (planners, workers, e2e
// supervisors) always use the workspace they were explicitly given, even when
// their CWD is inside a different workspace tree.
func openWS() *workspace.Workspace {
	// Prefer explicit env var — avoids CWD-walk confusion when a subprocess
	// has a CWD that lives inside a different (ancestor) workspace.
	if dir := os.Getenv("ST_WORKSPACE"); dir != "" {
		ws, err := workspace.Open(dir)
		if err != nil {
			fatal("open workspace from ST_WORKSPACE: %v", err)
		}
		return ws
	}
	// Fall back to walking up from CWD.
	cwd, err := os.Getwd()
	if err != nil {
		fatal("cannot determine current directory and ST_WORKSPACE is not set")
	}
	ws, err := workspace.FindRoot(cwd)
	if err != nil {
		fatal("not inside a st workspace (no .st/workspace.json found in %s or any parent directory)\n\nTo create a new workspace here:    st init .\nTo use an existing workspace:      export ST_WORKSPACE=/path/to/workspace", cwd)
	}
	return ws
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "st: "+format+"\n", args...)
	os.Exit(1)
}

// cleanDirContents removes all entries inside dir (not the directory itself).
func cleanDirContents(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			os.RemoveAll(p)
		} else {
			os.Remove(p)
		}
	}
}

// copyDirRecursive copies src directory tree into dst using filepath.Walk.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fmtTime(ts int64) string {
	if ts == 0 {
		return "—"
	}
	// Detect nanosecond vs second timestamps.
	// Current Unix seconds ~1.7e9; nanoseconds ~1.7e18.
	// Values > 1e12 are nanoseconds.
	var t time.Time
	if ts > 1_000_000_000_000 {
		t = time.Unix(0, ts)
	} else {
		t = time.Unix(ts, 0)
	}
	return t.Format("2006-01-02 15:04:05")
}

func fmtAge(unix int64) string {
	if unix == 0 {
		return "never"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}
