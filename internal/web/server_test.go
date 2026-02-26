package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ilocn/stint/internal/goal"
	"github.com/ilocn/stint/internal/task"
	"github.com/ilocn/stint/internal/workspace"
)

// newTestWS creates a temporary workspace for use in tests.
func newTestWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Init(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}
	return ws
}

// newTestServer creates an httptest.Server wired to a Server instance.
func newTestServer(t *testing.T, ws *workspace.Workspace) *httptest.Server {
	t.Helper()
	srv := &Server{
		ws:      ws,
		clients: make(map[*sseClient]struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/status", srv.handleAPIStatus)
	mux.HandleFunc("/events", srv.handleEvents)
	return httptest.NewServer(mux)
}

// TestGetRootReturns200WithHTML verifies the dashboard HTML is served at /.
func TestGetRootReturns200WithHTML(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	// The dashboard HTML uses "stint" as the tool name.
	// Check for the title element as a reliable marker that the right page was served.
	if !strings.Contains(string(body), "stint") {
		t.Errorf("response body does not contain 'stint'; body starts with: %.200s", body)
	}
}

// TestGetAPIStatusReturns200WithJSON verifies /api/status returns well-formed JSON
// with the expected top-level fields.
func TestGetAPIStatusReturns200WithJSON(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
	}

	for _, field := range []string{"goals", "updated_at"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("response JSON missing field %q; got keys: %v", field, keys(payload))
		}
	}
}

// TestGetEventsReturns200WithEventStream verifies /events has the SSE content-type.
func TestGetEventsReturns200WithEventStream(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	// Use a transport that doesn't follow redirects and doesn't buffer the body.
	client := &http.Client{
		Transport: http.DefaultTransport,
	}
	resp, err := client.Do(req)
	if err != nil {
		// Context cancellation after getting the response header is OK.
		if ctx.Err() == nil {
			t.Fatalf("GET /events: %v", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Cancel the context so the SSE handler exits cleanly.
	cancel()
}

// TestBuildStatusJSONEmptyWorkspace verifies buildStatusJSON succeeds on an
// empty workspace and produces a parseable payload with an empty goals list.
func TestBuildStatusJSONEmptyWorkspace(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	for _, field := range []string{"goals", "updated_at"} {
		if _, ok := m[field]; !ok {
			t.Errorf("JSON missing field %q; keys: %v", field, keys(m))
		}
	}

	// Goals should be nil/empty on a fresh workspace.
	if goalsVal, ok := m["goals"]; ok && goalsVal != nil {
		// goals should be an empty array or null, not a populated list.
		if arr, ok := goalsVal.([]any); ok && len(arr) != 0 {
			t.Errorf("expected empty goals, got %d entries", len(arr))
		}
	}

	if payload.UpdatedAt == 0 {
		t.Error("UpdatedAt should be non-zero")
	}
}

// TestGetNotFoundReturns404 verifies unregistered paths return 404.
func TestGetNotFoundReturns404(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/notfound")
	if err != nil {
		t.Fatalf("GET /notfound: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestAPIStatusIncludesSummaryAndTotal verifies /api/status contains all four
// top-level fields: goals, summary, total, and updated_at.
func TestAPIStatusIncludesSummaryAndTotal(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
	}

	for _, field := range []string{"goals", "summary", "total", "updated_at"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("response JSON missing field %q; got keys: %v", field, keys(payload))
		}
	}
}

// TestBuildStatusJSONWithGoalAndTasks verifies buildStatusJSON correctly
// aggregates goals, tasks, summary counts, and total from workspace state.
func TestBuildStatusJSONWithGoalAndTasks(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "build feature X", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Task 1 → done.
	tk1 := &task.Task{GoalID: g.ID, Title: "implement auth", Agent: "impl", Prompt: "do it"}
	if err := task.Create(ws, tk1); err != nil {
		t.Fatalf("Create tk1: %v", err)
	}
	c1, err := task.ClaimNext(ws, "w-1")
	if err != nil {
		t.Fatalf("ClaimNext c1: %v", err)
	}
	if err := task.Done(ws, c1.ID, "auth implemented", ""); err != nil {
		t.Fatalf("Done c1: %v", err)
	}

	// Task 2 → failed with an error message.
	tk2 := &task.Task{GoalID: g.ID, Title: "write tests", Agent: "test", Prompt: "test it"}
	if err := task.Create(ws, tk2); err != nil {
		t.Fatalf("Create tk2: %v", err)
	}
	c2, err := task.ClaimNext(ws, "w-2")
	if err != nil {
		t.Fatalf("ClaimNext c2: %v", err)
	}
	if err := task.Fail(ws, c2.ID, "tests timed out"); err != nil {
		t.Fatalf("Fail c2: %v", err)
	}

	// Task 3 → pending (created last, left in place).
	tk3 := &task.Task{GoalID: g.ID, Title: "deploy", Agent: "impl", Prompt: "deploy it"}
	if err := task.Create(ws, tk3); err != nil {
		t.Fatalf("Create tk3: %v", err)
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}

	// Exactly one goal in the workspace.
	if len(payload.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
	}
	got := payload.Goals[0]
	if got.ID != g.ID {
		t.Errorf("Goals[0].ID = %q, want %q", got.ID, g.ID)
	}
	if got.Text != "build feature X" {
		t.Errorf("Goals[0].Text = %q, want %q", got.Text, "build feature X")
	}

	// All three tasks should appear.
	if len(got.Tasks) != 3 {
		t.Fatalf("len(Tasks) = %d, want 3", len(got.Tasks))
	}

	// Summary counts must match actual task statuses.
	if payload.Summary[task.StatusDone] != 1 {
		t.Errorf("Summary[done] = %d, want 1", payload.Summary[task.StatusDone])
	}
	if payload.Summary[task.StatusFailed] != 1 {
		t.Errorf("Summary[failed] = %d, want 1", payload.Summary[task.StatusFailed])
	}
	if payload.Summary[task.StatusPending] != 1 {
		t.Errorf("Summary[pending] = %d, want 1", payload.Summary[task.StatusPending])
	}
	if payload.Total != 3 {
		t.Errorf("Total = %d, want 3", payload.Total)
	}
	if payload.UpdatedAt == 0 {
		t.Error("UpdatedAt should be non-zero")
	}
}

// TestBuildStatusJSONTaskSortOrder verifies that tasks within a goal are
// returned in Seq ascending order (primary key). Status priority is only a
// tie-breaker when tasks share the same Seq value.
//
// Tasks are given seq=1 (done), seq=2 (failed), seq=3 (pending). The expected
// output order is seq=1, seq=2, seq=3 — i.e. done first, failed second,
// pending last. This is the reverse of the old status-priority order
// (failed < pending < done), proving that Seq controls the sort.
func TestBuildStatusJSONTaskSortOrder(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "sort order goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Create tasks one at a time so each ClaimNext gets the intended task.
	// Explicit Seq values are used so the seq-primary sort is exercised.

	// Task A → seq=1, done.
	tkA := &task.Task{GoalID: g.ID, Title: "task A", Agent: "default", Prompt: "A", Seq: 1}
	if err := task.Create(ws, tkA); err != nil {
		t.Fatalf("Create tkA: %v", err)
	}
	cA, err := task.ClaimNext(ws, "w-A")
	if err != nil {
		t.Fatalf("ClaimNext A: %v", err)
	}
	if err := task.Done(ws, cA.ID, "A done", ""); err != nil {
		t.Fatalf("Done A: %v", err)
	}

	// Task B → seq=2, failed.
	tkB := &task.Task{GoalID: g.ID, Title: "task B", Agent: "default", Prompt: "B", Seq: 2}
	if err := task.Create(ws, tkB); err != nil {
		t.Fatalf("Create tkB: %v", err)
	}
	cB, err := task.ClaimNext(ws, "w-B")
	if err != nil {
		t.Fatalf("ClaimNext B: %v", err)
	}
	if err := task.Fail(ws, cB.ID, "B failed"); err != nil {
		t.Fatalf("Fail B: %v", err)
	}

	// Task C → seq=3, pending (stays in pending/).
	tkC := &task.Task{GoalID: g.ID, Title: "task C", Agent: "default", Prompt: "C", Seq: 3}
	if err := task.Create(ws, tkC); err != nil {
		t.Fatalf("Create tkC: %v", err)
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
	}
	tasks := payload.Goals[0].Tasks
	if len(tasks) != 3 {
		t.Fatalf("len(Tasks) = %d, want 3", len(tasks))
	}

	// Seq is the primary sort key: seq=1 (done) first, seq=2 (failed) second,
	// seq=3 (pending) last. This ordering differs from the status-priority order
	// (failed < pending < done), confirming Seq governs the sort.
	if tasks[0].Status != task.StatusDone {
		t.Errorf("tasks[0].Status = %q, want done (seq=1)", tasks[0].Status)
	}
	if tasks[1].Status != task.StatusFailed {
		t.Errorf("tasks[1].Status = %q, want failed (seq=2)", tasks[1].Status)
	}
	if tasks[2].Status != task.StatusPending {
		t.Errorf("tasks[2].Status = %q, want pending (seq=3)", tasks[2].Status)
	}
}

// TestBuildStatusJSONReasonField verifies the reason field behavior:
//   - failed tasks: reason = error message (ErrorMsg)
//   - done tasks: reason = result summary (Result.Summary)
//   - cancelled tasks (no error msg): reason = default "Cancelled"
//   - cancelled tasks (with error msg): reason = ErrorMsg
func TestBuildStatusJSONReasonField(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "reason field goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Create a failed task with a specific error message.
	tkFail := &task.Task{GoalID: g.ID, Title: "failing task", Agent: "default", Prompt: "fail me"}
	if err := task.Create(ws, tkFail); err != nil {
		t.Fatalf("Create tkFail: %v", err)
	}
	cFail, err := task.ClaimNext(ws, "w-fail")
	if err != nil {
		t.Fatalf("ClaimNext fail: %v", err)
	}
	if err := task.Fail(ws, cFail.ID, "something went wrong"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Create a done task with a specific summary.
	tkDone := &task.Task{GoalID: g.ID, Title: "succeeding task", Agent: "default", Prompt: "do it"}
	if err := task.Create(ws, tkDone); err != nil {
		t.Fatalf("Create tkDone: %v", err)
	}
	cDone, err := task.ClaimNext(ws, "w-done")
	if err != nil {
		t.Fatalf("ClaimNext done: %v", err)
	}
	if err := task.Done(ws, cDone.ID, "all good", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	// Create a cancelled task (pending → cancelled, no error message).
	tkCancel := &task.Task{GoalID: g.ID, Title: "cancelled task", Agent: "default", Prompt: "cancel me"}
	if err := task.Create(ws, tkCancel); err != nil {
		t.Fatalf("Create tkCancel: %v", err)
	}
	if err := task.Cancel(ws, tkCancel.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
	}

	tasksByStatus := make(map[string]taskJSON)
	for _, tj := range payload.Goals[0].Tasks {
		tasksByStatus[tj.Status] = tj
	}

	// Failed task: reason should carry the error message.
	failedTask, ok := tasksByStatus[task.StatusFailed]
	if !ok {
		t.Fatal("no failed task in payload")
	}
	if !strings.Contains(failedTask.Reason, "something went wrong") {
		t.Errorf("failed task Reason = %q, want to contain %q", failedTask.Reason, "something went wrong")
	}

	// Done task: reason should be the result summary.
	doneTask, ok := tasksByStatus[task.StatusDone]
	if !ok {
		t.Fatal("no done task in payload")
	}
	if doneTask.Reason != "all good" {
		t.Errorf("done task Reason = %q, want %q", doneTask.Reason, "all good")
	}

	// Cancelled task (no error message): reason should default to "Cancelled".
	cancelledTask, ok := tasksByStatus[task.StatusCancelled]
	if !ok {
		t.Fatal("no cancelled task in payload")
	}
	if cancelledTask.Reason != "Cancelled" {
		t.Errorf("cancelled task Reason = %q, want %q", cancelledTask.Reason, "Cancelled")
	}
}

// TestBuildStatusJSONReasonFieldDoneNoSummary verifies that a done task with
// a nil Result (or empty Summary) produces an empty reason field.
func TestBuildStatusJSONReasonFieldDoneNoSummary(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "done no summary goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	tk := &task.Task{GoalID: g.ID, Title: "done task", Agent: "default", Prompt: "do it"}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create task: %v", err)
	}
	c, err := task.ClaimNext(ws, "w-1")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	// Done with empty summary.
	if err := task.Done(ws, c.ID, "", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 1 || len(payload.Goals[0].Tasks) != 1 {
		t.Fatalf("unexpected goals/tasks count")
	}
	tj := payload.Goals[0].Tasks[0]
	if tj.Status != task.StatusDone {
		t.Fatalf("task status = %q, want done", tj.Status)
	}
	if tj.Reason != "" {
		t.Errorf("done task with empty summary: Reason = %q, want empty", tj.Reason)
	}
}

// TestBuildStatusJSONReasonFieldStatusCoexist verifies that the taskJSON
// payload includes both the "status" and "reason" fields together so the
// frontend JS can use t.status === 'failed' to conditionally apply the red
// CSS class (task-reason--error) to the reason span.
//
// Key assertions:
//   - Failed tasks have Status == "failed" AND a non-empty Reason (error msg)
//   - Done tasks have Status == "done" AND a non-empty Reason (result summary)
func TestBuildStatusJSONReasonFieldStatusCoexist(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "status+reason coexist goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Create and fail a task.
	tkFail := &task.Task{GoalID: g.ID, Title: "fail task", Agent: "default", Prompt: "fail"}
	if err := task.Create(ws, tkFail); err != nil {
		t.Fatalf("Create tkFail: %v", err)
	}
	cFail, err := task.ClaimNext(ws, "w-fail")
	if err != nil {
		t.Fatalf("ClaimNext fail: %v", err)
	}
	if err := task.Fail(ws, cFail.ID, "error: something broke"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Create and complete a task with a summary.
	tkDone := &task.Task{GoalID: g.ID, Title: "done task", Agent: "default", Prompt: "done"}
	if err := task.Create(ws, tkDone); err != nil {
		t.Fatalf("Create tkDone: %v", err)
	}
	cDone, err := task.ClaimNext(ws, "w-done")
	if err != nil {
		t.Fatalf("ClaimNext done: %v", err)
	}
	if err := task.Done(ws, cDone.ID, "task completed successfully", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
	}

	tasksByStatus := make(map[string]taskJSON)
	for _, tj := range payload.Goals[0].Tasks {
		tasksByStatus[tj.Status] = tj
	}

	// Failed task must have Status == "failed" so the frontend JS expression
	// `t.status === 'failed'` evaluates to true and applies task-reason--error.
	failedTask, ok := tasksByStatus[task.StatusFailed]
	if !ok {
		t.Fatal("no failed task in payload")
	}
	if failedTask.Status != task.StatusFailed {
		t.Errorf("failedTask.Status = %q, want %q (required for frontend red-color logic)", failedTask.Status, task.StatusFailed)
	}
	if failedTask.Reason == "" {
		t.Error("failedTask.Reason is empty, want non-empty error message")
	}
	if !strings.Contains(failedTask.Reason, "something broke") {
		t.Errorf("failedTask.Reason = %q, want to contain %q", failedTask.Reason, "something broke")
	}

	// Done task must have Status == "done" so the frontend JS expression
	// `t.status === 'failed'` evaluates to false and the reason uses
	// the default (non-red) task-reason class.
	doneTask, ok := tasksByStatus[task.StatusDone]
	if !ok {
		t.Fatal("no done task in payload")
	}
	if doneTask.Status != task.StatusDone {
		t.Errorf("doneTask.Status = %q, want %q (required for frontend non-red-color logic)", doneTask.Status, task.StatusDone)
	}
	if doneTask.Reason == "" {
		t.Error("doneTask.Reason is empty, want non-empty summary")
	}
	if doneTask.Reason != "task completed successfully" {
		t.Errorf("doneTask.Reason = %q, want %q", doneTask.Reason, "task completed successfully")
	}
}

// TestBuildStatusJSONSameStatusSeqOrder verifies that tasks sharing the same
// status are sorted by Seq ascending within that status bucket.
func TestBuildStatusJSONSameStatusSeqOrder(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "seq order goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	// Create pending tasks with out-of-order Seq values.
	for _, seq := range []int{3, 1, 2} {
		tk := &task.Task{
			GoalID: g.ID,
			Title:  "seq-" + string(rune('0'+seq)),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create task seq=%d: %v", seq, err)
		}
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
	}
	tasks := payload.Goals[0].Tasks
	if len(tasks) != 3 {
		t.Fatalf("len(Tasks) = %d, want 3", len(tasks))
	}

	// All tasks are pending; within the same status they must be in Seq order.
	// The title encodes the seq digit so we can verify ordering via title.
	expectedTitles := []string{"seq-1", "seq-2", "seq-3"}
	for i, tj := range tasks {
		if tj.Title != expectedTitles[i] {
			t.Errorf("tasks[%d].Title = %q, want %q", i, tj.Title, expectedTitles[i])
		}
	}
}

// TestBuildStatusJSONMultiGoalSeqOrdering verifies that seq-based ordering is
// applied independently within each goal — tasks in goal A are sorted by their
// own seq values and tasks in goal B are sorted by their own seq values,
// regardless of how the seq spaces interleave across goals.
func TestBuildStatusJSONMultiGoalSeqOrdering(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	gA, err := goal.Create(ws, "goal A", nil, nil)
	if err != nil {
		t.Fatalf("Create goal A: %v", err)
	}
	gB, err := goal.Create(ws, "goal B", nil, nil)
	if err != nil {
		t.Fatalf("Create goal B: %v", err)
	}

	// Goal A tasks: created with seq 3, 1, 2 (out of order).
	for _, seq := range []int{3, 1, 2} {
		tk := &task.Task{
			GoalID: gA.ID,
			Title:  fmt.Sprintf("A-%d", seq),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create A seq=%d: %v", seq, err)
		}
	}

	// Goal B tasks: created with seq 10, 30, 20 (out of order, larger values
	// to ensure goal A's seq numbers don't accidentally satisfy goal B's check).
	for _, seq := range []int{30, 10, 20} {
		tk := &task.Task{
			GoalID: gB.ID,
			Title:  fmt.Sprintf("B-%d", seq),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create B seq=%d: %v", seq, err)
		}
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != 2 {
		t.Fatalf("len(Goals) = %d, want 2", len(payload.Goals))
	}

	// Index goals by ID for deterministic assertions.
	goalsByID := make(map[string]goalJSON)
	for _, g := range payload.Goals {
		goalsByID[g.ID] = g
	}

	// Goal A: tasks should appear in seq 1, 2, 3 order.
	aGoal, ok := goalsByID[gA.ID]
	if !ok {
		t.Fatalf("goal A (%s) missing from payload", gA.ID)
	}
	if len(aGoal.Tasks) != 3 {
		t.Fatalf("goal A: len(Tasks) = %d, want 3", len(aGoal.Tasks))
	}
	for i, wantTitle := range []string{"A-1", "A-2", "A-3"} {
		if aGoal.Tasks[i].Title != wantTitle {
			t.Errorf("goal A task[%d].Title = %q, want %q", i, aGoal.Tasks[i].Title, wantTitle)
		}
	}

	// Goal B: tasks should appear in seq 10, 20, 30 order.
	bGoal, ok := goalsByID[gB.ID]
	if !ok {
		t.Fatalf("goal B (%s) missing from payload", gB.ID)
	}
	if len(bGoal.Tasks) != 3 {
		t.Fatalf("goal B: len(Tasks) = %d, want 3", len(bGoal.Tasks))
	}
	for i, wantTitle := range []string{"B-10", "B-20", "B-30"} {
		if bGoal.Tasks[i].Title != wantTitle {
			t.Errorf("goal B task[%d].Title = %q, want %q", i, bGoal.Tasks[i].Title, wantTitle)
		}
	}
}

// TestBuildStatusJSONGoalStatusNonEmpty verifies that goals returned by
// buildStatusJSON always carry a non-empty Status field.  The status field
// is what the filter bar's pill buttons read — if it's empty the buttons
// have nothing to count and the filter silently breaks.
func TestBuildStatusJSONGoalStatusNonEmpty(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	// Create three goals; goal.Create sets Status = "queued" by default.
	texts := []string{"alpha goal", "beta goal", "gamma goal"}
	for _, text := range texts {
		if _, err := goal.Create(ws, text, nil, nil); err != nil {
			t.Fatalf("Create goal %q: %v", text, err)
		}
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != len(texts) {
		t.Fatalf("len(Goals) = %d, want %d", len(payload.Goals), len(texts))
	}
	for _, g := range payload.Goals {
		if g.Status == "" {
			t.Errorf("goal %q has empty Status; filter bar cannot function without it", g.ID)
		}
	}
}

// TestBuildStatusJSONAllGoalStatuses creates one goal per valid status
// (queued, planning, active, done, failed) and verifies that buildStatusJSON
// returns each goal with the exact status that was persisted.  This is the
// primary correctness check for the status filter bar: every pill button
// must match the statuses actually present on goals.
func TestBuildStatusJSONAllGoalStatuses(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	wantStatuses := []string{
		goal.StatusQueued,
		goal.StatusPlanning,
		goal.StatusActive,
		goal.StatusDone,
		goal.StatusFailed,
	}

	// Create a goal for each status value, then update it to the target status.
	// goal.Create deduplicates by text, so each goal needs a unique text.
	for _, status := range wantStatuses {
		g, err := goal.Create(ws, "goal for status "+status, nil, nil)
		if err != nil {
			t.Fatalf("Create goal for status %q: %v", status, err)
		}
		if status != goal.StatusQueued {
			// goal.Create always starts at queued; advance to desired status.
			if err := goal.UpdateStatus(ws, g.ID, status); err != nil {
				t.Fatalf("UpdateStatus %q → %q: %v", g.ID, status, err)
			}
		}
	}

	payload, err := buildStatusJSON(ws)
	if err != nil {
		t.Fatalf("buildStatusJSON: %v", err)
	}
	if len(payload.Goals) != len(wantStatuses) {
		t.Fatalf("len(Goals) = %d, want %d", len(payload.Goals), len(wantStatuses))
	}

	// Build a set of the statuses we got back so ordering doesn't matter.
	gotStatuses := make(map[string]int)
	for _, g := range payload.Goals {
		gotStatuses[g.Status]++
	}
	for _, s := range wantStatuses {
		if gotStatuses[s] != 1 {
			t.Errorf("status %q: got count %d in payload, want 1", s, gotStatuses[s])
		}
	}
}

// TestBuildStatusJSONGoalStatusReflectsUpdate verifies that after a goal's
// status is changed via goal.UpdateStatus the updated value is immediately
// reflected in the next buildStatusJSON call — i.e., there is no stale cache
// between updates and the JSON response.
func TestBuildStatusJSONGoalStatusReflectsUpdate(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	g, err := goal.Create(ws, "update-reflect goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	transitions := []string{
		goal.StatusPlanning,
		goal.StatusActive,
		goal.StatusDone,
	}
	for _, status := range transitions {
		if err := goal.UpdateStatus(ws, g.ID, status); err != nil {
			t.Fatalf("UpdateStatus → %q: %v", status, err)
		}
		payload, err := buildStatusJSON(ws)
		if err != nil {
			t.Fatalf("buildStatusJSON after update to %q: %v", status, err)
		}
		if len(payload.Goals) != 1 {
			t.Fatalf("len(Goals) = %d, want 1", len(payload.Goals))
		}
		if payload.Goals[0].Status != status {
			t.Errorf("after UpdateStatus(%q): Goals[0].Status = %q, want %q",
				status, payload.Goals[0].Status, status)
		}
	}
}

// TestAPIStatusGoalStatusFieldPresent verifies the /api/status HTTP endpoint
// returns each goal with a non-empty "status" string in the JSON body.
// This is an end-to-end check from HTTP request to response payload, ensuring
// the field survives JSON marshalling and content-type negotiation.
func TestAPIStatusGoalStatusFieldPresent(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	g, err := goal.Create(ws, "http status field goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	// Parse into a typed structure to inspect the goals array.
	var payload struct {
		Goals []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"goals"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
	}

	if len(payload.Goals) == 0 {
		t.Fatal("expected at least one goal in response, got none")
	}
	for _, got := range payload.Goals {
		if got.Status == "" {
			t.Errorf("goal %q has empty status in HTTP response; filter bar needs it", got.ID)
		}
	}

	// Specifically confirm our created goal appears with the expected status.
	found := false
	for _, got := range payload.Goals {
		if got.ID == g.ID {
			found = true
			if got.Status != goal.StatusQueued {
				t.Errorf("goal %q: HTTP status = %q, want %q", g.ID, got.Status, goal.StatusQueued)
			}
		}
	}
	if !found {
		t.Errorf("created goal %q not found in /api/status response", g.ID)
	}
}

// TestAPIStatusGoalStatusAfterUpdate creates a goal, advances it through
// several statuses, and after each change confirms that a fresh GET /api/status
// returns the correct updated status — verifying filter pill counts stay
// accurate as goals progress through their lifecycle.
func TestAPIStatusGoalStatusAfterUpdate(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	ts := newTestServer(t, ws)
	defer ts.Close()

	g, err := goal.Create(ws, "lifecycle goal", nil, nil)
	if err != nil {
		t.Fatalf("Create goal: %v", err)
	}

	for _, wantStatus := range []string{
		goal.StatusPlanning,
		goal.StatusActive,
		goal.StatusFailed,
	} {
		if err := goal.UpdateStatus(ws, g.ID, wantStatus); err != nil {
			t.Fatalf("UpdateStatus → %q: %v", wantStatus, err)
		}

		resp, err := http.Get(ts.URL + "/api/status")
		if err != nil {
			t.Fatalf("GET /api/status: %v", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatalf("reading body: %v", readErr)
		}

		var payload struct {
			Goals []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"goals"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
		}

		gotStatus := ""
		for _, got := range payload.Goals {
			if got.ID == g.ID {
				gotStatus = got.Status
				break
			}
		}
		if gotStatus == "" {
			t.Fatalf("goal %q not found in response after transition to %q", g.ID, wantStatus)
		}
		if gotStatus != wantStatus {
			t.Errorf("goal %q: status = %q, want %q", g.ID, gotStatus, wantStatus)
		}
	}
}

// keys returns sorted map keys for readable error messages.
func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// extractSSEData extracts the data payload from a pre-formatted SSE string.
// The new pollLoop and broadcastRaw send "event: ...\ndata: ...\n\n" format.
func extractSSEData(sseMsg string) string {
	for _, line := range strings.Split(sseMsg, "\n") {
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	return sseMsg
}

// ---- broadcast() tests ----

// TestBroadcastSendsToAllClients verifies that broadcastRaw delivers a message
// to every connected SSE client.
func TestBroadcastSendsToAllClients(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	c1 := &sseClient{ch: make(chan string, 16)}
	c2 := &sseClient{ch: make(chan string, 16)}
	srv.mu.Lock()
	srv.clients[c1] = struct{}{}
	srv.clients[c2] = struct{}{}
	srv.mu.Unlock()

	srv.broadcastRaw("hello")

	for i, c := range []*sseClient{c1, c2} {
		select {
		case msg := <-c.ch:
			if msg != "hello" {
				t.Errorf("client[%d] got %q, want %q", i, msg, "hello")
			}
		default:
			t.Errorf("client[%d] did not receive message", i)
		}
	}
}

// TestBroadcastDropsSlowConsumer verifies that broadcastRaw does not block
// when a client's channel is full (slow consumer).
func TestBroadcastDropsSlowConsumer(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Unbuffered channel — select default always fires.
	slow := &sseClient{ch: make(chan string)}
	fast := &sseClient{ch: make(chan string, 16)}
	srv.mu.Lock()
	srv.clients[slow] = struct{}{}
	srv.clients[fast] = struct{}{}
	srv.mu.Unlock()

	done := make(chan struct{})
	go func() {
		srv.broadcastRaw("msg")
		close(done)
	}()

	select {
	case <-done:
		// broadcast returned without blocking — pass
	case <-time.After(time.Second):
		t.Error("broadcast blocked on slow consumer")
	}

	// Fast client should still have received the message.
	select {
	case msg := <-fast.ch:
		if msg != "msg" {
			t.Errorf("fast client got %q, want %q", msg, "msg")
		}
	default:
		t.Error("fast client did not receive message")
	}
}

// TestBroadcastWithNoClients verifies broadcastRaw does not panic when the
// clients map is empty.
func TestBroadcastWithNoClients(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}
	// Must not panic.
	srv.broadcastRaw("ignored")
}

// ---- handleEvents tests ----

// nonFlushableWriter implements http.ResponseWriter but intentionally omits
// http.Flusher so the streaming-not-supported branch in handleEvents fires.
type nonFlushableWriter struct {
	code   int
	header http.Header
	body   strings.Builder
}

func (w *nonFlushableWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *nonFlushableWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.body.Write(b)
}
func (w *nonFlushableWriter) WriteHeader(code int) { w.code = code }

// TestHandleEventsNonFlusher verifies handleEvents returns 500 when the
// response writer does not implement http.Flusher.
func TestHandleEventsNonFlusher(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	w := &nonFlushableWriter{}
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	srv.handleEvents(w, r)

	if w.code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.code)
	}
	if !strings.Contains(w.body.String(), "streaming not supported") {
		t.Errorf("body = %q, want 'streaming not supported'", w.body.String())
	}
}

// TestHandleEventsReceivesBroadcastMessage verifies that a connected SSE
// client receives a message sent via broadcast after receiving the initial
// snapshot.
func TestHandleEventsReceivesBroadcastMessage(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("/events", srv.handleEvents)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	// Collect SSE data lines on a channel.
	msgCh := make(chan string, 10)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				msgCh <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	// Wait for the initial snapshot — proves the client is registered.
	select {
	case <-msgCh:
		// initial snapshot received
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE snapshot")
	}

	// Broadcast a specific message using the SSE status event format.
	const wantMsg = `{"type":"test-broadcast"}`
	srv.broadcastRaw(fmt.Sprintf("event: status\ndata: %s\n\n", wantMsg))

	// The broadcast should arrive as the next SSE data line.
	select {
	case msg := <-msgCh:
		if msg != wantMsg {
			t.Errorf("received SSE msg %q, want %q", msg, wantMsg)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for broadcast SSE message")
	}
}

// ---- Serve() test ----

// TestServeStartsHTTPServer verifies that Serve() listens on the given address
// and correctly serves HTTP requests.
func TestServeStartsHTTPServer(t *testing.T) {
	// Not parallel: binds a real port.
	ws := newTestWS(t)

	// Grab a free port then release it so Serve can bind it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	go func() { _ = Serve(context.Background(), ws, addr, nil) }()

	// Retry until the server is up (up to 2 s).
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/api/status")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("could not reach Serve after 2s: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// ---- handleAPIStatus error-path test ----

// TestHandleAPIStatusBuildStatusJSONError verifies that handleAPIStatus
// returns 500 when buildStatusJSON fails (workspace goals directory corrupted).
func TestHandleAPIStatusBuildStatusJSONError(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	// Replace the goals directory with a regular file so os.ReadDir returns
	// a non-ENOENT error, causing goal.List → buildStatusJSON to fail.
	if err := os.RemoveAll(ws.GoalsDir()); err != nil {
		t.Fatalf("RemoveAll goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("WriteFile over goals dir: %v", err)
	}

	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	srv.handleAPIStatus(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// ---- buildStatusJSON error-path tests ----

// TestBuildStatusJSONListGoalsError verifies that buildStatusJSON propagates
// an error when goal.List fails due to a corrupted goals directory.
func TestBuildStatusJSONListGoalsError(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	if err := os.RemoveAll(ws.GoalsDir()); err != nil {
		t.Fatalf("RemoveAll goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("WriteFile over goals dir: %v", err)
	}

	_, err := buildStatusJSON(ws)
	if err == nil {
		t.Fatal("expected error from buildStatusJSON, got nil")
	}
	if !strings.Contains(err.Error(), "listing goals") {
		t.Errorf("error = %q, want to contain 'listing goals'", err.Error())
	}
}

// TestBuildStatusJSONListTasksError verifies that buildStatusJSON propagates
// an error when task.ListAll fails due to a corrupted task status directory.
func TestBuildStatusJSONListTasksError(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	// Corrupt the pending tasks directory so ListByStatus returns an error.
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll pending dir: %v", err)
	}
	if err := os.WriteFile(ws.PendingDir(), []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("WriteFile over pending dir: %v", err)
	}

	_, err := buildStatusJSON(ws)
	if err == nil {
		t.Fatal("expected error from buildStatusJSON, got nil")
	}
	if !strings.Contains(err.Error(), "listing tasks") {
		t.Errorf("error = %q, want to contain 'listing tasks'", err.Error())
	}
}

// ---- pollLoop() test ----

// TestPollLoopBroadcastsToClients verifies that pollLoop sends valid JSON
// workspace status to connected SSE clients after a tick.
//
// This test intentionally waits up to 4 seconds for the 2-second ticker.
func TestPollLoopBroadcastsToClients(t *testing.T) {
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	client := &sseClient{ch: make(chan string, 16)}
	srv.mu.Lock()
	srv.clients[client] = struct{}{}
	srv.mu.Unlock()

	go srv.pollLoop(context.Background())

	select {
	case msg := <-client.ch:
		// pollLoop now sends pre-formatted SSE: "event: status\ndata: {...}\n\n"
		jsonData := extractSSEData(msg)
		var p statusJSON
		if err := json.Unmarshal([]byte(jsonData), &p); err != nil {
			t.Errorf("pollLoop sent invalid JSON: %v; msg: %s", err, msg)
		}
	case <-time.After(4 * time.Second):
		t.Error("pollLoop did not broadcast within 4 seconds")
	}
}

// TestPollLoopHandlesBuildStatusJSONError verifies that pollLoop logs the
// error and continues (does not panic or stop) when buildStatusJSON fails.
//
// This test waits up to 4 seconds for the 2-second ticker.
func TestPollLoopHandlesBuildStatusJSONError(t *testing.T) {
	ws := newTestWS(t)

	// Break the goals directory so buildStatusJSON will fail on every tick.
	if err := os.RemoveAll(ws.GoalsDir()); err != nil {
		t.Fatalf("RemoveAll goals dir: %v", err)
	}
	if err := os.WriteFile(ws.GoalsDir(), []byte("not-a-dir"), 0644); err != nil {
		t.Fatalf("WriteFile over goals dir: %v", err)
	}

	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Connect a client so we can verify no message is sent on error.
	client := &sseClient{ch: make(chan string, 16)}
	srv.mu.Lock()
	srv.clients[client] = struct{}{}
	srv.mu.Unlock()

	go srv.pollLoop(context.Background())

	// Wait slightly longer than one tick (2s) to let the error path execute.
	// pollLoop logs the error and continues; no message must reach the client.
	select {
	case msg := <-client.ch:
		t.Errorf("pollLoop should not broadcast on error, but sent: %q", msg)
	case <-time.After(3 * time.Second):
		// Correct: error path ran, no message broadcast.
	}
}

// ---- templates.go test ----

// TestDashboardHTMLContainsExpectedContent verifies that the embedded dashboard
// HTML constant is non-empty and contains required structural markers.
func TestDashboardHTMLContainsExpectedContent(t *testing.T) {
	t.Parallel()
	if dashboardHTML == "" {
		t.Fatal("dashboardHTML constant is empty")
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"stint",
		"/api/status",
		"/events",
		"EventSource",
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboardHTML missing expected string %q", want)
		}
	}
}

// TestDashboardHTMLReasonColorConditional verifies that the embedded dashboard
// HTML template contains the JS conditional logic that applies the red CSS
// class (task-reason--error) only to failed tasks. This ensures the frontend
// correctly styles the reason field:
//   - status == "failed"  → class "task-reason task-reason--error" (red)
//   - any other status    → class "task-reason" (muted/default color)
func TestDashboardHTMLReasonColorConditional(t *testing.T) {
	t.Parallel()
	// Verify the JS conditional expression for the CSS class.
	wantConditional := `t.status === 'failed' ? 'task-reason task-reason--error' : 'task-reason'`
	if !strings.Contains(dashboardHTML, wantConditional) {
		t.Errorf("dashboardHTML missing JS conditional for reason color:\nwant: %q", wantConditional)
	}

	// Verify the CSS rules for both classes are present.
	for _, wantCSS := range []string{
		".task-reason",
		".task-reason--error",
	} {
		if !strings.Contains(dashboardHTML, wantCSS) {
			t.Errorf("dashboardHTML missing CSS class %q", wantCSS)
		}
	}

	// Verify that the error class uses the red color variable.
	wantRedRule := ".task-reason--error { color: var(--red)"
	if !strings.Contains(dashboardHTML, wantRedRule) {
		t.Errorf("dashboardHTML missing red color rule, want to contain %q", wantRedRule)
	}
}

// ─── Tests from main branch ─────────────────────────────────────────────────

// TestPollLoopExitsOnContextCancel verifies that pollLoop terminates when
// its context is cancelled — previously used "for range ticker.C" which
// blocks forever since ticker.Stop() does not close the channel.
func TestPollLoopExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{
		ws:      ws,
		clients: make(map[*sseClient]struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.pollLoop(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// pollLoop exited after context cancellation — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("pollLoop did not exit after context cancellation (goroutine leak)")
	}
}
