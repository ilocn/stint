package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/ilocn/stint/internal/goal"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/workspace"
)

// newTestWS creates a temporary workspace for tests.
func newTestWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Init(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws
}

// captureKongHelp returns the kong --help output for the given subcommand args.
// e.g. captureKongHelp("run") returns `st run --help` output.
// e.g. captureKongHelp() returns `st --help` output.
func captureKongHelp(t *testing.T, subcmd ...string) string {
	t.Helper()
	var cli CLI
	globals := &Globals{}

	var buf bytes.Buffer
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Description("stint — multi-agent AI task orchestration\n\nCoordinate AI workers: plan tasks, dispatch agents, track execution progress.\n\nUSAGE:  st <command> [arguments]"),
		kong.UsageOnError(),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
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
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := append(subcmd, "--help")
	_, _ = k.Parse(args)
	return buf.String()
}

// TestRunHelpContainsRandomPortFlag verifies that 'st run --help' documents
// the --random-port flag. The flag is the user-facing surface of the random-port
// feature; if it's missing from help the feature is effectively undiscoverable.
func TestRunHelpContainsRandomPortFlag(t *testing.T) {
	output := captureKongHelp(t, "run")
	if !strings.Contains(output, "random-port") {
		t.Errorf("'st run --help' does not mention --random-port\noutput:\n%s", output)
	}
}

// TestSupervisorHelpContainsRandomPortFlag verifies that 'st supervisor --help'
// documents the --random-port flag.
func TestSupervisorHelpContainsRandomPortFlag(t *testing.T) {
	output := captureKongHelp(t, "supervisor")
	if !strings.Contains(output, "random-port") {
		t.Errorf("'st supervisor --help' does not mention --random-port\noutput:\n%s", output)
	}
}

// TestRunHelpContainsWebDefault8080 verifies that 'st run --help' tells users
// the web dashboard starts on port 8080 by default.
func TestRunHelpContainsWebDefault8080(t *testing.T) {
	output := captureKongHelp(t, "run")
	if !strings.Contains(output, "8080") {
		t.Errorf("'st run --help' does not mention 8080 as default web port\noutput:\n%s", output)
	}
}

// TestSupervisorHelpContainsWebDefault8080 verifies that 'st supervisor --help'
// tells users the web dashboard starts on port 8080 by default.
func TestSupervisorHelpContainsWebDefault8080(t *testing.T) {
	output := captureKongHelp(t, "supervisor")
	if !strings.Contains(output, "8080") {
		t.Errorf("'st supervisor --help' does not mention 8080 as default web port\noutput:\n%s", output)
	}
}

// TestRandomPortPatternAllocatesValidPort validates the exact net.Listen(":0")
// technique used by the --random-port flag in cmdRun and cmdSupervisor.  The
// pattern binds ":0", lets the OS assign a free port, records it, then closes
// the listener — leaving a known-free port for the supervisor to rebind.
// This test mirrors the implementation:
//
//	ln, err := net.Listen("tcp", ":0")
//	port = ln.Addr().(*net.TCPAddr).Port
//	ln.Close()
func TestRandomPortPatternAllocatesValidPort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen(\":0\"): %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Port must be in the valid ephemeral range.
	if port < 1 || port > 65535 {
		t.Errorf("allocated port %d is outside valid TCP range [1, 65535]", port)
	}
}

// TestRandomPortPatternReturnsDifferentPortsOnRepeatedCalls verifies that
// repeated invocations of the OS-assign-then-close pattern tend to produce
// different ports.  Multiple concurrent 'st run --random-port' or
// 'st supervisor --random-port' processes should each land on a distinct port.
func TestRandomPortPatternReturnsDifferentPortsOnRepeatedCalls(t *testing.T) {
	const n = 10
	seen := make(map[int]bool, n)

	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("net.Listen(\":0\") iteration %d: %v", i, err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		seen[port] = true
	}

	// Assert at least 2 distinct ports — a healthy OS almost always returns
	// different values but we avoid asserting n distinct to remain non-flaky.
	if len(seen) < 2 {
		ports := make([]string, 0, len(seen))
		for p := range seen {
			ports = append(ports, fmt.Sprintf("%d", p))
		}
		t.Errorf("%d allocations all returned the same port(s) %v; expected variety", n, ports)
	}
}

// TestKongPositionalArgsBeforeFlags verifies that kong-based commands that mix
// positional arguments with flags parse correctly. This replaces the old
// splitArgsAndFlags tests — kong natively handles "cmd <id> --flag val" syntax
// via arg:"" struct tags, making the custom splitArgsAndFlags helper obsolete.
//
// These cases correspond to the commands that previously required splitArgsAndFlags:
//   - st task-done <id> --summary "..."
//   - st task-fail <id> --reason "..."
//   - st log <id> [--tail N]
//   - st agent create <name> --prompt "..."
func TestKongPositionalArgsBeforeFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantCmd string // expected command path
	}{
		{
			name:    "task-done with id before summary flag",
			args:    []string{"task-done", "myid", "--summary", "all done"},
			wantCmd: "task-done <id>",
		},
		{
			name:    "task-fail with id before reason flag",
			args:    []string{"task-fail", "myid", "--reason", "broke"},
			wantCmd: "task-fail <id>",
		},
		{
			name:    "log with id and tail flag",
			args:    []string{"log", "myid", "--tail", "5"},
			wantCmd: "log <id>",
		},
		{
			name:    "heartbeat with id",
			args:    []string{"heartbeat", "myid"},
			wantCmd: "heartbeat <id>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cli CLI
			globals := &Globals{}

			var exitCode int
			k, err := kong.New(&cli,
				kong.Name("st"),
				kong.Bind(globals),
				kong.Exit(func(code int) { exitCode = code }),
			)
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}

			ctx, parseErr := k.Parse(tc.args)
			// We expect successful parse (exit code 0, no error) since the args are valid.
			if parseErr != nil {
				t.Errorf("parse error for %v: %v", tc.args, parseErr)
				return
			}
			if exitCode != 0 {
				t.Errorf("exit code %d for args %v", exitCode, tc.args)
				return
			}
			// Verify the command was matched (not just parsed without error).
			if ctx == nil {
				t.Errorf("nil context for args %v", tc.args)
			}
		})
	}
}

// newTestKong creates a kong instance wired up for testing: exits are captured
// rather than calling os.Exit, and both stdout/stderr go to buf.
func newTestKong(t *testing.T, buf *bytes.Buffer) (*kong.Kong, *int) {
	t.Helper()
	var cli CLI
	globals := &Globals{}
	var exitCode int
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(buf, buf),
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	return k, &exitCode
}

// parseExpectOK asserts that kong parses args without error and with exit 0.
func parseExpectOK(t *testing.T, args []string) *kong.Context {
	t.Helper()
	var buf bytes.Buffer
	k, exitCode := newTestKong(t, &buf)
	ctx, err := k.Parse(args)
	if err != nil {
		t.Errorf("unexpected parse error for %v: %v\noutput:\n%s", args, err, buf.String())
	}
	if *exitCode != 0 {
		t.Errorf("unexpected exit code %d for %v\noutput:\n%s", *exitCode, args, buf.String())
	}
	return ctx
}

// parseExpectError asserts that kong rejects args (parse error or non-zero exit).
func parseExpectError(t *testing.T, args []string) {
	t.Helper()
	var buf bytes.Buffer
	k, exitCode := newTestKong(t, &buf)
	_, parseErr := k.Parse(args)
	if parseErr == nil && *exitCode == 0 {
		t.Errorf("expected parse error or non-zero exit for %v, but got neither\noutput:\n%s", args, buf.String())
	}
}

// ─── Root help ───────────────────────────────────────────────────────────────

// TestRootHelpContainsAllCommandGroups verifies that 'st --help' includes every
// command-group section header added by ExplicitGroups. Missing headers mean a
// whole category of commands is hidden from users.
func TestRootHelpContainsAllCommandGroups(t *testing.T) {
	output := captureKongHelp(t)
	groups := []string{
		"WORKSPACE",
		"GOALS",
		"EXECUTION",
		"TASKS",
		"AGENTS",
		"MONITORING",
		"MAINTENANCE",
		"WORKER PROTOCOL",
	}
	for _, g := range groups {
		if !strings.Contains(output, g) {
			t.Errorf("root --help missing section %q\noutput:\n%s", g, output)
		}
	}
}

// TestRootHelpContainsAllTopLevelSubcommands verifies that every top-level
// subcommand name appears in root --help so users can discover them.
func TestRootHelpContainsAllTopLevelSubcommands(t *testing.T) {
	output := captureKongHelp(t)
	subcommands := []string{
		"init",
		"repo",
		"goal",
		"plan",
		"run",
		"supervisor",
		"recover",
		"task",
		"heartbeat",
		"agent",
		"log",
		"version",
		"clean",
	}
	for _, sub := range subcommands {
		if !strings.Contains(output, sub) {
			t.Errorf("root --help missing subcommand %q\noutput:\n%s", sub, output)
		}
	}
}

// ─── Per-subcommand group help ────────────────────────────────────────────────

// TestTaskGroupHelpListsAllSubcommands verifies that 'st task --help' lists every
// task subcommand. This is a new capability vs the old stdlib-flag implementation
// which only printed generic usage text for group commands.
func TestTaskGroupHelpListsAllSubcommands(t *testing.T) {
	output := captureKongHelp(t, "task")
	for _, sub := range []string{"add", "list", "show", "retry", "cancel", "done", "fail"} {
		if !strings.Contains(output, sub) {
			t.Errorf("'st task --help' missing subcommand %q\noutput:\n%s", sub, output)
		}
	}
}

// TestGoalGroupHelpListsAllSubcommands verifies that 'st goal --help' lists
// all goal subcommands.
func TestGoalGroupHelpListsAllSubcommands(t *testing.T) {
	output := captureKongHelp(t, "goal")
	for _, sub := range []string{"add", "list", "show", "cancel"} {
		if !strings.Contains(output, sub) {
			t.Errorf("'st goal --help' missing subcommand %q\noutput:\n%s", sub, output)
		}
	}
}

// TestAgentGroupHelpListsAllSubcommands verifies that 'st agent --help' lists
// all agent subcommands.
func TestAgentGroupHelpListsAllSubcommands(t *testing.T) {
	output := captureKongHelp(t, "agent")
	for _, sub := range []string{"create", "fetch", "remove", "list", "show"} {
		if !strings.Contains(output, sub) {
			t.Errorf("'st agent --help' missing subcommand %q\noutput:\n%s", sub, output)
		}
	}
}

// TestRepoGroupHelpListsAllSubcommands verifies that 'st repo --help' lists
// all repo subcommands.
func TestRepoGroupHelpListsAllSubcommands(t *testing.T) {
	output := captureKongHelp(t, "repo")
	for _, sub := range []string{"add", "clone", "delete", "list"} {
		if !strings.Contains(output, sub) {
			t.Errorf("'st repo --help' missing subcommand %q\noutput:\n%s", sub, output)
		}
	}
}

// ─── Flag preservation ────────────────────────────────────────────────────────

// TestTaskAddHelpContainsAllFlags verifies that 'st task add --help' documents
// every flag that existed before the Kong migration.
func TestTaskAddHelpContainsAllFlags(t *testing.T) {
	output := captureKongHelp(t, "task", "add")
	flags := []string{
		"--goal",
		"--title",
		"--prompt",
		"--agent",
		"--deps",
		"--context-from",
		"--goal-branch",
		"--branch",
	}
	for _, f := range flags {
		if !strings.Contains(output, f) {
			t.Errorf("'st task add --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestAgentAddHelpContainsAllFlags verifies that 'st agent create --help'
// documents every flag that existed before the restructuring.
func TestAgentAddHelpContainsAllFlags(t *testing.T) {
	output := captureKongHelp(t, "agent", "create")
	flags := []string{
		"--prompt",
		"--tools",
		"--model",
		"--max-turns",
	}
	for _, f := range flags {
		if !strings.Contains(output, f) {
			t.Errorf("'st agent create --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestTaskListHelpContainsStatusAndGoalFlags verifies that 'st task list --help'
// documents --status and --goal filter flags.
func TestTaskListHelpContainsStatusAndGoalFlags(t *testing.T) {
	output := captureKongHelp(t, "task", "list")
	for _, f := range []string{"--goal", "--status"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st task list --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestTaskDoneHelpContainsSummaryAndBranchFlags verifies 'st task-done --help'.
func TestTaskDoneHelpContainsSummaryAndBranchFlags(t *testing.T) {
	output := captureKongHelp(t, "task-done")
	for _, f := range []string{"--summary", "--branch"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st task-done --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestTaskFailHelpContainsReasonFlag verifies 'st task-fail --help'.
func TestTaskFailHelpContainsReasonFlag(t *testing.T) {
	output := captureKongHelp(t, "task-fail")
	if !strings.Contains(output, "--reason") {
		t.Errorf("'st task-fail --help' missing --reason\noutput:\n%s", output)
	}
}

// TestLogHelpContainsTailFlag verifies 'st log --help'.
func TestLogHelpContainsTailFlag(t *testing.T) {
	output := captureKongHelp(t, "log")
	if !strings.Contains(output, "--tail") {
		t.Errorf("'st log --help' missing --tail\noutput:\n%s", output)
	}
}

// TestCleanHelpContainsIncompleteAndYesFlags verifies 'st clean --help'.
func TestCleanHelpContainsIncompleteAndYesFlags(t *testing.T) {
	output := captureKongHelp(t, "clean")
	for _, f := range []string{"--incomplete", "--yes", "--goal"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st clean --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestGoalAddHelpContainsHintAndReposFlags verifies 'st goal add --help'.
func TestGoalAddHelpContainsHintAndReposFlags(t *testing.T) {
	output := captureKongHelp(t, "goal", "add")
	for _, f := range []string{"--hint", "--repos"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st goal add --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestPlanHelpContainsHintAndResumeFlags verifies 'st plan --help'.
func TestPlanHelpContainsHintAndResumeFlags(t *testing.T) {
	output := captureKongHelp(t, "plan")
	for _, f := range []string{"--hint", "--resume"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st plan --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestRunHelpContainsHintAndReposFlags verifies 'st run --help'.
func TestRunHelpContainsHintAndReposFlags(t *testing.T) {
	output := captureKongHelp(t, "run")
	for _, f := range []string{"--hint", "--repos", "--max-concurrent"} {
		if !strings.Contains(output, f) {
			t.Errorf("'st run --help' missing flag %q\noutput:\n%s", f, output)
		}
	}
}

// TestSupervisorHelpContainsMaxConcurrentFlag verifies 'st supervisor --help'.
func TestSupervisorHelpContainsMaxConcurrentFlag(t *testing.T) {
	output := captureKongHelp(t, "supervisor")
	if !strings.Contains(output, "--max-concurrent") {
		t.Errorf("'st supervisor --help' missing --max-concurrent\noutput:\n%s", output)
	}
}

// ─── Default values ───────────────────────────────────────────────────────────

// TestAgentCreateDefaultModelIsSonnet verifies that the --model flag defaults to
// "sonnet" in the help output, so users know what they get without the flag.
func TestAgentCreateDefaultModelIsSonnet(t *testing.T) {
	output := captureKongHelp(t, "agent", "create")
	if !strings.Contains(output, "sonnet") {
		t.Errorf("'st agent create --help' does not show default model 'sonnet'\noutput:\n%s", output)
	}
}

// TestAgentCreateDefaultMaxTurnsIs50 verifies that --max-turns defaults to 50.
func TestAgentCreateDefaultMaxTurnsIs50(t *testing.T) {
	output := captureKongHelp(t, "agent", "create")
	if !strings.Contains(output, "50") {
		t.Errorf("'st agent create --help' does not show default max-turns 50\noutput:\n%s", output)
	}
}

// TestTaskAddDefaultAgentIsDefault verifies that --agent defaults to "default".
func TestTaskAddDefaultAgentIsDefault(t *testing.T) {
	output := captureKongHelp(t, "task", "add")
	if !strings.Contains(output, "default") {
		t.Errorf("'st task add --help' does not show default agent 'default'\noutput:\n%s", output)
	}
}

// TestSupervisorDefaultMaxConcurrent verifies that --max-concurrent defaults to 4.
func TestSupervisorDefaultMaxConcurrent(t *testing.T) {
	output := captureKongHelp(t, "supervisor")
	if !strings.Contains(output, "4") {
		t.Errorf("'st supervisor --help' does not show default max-concurrent 4\noutput:\n%s", output)
	}
}

// ─── Required flags produce errors ───────────────────────────────────────────

// TestTaskAddMissingRequiredFlagsIsRejected verifies that 'st task add' without
// the required --goal, --title, or --prompt flags produces a parse error.
// Kong enforces required:"" tags at parse time, unlike the old manual validation.
func TestTaskAddMissingRequiredFlagsIsRejected(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing all required", []string{"task", "add"}},
		{"missing --title and --prompt", []string{"task", "add", "--goal", "g1"}},
		{"missing --prompt", []string{"task", "add", "--goal", "g1", "--title", "t1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parseExpectError(t, tc.args)
		})
	}
}

// TestTaskAddAllRequiredFlagsAccepted verifies that providing all required flags
// for 'st task add' results in successful parsing.
func TestTaskAddAllRequiredFlagsAccepted(t *testing.T) {
	args := []string{"task", "add", "--goal", "g1", "--title", "my task", "--prompt", "do the thing"}
	parseExpectOK(t, args)
}

// TestTaskDoneMissingSummaryIsRejected verifies that 'st task-done <id>' without
// --summary is rejected at parse time.
func TestTaskDoneMissingSummaryIsRejected(t *testing.T) {
	parseExpectError(t, []string{"task-done", "myid"})
}

// TestTaskDoneWithSummaryIsAccepted verifies that providing --summary makes
// 'st task-done' parse successfully.
func TestTaskDoneWithSummaryIsAccepted(t *testing.T) {
	parseExpectOK(t, []string{"task-done", "myid", "--summary", "completed work"})
}

// TestTaskFailMissingReasonIsRejected verifies that 'st task-fail <id>' without
// --reason is rejected at parse time.
func TestTaskFailMissingReasonIsRejected(t *testing.T) {
	parseExpectError(t, []string{"task-fail", "myid"})
}

// TestTaskFailWithReasonIsAccepted verifies that providing --reason makes
// 'st task-fail' parse successfully.
func TestTaskFailWithReasonIsAccepted(t *testing.T) {
	parseExpectOK(t, []string{"task-fail", "myid", "--reason", "build failed"})
}

// TestAgentCreateMissingPromptIsRejected verifies that 'st agent create <name>'
// without --prompt is rejected at parse time.
func TestAgentCreateMissingPromptIsRejected(t *testing.T) {
	parseExpectError(t, []string{"agent", "create", "myagent"})
}

// TestAgentCreateWithPromptIsAccepted verifies that providing --prompt makes
// 'st agent create' parse successfully.
func TestAgentCreateWithPromptIsAccepted(t *testing.T) {
	parseExpectOK(t, []string{"agent", "create", "myagent", "--prompt", "you are a helpful assistant"})
}

// TestAgentFetchSourceParsesOK verifies 'st agent fetch <source>' parses.
func TestAgentFetchSourceParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"agent", "fetch", "owner/repo/path/agent.md"})
}

// TestAgentFetchWithNameOverrideParsesOK verifies the --name flag is accepted.
func TestAgentFetchWithNameOverrideParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"agent", "fetch", "owner/repo/path", "--name", "my-agent"})
}

// TestAgentRemoveParsesOK verifies 'st agent remove <name>' parses correctly.
func TestAgentRemoveParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"agent", "remove", "some-agent"})
}

// ─── Positional arguments for all commands ────────────────────────────────────

// TestAllPositionalArgCommandsParse verifies that every command taking a positional
// <id> or <name> argument parses correctly when that argument is provided.
func TestAllPositionalArgCommandsParse(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"task show", []string{"task", "show", "task-id-123"}},
		{"task retry", []string{"task", "retry", "task-id-123"}},
		{"task cancel", []string{"task", "cancel", "task-id-123"}},
		{"task-done", []string{"task-done", "task-id-123", "--summary", "done"}},
		{"task-fail", []string{"task-fail", "task-id-123", "--reason", "failed"}},
		{"goal show", []string{"goal", "show", "goal-id-456"}},
		{"goal cancel", []string{"goal", "cancel", "goal-id-456"}},
		{"agent show", []string{"agent", "show", "my-agent"}},
		{"log", []string{"log", "task-id-123"}},
		{"log with tail", []string{"log", "task-id-123", "--tail", "20"}},
		{"heartbeat", []string{"heartbeat", "task-id-123"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parseExpectOK(t, tc.args)
		})
	}
}

// TestPositionalArgCommandsMissingIDIsRejected verifies that commands requiring
// a positional <id> argument reject invocations without the argument.
func TestPositionalArgCommandsMissingIDIsRejected(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"task show no id", []string{"task", "show"}},
		{"task retry no id", []string{"task", "retry"}},
		{"task cancel no id", []string{"task", "cancel"}},
		{"goal show no id", []string{"goal", "show"}},
		{"goal cancel no id", []string{"goal", "cancel"}},
		{"agent show no name", []string{"agent", "show"}},
		{"log no id", []string{"log"}},
		{"heartbeat no id", []string{"heartbeat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parseExpectError(t, tc.args)
		})
	}
}

// ─── Flag value parsing ───────────────────────────────────────────────────────

// TestTaskAddFlagValuesAreParsed verifies that kong correctly stores flag values
// into the struct fields after parsing, not just that parsing succeeds.
func TestTaskAddFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf bytes.Buffer
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := []string{
		"task", "add",
		"--goal", "g-123",
		"--title", "my task title",
		"--prompt", "do the work",
		"--agent", "impl",
		"--deps", "t-1,t-2",
		"--context-from", "t-0",
		"--goal-branch", "st/goals/g-123",
		"--branch", "st/tasks/t-abc",
	}
	if _, err := k.Parse(args); err != nil {
		t.Fatalf("parse error: %v\noutput:\n%s", err, buf.String())
	}

	c := &cli.Task.Add
	if c.GoalID != "g-123" {
		t.Errorf("GoalID: got %q, want %q", c.GoalID, "g-123")
	}
	if c.Title != "my task title" {
		t.Errorf("Title: got %q, want %q", c.Title, "my task title")
	}
	if c.Prompt != "do the work" {
		t.Errorf("Prompt: got %q, want %q", c.Prompt, "do the work")
	}
	if c.Agent != "impl" {
		t.Errorf("Agent: got %q, want %q", c.Agent, "impl")
	}
	if c.Deps != "t-1,t-2" {
		t.Errorf("Deps: got %q, want %q", c.Deps, "t-1,t-2")
	}
	if c.ContextFrom != "t-0" {
		t.Errorf("ContextFrom: got %q, want %q", c.ContextFrom, "t-0")
	}
	if c.GoalBranch != "st/goals/g-123" {
		t.Errorf("GoalBranch: got %q, want %q", c.GoalBranch, "st/goals/g-123")
	}
	if c.Branch != "st/tasks/t-abc" {
		t.Errorf("Branch: got %q, want %q", c.Branch, "st/tasks/t-abc")
	}
}

// TestTaskDoneFlagValuesAreParsed verifies ID, summary, and branch are correctly
// parsed into TaskDoneCmd fields.
func TestTaskDoneFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf bytes.Buffer
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := []string{"task-done", "task-abc", "--summary", "all done", "--branch", "st/tasks/t-abc"}
	if _, err := k.Parse(args); err != nil {
		t.Fatalf("parse error: %v\noutput:\n%s", err, buf.String())
	}

	c := &cli.TaskDone
	if c.ID != "task-abc" {
		t.Errorf("ID: got %q, want %q", c.ID, "task-abc")
	}
	if c.Summary != "all done" {
		t.Errorf("Summary: got %q, want %q", c.Summary, "all done")
	}
	if c.Branch != "st/tasks/t-abc" {
		t.Errorf("Branch: got %q, want %q", c.Branch, "st/tasks/t-abc")
	}
}

// TestAgentCreateFlagValuesAreParsed verifies that agent create flag values
// are correctly parsed into AgentCreateCmd fields.
func TestAgentCreateFlagValuesAreParsed(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf bytes.Buffer
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	args := []string{
		"agent", "create", "myagent",
		"--prompt", "you are helpful",
		"--tools", "bash,read,write",
		"--model", "opus",
		"--max-turns", "100",
	}
	if _, err := k.Parse(args); err != nil {
		t.Fatalf("parse error: %v\noutput:\n%s", err, buf.String())
	}

	c := &cli.Agent.Create
	if c.Name != "myagent" {
		t.Errorf("Name: got %q, want %q", c.Name, "myagent")
	}
	if c.Prompt != "you are helpful" {
		t.Errorf("Prompt: got %q, want %q", c.Prompt, "you are helpful")
	}
	if c.Tools != "bash,read,write" {
		t.Errorf("Tools: got %q, want %q", c.Tools, "bash,read,write")
	}
	if c.Model != "opus" {
		t.Errorf("Model: got %q, want %q", c.Model, "opus")
	}
	if c.MaxTurns != 100 {
		t.Errorf("MaxTurns: got %d, want %d", c.MaxTurns, 100)
	}
}

// TestSupervisorFlagDefaultValues verifies that supervisor struct fields have
// the expected default values when parsed without flags.
func TestSupervisorFlagDefaultValues(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf bytes.Buffer
	// supervisor requires a workspace, but defaults are set at parse time
	// before Run is called — so we can test defaults via parse alone.
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	if _, err := k.Parse([]string{"supervisor"}); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	c := &cli.Supervisor
	if c.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent default: got %d, want 4", c.MaxConcurrent)
	}
	if c.Port != 8080 {
		t.Errorf("Port default: got %d, want 8080", c.Port)
	}
	if c.RandomPort {
		t.Errorf("RandomPort default: got true, want false")
	}
}

// TestLogTailDefaultIsZero verifies that --tail defaults to 0 (show all lines).
func TestLogTailDefaultIsZero(t *testing.T) {
	var cli CLI
	globals := &Globals{}
	var buf bytes.Buffer
	k, err := kong.New(&cli,
		kong.Name("st"),
		kong.Bind(globals),
		kong.Writers(&buf, &buf),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	if _, err := k.Parse([]string{"log", "task-123"}); err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if cli.Log.Tail != 0 {
		t.Errorf("Log.Tail default: got %d, want 0", cli.Log.Tail)
	}
}

// ─── Behavioral regression guards ────────────────────────────────────────────

// TestWorkerCommandParsesOK verifies that 'st worker' parses and runs directly
// (no subcommand required — worker is now a flat command in the MONITORING group).
func TestWorkerCommandParsesOK(t *testing.T) {
	parseExpectOK(t, []string{"worker"})
}

// TestTaskAddRepoFlagInHelp verifies that 'st task add --help' documents the
// --repo flag (was missing from the original flag list test).
func TestTaskAddRepoFlagInHelp(t *testing.T) {
	output := captureKongHelp(t, "task", "add")
	if !strings.Contains(output, "--repo") {
		t.Errorf("'st task add --help' missing --repo flag\noutput:\n%s", output)
	}
}

// ─── updateGoalStatusAfterPlanner ────────────────────────────────────────────

// TestUpdateGoalStatusAfterPlannerSuccessWithTasks verifies that when the
// planner exits without error and has created tasks, the goal is transitioned
// to active so the supervisor can dispatch them.
func TestUpdateGoalStatusAfterPlannerSuccessWithTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "goal with tasks", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "planned task", Agent: "default", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	if err := updateGoalStatusAfterPlanner(ws, g.ID, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusActive {
		t.Errorf("goal status = %s, want active (planner succeeded with tasks)", got.Status)
	}
}

// TestUpdateGoalStatusAfterPlannerSuccessNoTasks verifies that when the
// planner exits without error but created no tasks, the goal is marked done.
func TestUpdateGoalStatusAfterPlannerSuccessNoTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "goal no tasks", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	if err := updateGoalStatusAfterPlanner(ws, g.ID, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusDone {
		t.Errorf("goal status = %s, want done (planner succeeded with no tasks)", got.Status)
	}
}

// TestUpdateGoalStatusAfterPlannerErrorWithTasks verifies that when the planner
// exits with an error but created tasks, the goal transitions to active (partial
// work can still run) and the function returns the planner error.
func TestUpdateGoalStatusAfterPlannerErrorWithTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "partial goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "partial task", Agent: "default", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	planErr := errors.New("planner crashed")
	err = updateGoalStatusAfterPlanner(ws, g.ID, planErr)
	if err == nil {
		t.Error("expected error returned, got nil")
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusActive {
		t.Errorf("goal status = %s, want active (planner errored but left tasks)", got.Status)
	}
}

// TestUpdateGoalStatusAfterPlannerErrorNoTasks verifies that when the planner
// exits with an error and created no tasks, the goal is marked failed and the
// function returns the planner error.
func TestUpdateGoalStatusAfterPlannerErrorNoTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "failed goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	planErr := errors.New("planner crashed")
	err = updateGoalStatusAfterPlanner(ws, g.ID, planErr)
	if err == nil {
		t.Error("expected error returned, got nil")
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusFailed {
		t.Errorf("goal status = %s, want failed (planner errored with no tasks)", got.Status)
	}
}

// TestUpdateGoalStatusAfterPlannerErrorWrapsOriginalMessage verifies that the
// error returned by updateGoalStatusAfterPlanner wraps the original planner
// error with the sentinel prefix "planner exited with error:". This ensures
// callers can identify the cause and the original message is not lost.
func TestUpdateGoalStatusAfterPlannerErrorWrapsOriginalMessage(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "error wrap goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	planErr := errors.New("exit status 1")
	got := updateGoalStatusAfterPlanner(ws, g.ID, planErr)
	if got == nil {
		t.Fatal("expected non-nil error, got nil")
	}
	msg := got.Error()
	if !strings.Contains(msg, "planner exited with error:") {
		t.Errorf("error message %q missing sentinel prefix 'planner exited with error:'", msg)
	}
	if !strings.Contains(msg, "exit status 1") {
		t.Errorf("error message %q does not contain original error 'exit status 1'", msg)
	}
}

// TestUpdateGoalStatusAfterPlannerSuccessWithMultipleTasks verifies that the
// goal transitions to active when the planner succeeds and created multiple
// tasks (not just a single task).
func TestUpdateGoalStatusAfterPlannerSuccessWithMultipleTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "goal with many tasks", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	for i := range 3 {
		tk := &task.Task{GoalID: g.ID, Title: fmt.Sprintf("task %d", i+1), Agent: "default", Prompt: "do it"}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create task %d: %v", i+1, err)
		}
	}

	if err := updateGoalStatusAfterPlanner(ws, g.ID, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusActive {
		t.Errorf("goal status = %s, want active (planner succeeded with 3 tasks)", got.Status)
	}
}

// TestUpdateGoalStatusAfterPlannerWhenGoalIsInPlanningStatus verifies that the
// helper correctly transitions a goal that is currently in planning status —
// which is the exact state PlanCmd.Run() leaves the goal in before calling
// the planner. This is the primary production call-site.
func TestUpdateGoalStatusAfterPlannerWhenGoalIsInPlanningStatus(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "planning goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}
	// Mirror what PlanCmd.Run() does: set status to planning before spawning planner.
	if err := goal.UpdateStatus(ws, g.ID, goal.StatusPlanning); err != nil {
		t.Fatalf("UpdateStatus planning: %v", err)
	}

	// Planner succeeds and creates one task.
	tk := &task.Task{GoalID: g.ID, Title: "impl task", Agent: "default", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	if err := updateGoalStatusAfterPlanner(ws, g.ID, nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	got, err := goal.Get(ws, g.ID)
	if err != nil {
		t.Fatalf("Get goal: %v", err)
	}
	if got.Status != goal.StatusActive {
		t.Errorf("goal status = %s, want active (planning→active after successful planner)", got.Status)
	}
}

// ─── fmtTime ─────────────────────────────────────────────────────────────────

// TestFmtTimeZeroReturnsEmDash verifies the sentinel "no timestamp" value
// returns the em-dash placeholder rather than a date.
func TestFmtTimeZeroReturnsEmDash(t *testing.T) {
	if got := fmtTime(0); got != "—" {
		t.Errorf("fmtTime(0) = %q, want %q", got, "—")
	}
}

// TestFmtTimeSecondsTimestamps verifies that values ≤ 1e12 are treated as
// Unix second timestamps. Table-driven to cover small, typical, and exact-
// boundary values.
func TestFmtTimeSecondsTimestamps(t *testing.T) {
	tests := []struct {
		name string
		ts   int64
	}{
		{"unix second 1 (near epoch)", 1},
		{"unix second 1 billion (2001-09-09)", 1_000_000_000},
		{"unix second 1.5 billion (2017-07-14)", 1_500_000_000},
		{"boundary value exactly 1e12 treated as seconds", 1_000_000_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := time.Unix(tc.ts, 0).Format("2006-01-02 15:04:05")
			got := fmtTime(tc.ts)
			if got != want {
				t.Errorf("fmtTime(%d) = %q, want %q", tc.ts, got, want)
			}
		})
	}
}

// TestFmtTimeNanosecondsTimestamps verifies that values > 1e12 are treated as
// nanosecond timestamps. A typical nanosecond stamp is ~1.7e18.
func TestFmtTimeNanosecondsTimestamps(t *testing.T) {
	tests := []struct {
		name string
		ts   int64
	}{
		{"one above boundary is nanoseconds", 1_000_000_000_001},
		{"realistic nanosecond timestamp (2023)", 1_700_000_000_000_000_000},
		{"another nanosecond value (2024)", 1_720_000_000_000_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := time.Unix(0, tc.ts).Format("2006-01-02 15:04:05")
			got := fmtTime(tc.ts)
			if got != want {
				t.Errorf("fmtTime(%d) = %q, want %q", tc.ts, got, want)
			}
		})
	}
}

// TestFmtTimeBoundarySecVsNano verifies that the exact threshold value 1e12 is
// treated differently from 1e12+1: the former is seconds, the latter nanoseconds.
// This guards against off-by-one errors in the threshold comparison.
func TestFmtTimeBoundarySecVsNano(t *testing.T) {
	secResult := fmtTime(1_000_000_000_000)
	nanoResult := fmtTime(1_000_000_000_001)

	wantSec := time.Unix(1_000_000_000_000, 0).Format("2006-01-02 15:04:05")
	wantNano := time.Unix(0, 1_000_000_000_001).Format("2006-01-02 15:04:05")

	if secResult != wantSec {
		t.Errorf("fmtTime(1e12): got %q, want %q (should be seconds path)", secResult, wantSec)
	}
	if nanoResult != wantNano {
		t.Errorf("fmtTime(1e12+1): got %q, want %q (should be nano path)", nanoResult, wantNano)
	}
	// The two results must be different dates: seconds path gives year ~33658,
	// nanoseconds path gives a date near 1970-01-01.
	if secResult == nanoResult {
		t.Errorf("fmtTime(1e12) == fmtTime(1e12+1): boundary not handled correctly")
	}
}

// TestFmtTimeOutputIsDatetimeFormat verifies the output always matches the
// "YYYY-MM-DD HH:MM:SS" shape (19 characters, fixed separators).
func TestFmtTimeOutputIsDatetimeFormat(t *testing.T) {
	for _, ts := range []int64{1, 1_000_000_000, 1_700_000_000_000_000_000} {
		got := fmtTime(ts)
		if len(got) != 19 {
			t.Errorf("fmtTime(%d) = %q: length %d, want 19 (YYYY-MM-DD HH:MM:SS)", ts, got, len(got))
		}
		if got[4] != '-' || got[7] != '-' || got[10] != ' ' || got[13] != ':' || got[16] != ':' {
			t.Errorf("fmtTime(%d) = %q: unexpected datetime format", ts, got)
		}
	}
}

// ─── fmtAge ──────────────────────────────────────────────────────────────────

// TestFmtAgeZeroReturnsNever verifies that unix==0 means "no heartbeat ever".
func TestFmtAgeZeroReturnsNever(t *testing.T) {
	if got := fmtAge(0); got != "never" {
		t.Errorf("fmtAge(0) = %q, want %q", got, "never")
	}
}

// TestFmtAgeSuffix verifies that the function selects the correct unit suffix
// (s/m/h) based on elapsed duration. Each sub-test uses a timestamp far from
// the unit boundaries to prevent flakiness from test-execution timing.
func TestFmtAgeSuffix(t *testing.T) {
	tests := []struct {
		name       string
		offset     time.Duration // how far in the past the timestamp is
		wantSuffix string
	}{
		// 10 seconds ago — well within the < 1-minute bucket.
		{"10 seconds ago → s", -10 * time.Second, "s"},
		// 30 minutes ago — comfortably in [1min, 1hr) bucket.
		{"30 minutes ago → m", -30 * time.Minute, "m"},
		// 2 hours ago — comfortably in the ≥1hr bucket.
		{"2 hours ago → h", -2 * time.Hour, "h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unix := time.Now().Add(tc.offset).Unix()
			got := fmtAge(unix)
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("fmtAge offset=%v: got %q, want suffix %q", tc.offset, got, tc.wantSuffix)
			}
		})
	}
}

// TestFmtAgeSecondsValue verifies the numeric seconds portion is in the
// expected range. Uses a generous ±3 second window to be robust against
// slow CI environments.
func TestFmtAgeSecondsValue(t *testing.T) {
	unix := time.Now().Add(-20 * time.Second).Unix()
	got := fmtAge(unix)

	// Accept 17s–23s to tolerate test-execution timing variations.
	acceptable := map[string]bool{
		"17s": true, "18s": true, "19s": true,
		"20s": true, "21s": true, "22s": true, "23s": true,
	}
	if !acceptable[got] {
		t.Errorf("fmtAge(20s ago) = %q, expected value near 20s (17s–23s)", got)
	}
}

// TestFmtAgeMinutesValue verifies the numeric minutes portion.
func TestFmtAgeMinutesValue(t *testing.T) {
	unix := time.Now().Add(-5 * time.Minute).Unix()
	got := fmtAge(unix)

	// Accept 4m–6m.
	acceptable := map[string]bool{"4m": true, "5m": true, "6m": true}
	if !acceptable[got] {
		t.Errorf("fmtAge(5m ago) = %q, expected value near 5m (4m–6m)", got)
	}
}

// TestFmtAgeHoursValue verifies the numeric hours portion.
func TestFmtAgeHoursValue(t *testing.T) {
	unix := time.Now().Add(-3 * time.Hour).Unix()
	got := fmtAge(unix)

	// Accept 2h–4h.
	acceptable := map[string]bool{"2h": true, "3h": true, "4h": true}
	if !acceptable[got] {
		t.Errorf("fmtAge(3h ago) = %q, expected value near 3h (2h–4h)", got)
	}
}

// ─── sortedKeys ──────────────────────────────────────────────────────────────

// TestSortedKeysSingleEntry verifies a single-entry map returns a slice with
// exactly that key.
func TestSortedKeysSingleEntry(t *testing.T) {
	got := sortedKeys(map[string]string{"only": "value"})
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("sortedKeys({only:value}) = %v, want [only]", got)
	}
}

// TestSortedKeysAlphabeticalOrder verifies multiple keys are returned in
// ascending lexicographic order regardless of map iteration order.
func TestSortedKeysAlphabeticalOrder(t *testing.T) {
	m := map[string]string{
		"zebra":  "z",
		"apple":  "a",
		"mango":  "m",
		"banana": "b",
	}
	got := sortedKeys(m)
	want := []string{"apple", "banana", "mango", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys len = %d, want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("sortedKeys[%d] = %q, want %q", i, got[i], k)
		}
	}
}

// TestSortedKeysPreservesAllKeys verifies the output contains every key from
// the input map (no keys dropped or duplicated).
func TestSortedKeysPreservesAllKeys(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	got := sortedKeys(m)
	seen := make(map[string]int)
	for _, k := range got {
		seen[k]++
	}
	for k := range m {
		if seen[k] != 1 {
			t.Errorf("key %q appears %d times in sortedKeys output, want 1", k, seen[k])
		}
	}
}

// ─── cleanDirContents ────────────────────────────────────────────────────────

// TestCleanDirContentsNonExistentDir verifies the function does not panic or
// return an error on a directory that does not exist.
func TestCleanDirContentsNonExistentDir(t *testing.T) {
	// Should silently no-op — not panic, not error.
	cleanDirContents(filepath.Join(t.TempDir(), "does-not-exist"))
}

// TestCleanDirContentsEmptyDir verifies the function is a no-op on an empty dir.
func TestCleanDirContentsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	cleanDirContents(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir after cleanDirContents: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("after cleanDirContents on empty dir, found %d entries, want 0", len(entries))
	}
}

// TestCleanDirContentsRemovesFiles verifies that regular files inside the
// directory are removed.
func TestCleanDirContentsRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.json", "c.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	cleanDirContents(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("after cleanDirContents, found %d entries, want 0", len(entries))
	}
}

// TestCleanDirContentsRemovesSubdirectories verifies that subdirectories (and
// their contents) are recursively removed.
func TestCleanDirContentsRemovesSubdirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile nested.txt: %v", err)
	}
	cleanDirContents(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("after cleanDirContents, found %d entries (expected subdir removed), want 0", len(entries))
	}
}

// TestCleanDirContentsMixedContent verifies that a directory containing both
// files and subdirectories is fully emptied.
func TestCleanDirContentsMixedContent(t *testing.T) {
	dir := t.TempDir()
	// Create a file and a subdir.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("y"), 0644); err != nil {
		t.Fatalf("WriteFile inner: %v", err)
	}
	cleanDirContents(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("after cleanDirContents on mixed dir, found %d entries, want 0", len(entries))
	}
}

// ─── copyFile ────────────────────────────────────────────────────────────────

// TestCopyFileBasicContent verifies that copyFile produces a destination file
// with identical content to the source.
func TestCopyFileBasicContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	content := []byte("hello, copyFile")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("WriteFile src: %v", err)
	}
	if err := copyFile(src, dst, 0644); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}
}

// TestCopyFileEmptyFile verifies that an empty source file results in an empty
// destination (edge case: zero bytes).
func TestCopyFileEmptyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := copyFile(src, dst, 0644); err != nil {
		t.Fatalf("copyFile empty: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if len(got) != 0 {
		t.Errorf("empty file copy: dst has %d bytes, want 0", len(got))
	}
}

// TestCopyFileNonExistentSrcReturnsError verifies that a missing source
// produces an error rather than silently creating an empty file.
func TestCopyFileNonExistentSrcReturnsError(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(filepath.Join(dir, "no-such-file"), filepath.Join(dir, "dst"), 0644)
	if err == nil {
		t.Error("copyFile with missing src: expected error, got nil")
	}
}

// TestCopyFileMissingDstDirReturnsError verifies that a destination whose
// parent directory does not exist returns an error.
func TestCopyFileMissingDstDirReturnsError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile src: %v", err)
	}
	dst := filepath.Join(dir, "nonexistent-subdir", "dst.txt")
	err := copyFile(src, dst, 0644)
	if err == nil {
		t.Error("copyFile with missing dst dir: expected error, got nil")
	}
}

// ─── copyDirRecursive ─────────────────────────────────────────────────────────

// TestCopyDirRecursiveBasicTree verifies that a directory tree (files and
// subdirectories) is fully and accurately replicated at the destination.
func TestCopyDirRecursiveBasicTree(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// src/
	//   top.txt
	//   sub/
	//     nested.txt
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0644); err != nil {
		t.Fatalf("WriteFile top.txt: %v", err)
	}
	sub := filepath.Join(src, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("MkdirAll sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("nested"), 0644); err != nil {
		t.Fatalf("WriteFile nested.txt: %v", err)
	}

	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copyDirRecursive: %v", err)
	}

	for _, tc := range []struct {
		rel  string
		want string
	}{
		{"top.txt", "top"},
		{filepath.Join("sub", "nested.txt"), "nested"},
	} {
		got, err := os.ReadFile(filepath.Join(dst, tc.rel))
		if err != nil {
			t.Errorf("ReadFile dst/%s: %v", tc.rel, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("dst/%s = %q, want %q", tc.rel, got, tc.want)
		}
	}
}

// TestCopyDirRecursiveEmptyDir verifies that copying an empty source directory
// succeeds without error and leaves an empty destination.
func TestCopyDirRecursiveEmptyDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copyDirRecursive empty: %v", err)
	}
}

// TestCopyDirRecursiveSingleFile verifies the single-file case (no subdirs).
func TestCopyDirRecursiveSingleFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "only.txt"), []byte("solo"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copyDirRecursive single file: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "only.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "solo" {
		t.Errorf("dst/only.txt = %q, want %q", got, "solo")
	}
}

// TestCopyDirRecursiveNonExistentSrcReturnsError verifies that a missing
// source directory returns an error rather than silently succeeding.
func TestCopyDirRecursiveNonExistentSrcReturnsError(t *testing.T) {
	dst := t.TempDir()
	err := copyDirRecursive(filepath.Join(t.TempDir(), "no-such-dir"), dst)
	if err == nil {
		t.Error("copyDirRecursive with missing src: expected error, got nil")
	}
}

// ─── AgentShowCmd – not-found error path ─────────────────────────────────────

// TestAgentShowCmdRunNotFoundReturnsError verifies that AgentShowCmd.Run returns
// a "agent not found: <name>" error when the requested agent does not exist in
// the workspace agents directory. This exercises the os.Stat → os.IsNotExist
// branch that is otherwise unreachable via the happy-path tests.
func TestAgentShowCmdRunNotFoundReturnsError(t *testing.T) {
	t.Parallel()
	_, g := initTestWS(t)

	cmd := &AgentShowCmd{Name: "no-such-agent-xyz"}
	err := cmd.Run(g)
	if err == nil {
		t.Fatal("expected error for non-existent agent, got nil")
	}
	if !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("error %q should contain 'agent not found'", err.Error())
	}
	if !strings.Contains(err.Error(), "no-such-agent-xyz") {
		t.Errorf("error %q should contain the agent name", err.Error())
	}
}

// ─── updateGoalStatusAfterPlanner – warning paths (UpdateStatus fails) ───────

// deleteGoalFile removes the goal's JSON file from the workspace so that any
// subsequent goal.UpdateStatus call will fail (simulating workspace corruption).
func deleteGoalFile(t *testing.T, ws *workspace.Workspace, goalID string) {
	t.Helper()
	if err := os.Remove(ws.GoalPath(goalID)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("deleteGoalFile: %v", err)
	}
}

// TestUpdateGoalStatusAfterPlannerWarningSuccessWithTasksGoalDeleted verifies
// that when planErr==nil, tasks exist, but goal.UpdateStatus fails (goal was
// deleted), the function still returns nil — the UpdateStatus failure is
// demoted to a warning printed to stderr, not a returned error.
func TestUpdateGoalStatusAfterPlannerWarningSuccessWithTasksGoalDeleted(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "warning-success-tasks goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "a task", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	// Delete the goal file so UpdateStatus will fail.
	deleteGoalFile(t, ws, g.ID)

	// planErr is nil → should return nil even though UpdateStatus fails.
	if got := updateGoalStatusAfterPlanner(ws, g.ID, nil); got != nil {
		t.Errorf("expected nil return when UpdateStatus fails on success+tasks path, got: %v", got)
	}
}

// TestUpdateGoalStatusAfterPlannerWarningSuccessNoTasksGoalDeleted verifies
// that when planErr==nil, no tasks exist, and goal.UpdateStatus fails, the
// function still returns nil.
func TestUpdateGoalStatusAfterPlannerWarningSuccessNoTasksGoalDeleted(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "warning-success-no-tasks goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	// Delete the goal file so UpdateStatus will fail.
	deleteGoalFile(t, ws, g.ID)

	// planErr is nil, no tasks → should return nil even though UpdateStatus fails.
	if got := updateGoalStatusAfterPlanner(ws, g.ID, nil); got != nil {
		t.Errorf("expected nil return when UpdateStatus fails on success+no-tasks path, got: %v", got)
	}
}

// TestUpdateGoalStatusAfterPlannerWarningErrorWithTasksGoalDeleted verifies that
// when planErr!=nil, tasks exist, and goal.UpdateStatus fails, the function
// returns the planner error (not the UpdateStatus error) — the UpdateStatus
// failure is a warning.
func TestUpdateGoalStatusAfterPlannerWarningErrorWithTasksGoalDeleted(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "warning-error-tasks goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}
	tk := &task.Task{GoalID: g.ID, Title: "a task", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("task.Create: %v", err)
	}

	// Delete the goal file so UpdateStatus will fail.
	deleteGoalFile(t, ws, g.ID)

	planErr := errors.New("planner crashed hard")
	got := updateGoalStatusAfterPlanner(ws, g.ID, planErr)
	if got == nil {
		t.Fatal("expected non-nil error returned, got nil")
	}
	// Must wrap the planner error, not the UpdateStatus error.
	if !strings.Contains(got.Error(), "planner exited with error") {
		t.Errorf("error %q missing 'planner exited with error'", got.Error())
	}
	if !strings.Contains(got.Error(), "planner crashed hard") {
		t.Errorf("error %q missing original planner error message", got.Error())
	}
}

// TestUpdateGoalStatusAfterPlannerWarningErrorNoTasksGoalDeleted verifies that
// when planErr!=nil, no tasks exist, and goal.UpdateStatus fails, the function
// returns the planner error (not the UpdateStatus error).
func TestUpdateGoalStatusAfterPlannerWarningErrorNoTasksGoalDeleted(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "warning-error-no-tasks goal", nil, nil)
	if err != nil {
		t.Fatalf("goal.Create: %v", err)
	}

	// Delete the goal file so UpdateStatus will fail.
	deleteGoalFile(t, ws, g.ID)

	planErr := errors.New("planner totally bombed")
	got := updateGoalStatusAfterPlanner(ws, g.ID, planErr)
	if got == nil {
		t.Fatal("expected non-nil error returned, got nil")
	}
	if !strings.Contains(got.Error(), "planner exited with error") {
		t.Errorf("error %q missing 'planner exited with error'", got.Error())
	}
}
