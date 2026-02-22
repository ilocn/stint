# E2E Tests

End-to-end tests for `st`. Build-tagged `e2e` so they only run when explicitly invoked.

## How to run

```bash
# Fast tests only — no claude required, < 2 min
go test -v -tags e2e -run 'TestInit|TestGoal|TestTask|TestStatus|TestAgent|TestRecover|TestVersion|TestRepo|TestHeartbeat|TestLog|TestAllBuiltin|TestStatusWith' -timeout 2m ./e2e/

# All tests including real claude (requires /Users/zidiwang/.local/bin/claude)
go test -v -tags e2e -timeout 30m ./e2e/

# Single test
go test -v -tags e2e -run TestFullPlannerFlow -timeout 12m ./e2e/
```

## What is tested

### Group 1 — CLI (no claude, ~1 s each, 22 tests)

Every `st` CLI command: `init`, `goal add/list/show/cancel`, `task add/list/show/done/fail/retry/cancel`, `status`, `recover`, `heartbeat`, `worker list`, `log`, `agent add/list/show`, `repo add/list`, `version`.

### Group 2 — Supervisor + real claude (20–120 s each, 10 tests)

| Test | What it proves |
|------|---------------|
| `TestSupervisorCompletesSimpleTask` | Supervisor dispatches task → worker creates file → task done → goal done → worktree cleaned |
| `TestMergeTaskIntegratesIntoGoalBranch` | T1 impl → merge-task integrates into goal branch → T3 sees merged file |
| `TestParallelGoalsOnSameRepo` | Two goals on same repo both dispatch within 30 s (no serialization) |
| `TestMergeAgentMergesToMain` | Full 3-step pipeline: impl → merge-task → merge to main |
| `TestLogCommandAfterRealTask` | `st log <id>` returns real claude output after task runs |
| `TestContextInjectionBetweenTasks` | T1 summary appears in T2's log (context_from wiring) |
| `TestFullPlannerFlow` | **Core path**: queued goal → supervisor spawns planner → planner creates tasks → workers complete → goal done |
| `TestWorkerTaskFailureHandling` | Worker calls `st task fail` → task goes to failed with error → blocked dep stays blocked |
| `TestWorkerDeathCausesFailedThenRetrySucceeds` | `monitorWorker` marks task failed on SIGKILL → retry → new worker completes |
| `TestHealthCheckDetectsOrphanedWorker` | Injected dead-PID worker record → `healthCheck` prunes it within 30 s → task re-dispatched |
| `TestStartupRecoveryPreservesAndRestartsTask` | Pre-populated dead-PID crash state → `startupRecovery` resets task on boot → real worker completes |

## Architecture

```
TestMain
  └── buildSt()   ← compiles ./cmd/st once to a temp dir
                      all tests share this binary

Each test gets:
  - t.TempDir() workspace   (auto-cleaned after test)
  - fresh git repo(s)
  - test-worker agent (maxTurns: 5, bypassPermissions)

Group 2 tests additionally:
  - start supervisor via exec.CommandContext
  - supervisor env has CLAUDECODE stripped (allows nested claude)
  - supervisor env has PATH set so workers find both st and claude
```
