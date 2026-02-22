package task_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/stint/internal/task"
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

func makeTask(t *testing.T, ws *workspace.Workspace, goalID, title string) *task.Task {
	t.Helper()
	tk := &task.Task{
		GoalID: goalID,
		Title:  title,
		Agent:  "default",
		Prompt: "do stuff",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tk
}

func TestCreateAndGet(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "implement feature")
	if tk.ID == "" {
		t.Error("ID is empty")
	}

	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if got.Title != "implement feature" {
		t.Error("title not preserved")
	}
}

func TestClaimNextAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	makeTask(t, ws, "g-001", "the one task")

	const goroutines = 20
	results := make([]bool, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := task.ClaimNext(ws, "w-test")
			results[idx] = (err == nil)
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("exactly 1 goroutine should claim the task, got %d", successes)
	}
}

func TestClaimNextNoDeps(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	t1 := makeTask(t, ws, "g-001", "task 1")

	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != t1.ID {
		t.Errorf("claimed %s, want %s", claimed.ID, t1.ID)
	}

	// Task should now be in running.
	_, status, _ := task.Get(ws, t1.ID)
	if status != task.StatusRunning {
		t.Errorf("status = %s, want running", status)
	}
}

func TestClaimNextSkipsDependentTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	t1 := makeTask(t, ws, "g-001", "first")
	t2 := &task.Task{
		GoalID:     "g-001",
		Title:      "second",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{t1.ID},
	}
	if err := task.Create(ws, t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}

	// Only t1 has no deps and should be claimable.
	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != t1.ID {
		t.Errorf("expected to claim t1 (%s), got %s", t1.ID, claimed.ID)
	}

	// Try again — t2 still has unmet dep.
	_, err = task.ClaimNext(ws, "w-002")
	if err != task.ErrNoTask {
		t.Errorf("expected ErrNoTask, got %v", err)
	}
}

func TestClaimNextUnblocksWhenDepDone(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	t1 := makeTask(t, ws, "g-001", "first")
	t2 := &task.Task{
		GoalID:     "g-001",
		Title:      "second",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{t1.ID},
	}
	if err := task.Create(ws, t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}

	// Claim and complete t1.
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext t1: %v", err)
	}
	if err := task.Done(ws, t1.ID, "done", ""); err != nil {
		t.Fatalf("Done t1: %v", err)
	}

	// Now t2 should be claimable.
	claimed, err := task.ClaimNext(ws, "w-002")
	if err != nil {
		t.Fatalf("ClaimNext after dep done: %v", err)
	}
	if claimed.ID != t2.ID {
		t.Errorf("expected to claim t2 (%s), got %s", t2.ID, claimed.ID)
	}
}

func TestDoneTransition(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := task.Done(ws, tk.ID, "it worked", "my-branch"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusDone {
		t.Errorf("status = %s, want done", status)
	}
	if got.Result == nil || got.Result.Summary != "it worked" {
		t.Error("result not set")
	}
	if got.Result.Branch != "my-branch" {
		t.Error("branch not set")
	}
}

func TestFailTransition(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := task.Fail(ws, tk.ID, "it broke"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusFailed {
		t.Errorf("status = %s, want failed", status)
	}
	if got.ErrorMsg != "it broke" {
		t.Errorf("ErrorMsg = %s, want 'it broke'", got.ErrorMsg)
	}
}

func TestRetryTransition(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "error"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if err := task.Retry(ws, tk.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
}

func TestRetryResetsRetryCount(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")

	// Simulate two health-check resets to build up RetryCount.
	for i := 0; i < 2; i++ {
		if _, err := task.ClaimNext(ws, fmt.Sprintf("w-%03d", i)); err != nil {
			t.Fatalf("ClaimNext %d: %v", i, err)
		}
		if err := task.ResetToQueue(ws, tk.ID); err != nil {
			t.Fatalf("ResetToQueue %d: %v", i, err)
		}
	}

	got, _, _ := task.Get(ws, tk.ID)
	if got.RetryCount != 2 {
		t.Fatalf("RetryCount before Retry = %d, want 2", got.RetryCount)
	}

	// Fail the task and then call Retry — RetryCount must be reset to 0.
	if _, err := task.ClaimNext(ws, "w-002"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "broken"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := task.Retry(ws, tk.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if got.RetryCount != 0 {
		t.Errorf("RetryCount = %d after Retry, want 0", got.RetryCount)
	}
}

func TestCancelPendingTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")

	if err := task.Cancel(ws, tk.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusCancelled {
		t.Errorf("status = %s, want cancelled", status)
	}
}

func TestResetToQueue(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := task.ResetToQueue(ws, tk.ID); err != nil {
		t.Fatalf("ResetToQueue: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if got.WorkerID != "" {
		t.Error("WorkerID should be cleared")
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
}

func TestResetToQueueIncrementsRetryCount(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")

	// Reset twice (within limit) and verify RetryCount increments.
	for i := 1; i <= 2; i++ {
		if _, err := task.ClaimNext(ws, fmt.Sprintf("w-%03d", i)); err != nil {
			t.Fatalf("ClaimNext iteration %d: %v", i, err)
		}
		if err := task.ResetToQueue(ws, tk.ID); err != nil {
			t.Fatalf("ResetToQueue iteration %d: %v", i, err)
		}
		got, status, err := task.Get(ws, tk.ID)
		if err != nil {
			t.Fatalf("Get iteration %d: %v", i, err)
		}
		if status != task.StatusPending {
			t.Errorf("iteration %d: status = %s, want pending", i, status)
		}
		if got.RetryCount != i {
			t.Errorf("iteration %d: RetryCount = %d, want %d", i, got.RetryCount, i)
		}
	}
}

func TestResetToQueueExceedsMaxRetriesMarksFailed(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")

	// Perform MaxResetRetries resets; the last one should permanently fail the task.
	for i := 1; i <= task.MaxResetRetries; i++ {
		if _, err := task.ClaimNext(ws, fmt.Sprintf("w-%03d", i)); err != nil {
			t.Fatalf("ClaimNext iteration %d: %v", i, err)
		}
		if err := task.ResetToQueue(ws, tk.ID); err != nil {
			t.Fatalf("ResetToQueue iteration %d: %v", i, err)
		}
	}

	// After MaxResetRetries resets the task must be in failed/.
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get after max retries: %v", err)
	}
	if status != task.StatusFailed {
		t.Errorf("status = %s, want failed", status)
	}
	if got.RetryCount != task.MaxResetRetries {
		t.Errorf("RetryCount = %d, want %d", got.RetryCount, task.MaxResetRetries)
	}
	if got.ErrorMsg == "" {
		t.Error("ErrorMsg should be set when max retries exceeded")
	}
	if got.CompletedAt == 0 {
		t.Error("CompletedAt should be set when max retries exceeded")
	}
}

func TestListAll(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	t1 := makeTask(t, ws, "g-001", "task 1")
	t2 := makeTask(t, ws, "g-001", "task 2")

	// Claim both tasks.
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext t1: %v", err)
	}
	if _, err := task.ClaimNext(ws, "w-002"); err != nil {
		t.Fatalf("ClaimNext t2: %v", err)
	}
	// Complete t2 (it is now running).
	if err := task.Done(ws, t2.ID, "done", ""); err != nil {
		t.Fatalf("Done t2: %v", err)
	}
	_ = t1

	all, err := task.ListAll(ws)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListAll returned %d, want 2", len(all))
	}
}

func TestListForGoal(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	makeTask(t, ws, "g-001", "for goal 1")
	makeTask(t, ws, "g-001", "also goal 1")
	makeTask(t, ws, "g-002", "for goal 2")

	tasks, err := task.ListForGoal(ws, "g-001")
	if err != nil {
		t.Fatalf("ListForGoal: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("ListForGoal returned %d, want 2", len(tasks))
	}
}

func TestClaimNextSeqOrdering(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create tasks out of order with explicit Seq values.
	for _, seq := range []int{3, 1, 2} {
		tk := &task.Task{
			GoalID: "g-001",
			Title:  fmt.Sprintf("task-seq-%d", seq),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create seq=%d: %v", seq, err)
		}
	}

	// Claim them one by one; should arrive in ascending Seq order.
	for _, expectedSeq := range []int{1, 2, 3} {
		claimed, err := task.ClaimNext(ws, "w-001")
		if err != nil {
			t.Fatalf("ClaimNext for expected seq=%d: %v", expectedSeq, err)
		}
		if claimed.Seq != expectedSeq {
			t.Errorf("claimed Seq=%d, want %d", claimed.Seq, expectedSeq)
		}
		if err := task.Done(ws, claimed.ID, "done", ""); err != nil {
			t.Fatalf("Done seq=%d: %v", expectedSeq, err)
		}
	}
}

func TestListByStatusSeqOrdering(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create tasks with out-of-order Seq values.
	for _, seq := range []int{5, 2, 8, 1, 3} {
		tk := &task.Task{
			GoalID: "g-001",
			Title:  fmt.Sprintf("task-seq-%d", seq),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create seq=%d: %v", seq, err)
		}
	}

	tasks, err := task.ListByStatus(ws, task.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(tasks) != 5 {
		t.Fatalf("len = %d, want 5", len(tasks))
	}

	// Expect ascending Seq order.
	expectedSeqs := []int{1, 2, 3, 5, 8}
	for i, tk := range tasks {
		if tk.Seq != expectedSeqs[i] {
			t.Errorf("tasks[%d].Seq = %d, want %d", i, tk.Seq, expectedSeqs[i])
		}
	}
}

// TestClaimNextCrossGoalSeqIsolation verifies that tasks from different goals
// are claimed in global ascending Seq order and that one goal's seq numbering
// does not displace tasks from another goal out of order.
//
// Scenario: Goal A has seq=1 and seq=5; Goal B has seq=3 (sitting between
// A's two tasks). Expected claim order is A-seq1 → B-seq3 → A-seq5,
// confirming the global sort is applied across goals and neither goal's tasks
// are grouped ahead of the other's.
func TestClaimNextCrossGoalSeqIsolation(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	type spec struct {
		goalID string
		seq    int
		title  string
	}
	// Intentionally created out of insertion order so seq, not file order, drives claims.
	specs := []spec{
		{"g-A", 5, "A-seq-5"},
		{"g-B", 3, "B-seq-3"},
		{"g-A", 1, "A-seq-1"},
	}
	for _, s := range specs {
		tk := &task.Task{
			GoalID: s.goalID,
			Title:  s.title,
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    s.seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create %s: %v", s.title, err)
		}
	}

	// Claim all three tasks; they must arrive in global Seq ascending order.
	expected := []spec{
		{"g-A", 1, "A-seq-1"},
		{"g-B", 3, "B-seq-3"},
		{"g-A", 5, "A-seq-5"},
	}
	for i, want := range expected {
		claimed, err := task.ClaimNext(ws, "w-001")
		if err != nil {
			t.Fatalf("ClaimNext #%d: %v", i+1, err)
		}
		if claimed.GoalID != want.goalID || claimed.Seq != want.seq {
			t.Errorf("claim #%d: got (GoalID=%s, Seq=%d), want (GoalID=%s, Seq=%d)",
				i+1, claimed.GoalID, claimed.Seq, want.goalID, want.seq)
		}
		if err := task.Done(ws, claimed.ID, "done", ""); err != nil {
			t.Fatalf("Done #%d: %v", i+1, err)
		}
	}
}

// TestClaimNextSameSeqFallsBackToCreatedAt verifies that when two pending tasks
// share the same Seq number, the one created earlier is claimed first.
func TestClaimNextSameSeqFallsBackToCreatedAt(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create two tasks with the same Seq — first will have an earlier CreatedAt.
	tkFirst := &task.Task{
		GoalID: "g-001",
		Title:  "first-created",
		Agent:  "default",
		Prompt: "do stuff",
		Seq:    7,
	}
	if err := task.Create(ws, tkFirst); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	tkSecond := &task.Task{
		GoalID: "g-001",
		Title:  "second-created",
		Agent:  "default",
		Prompt: "do stuff",
		Seq:    7,
	}
	if err := task.Create(ws, tkSecond); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	// The task created earlier (tkFirst) must be claimed before tkSecond.
	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != tkFirst.ID {
		t.Errorf("claimed ID=%s, want %s (first-created)", claimed.ID, tkFirst.ID)
	}
}

func TestStressClaimNoDuplicates(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create 50 tasks.
	taskIDs := make([]string, 50)
	for i := range taskIDs {
		tk := &task.Task{GoalID: "g-001", Title: "task", Agent: "default", Prompt: "do it"}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create task %d: %v", i, err)
		}
		taskIDs[i] = tk.ID
	}

	const goroutines = 10
	claimed := make(chan string, 60)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			workerID := fmt.Sprintf("w-%d", g)
			for {
				t, err := task.ClaimNext(ws, workerID)
				if err == task.ErrNoTask {
					return
				}
				if err != nil {
					return
				}
				claimed <- t.ID
			}
		}(g)
	}
	wg.Wait()
	close(claimed)

	seen := make(map[string]int)
	for id := range claimed {
		seen[id]++
	}

	// Verify no task was claimed more than once.
	for id, count := range seen {
		if count > 1 {
			t.Errorf("task %s was claimed %d times", id, count)
		}
	}
	if len(seen) != 50 {
		t.Errorf("claimed %d unique tasks, want 50", len(seen))
	}
}

// TestTaskNewIDFormat is a regression test verifying that task.NewID() still
// produces correctly formatted IDs after the time-sortable ID migration.
func TestTaskNewIDFormat(t *testing.T) {
	t.Parallel()
	id := task.NewID()
	if !strings.HasPrefix(id, "t-") {
		t.Errorf("task.NewID() = %q, want prefix \"t-\"", id)
	}
	suffix := id[2:] // strip "t-"
	if len(suffix) != 11 {
		t.Errorf("task.NewID() suffix %q has len=%d, want 11", suffix, len(suffix))
	}
	for _, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("task.NewID() = %q contains non-base36 char %q", id, c)
		}
	}
}

// TestTaskNewIDTemporalSort verifies that task IDs generated at different
// times sort lexicographically in temporal order.
func TestTaskNewIDTemporalSort(t *testing.T) {
	t.Parallel()
	id1 := task.NewID()
	time.Sleep(2 * time.Millisecond)
	id2 := task.NewID()
	if id1[2:] >= id2[2:] {
		t.Errorf("task IDs not temporally sorted: %q >= %q", id1, id2)
	}
}

// ── GetByStatus edge cases ────────────────────────────────────────────────────

func TestGetByStatusUnknownStatus(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	_, _, err := task.GetByStatus(ws, "t-fake", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown status, got nil")
	}
	if !strings.Contains(err.Error(), "unknown status") {
		t.Errorf("error = %q, want 'unknown status'", err)
	}
}

func TestGetByStatusMalformedJSON(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	id := "t-malformed"
	// Write malformed JSON directly into the pending directory.
	path := filepath.Join(ws.PendingDir(), id+".json")
	if err := os.WriteFile(path, []byte("{not valid json}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, _, err := task.GetByStatus(ws, id, task.StatusPending)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "malformed task") {
		t.Errorf("error = %q, want 'malformed task'", err)
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	_, _, err := task.Get(ws, "t-does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

// ── ListByStatus edge cases ───────────────────────────────────────────────────

func TestListByStatusUnknownStatus(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	_, err := task.ListByStatus(ws, "invalid-status")
	if err == nil {
		t.Fatal("expected error for unknown status, got nil")
	}
	if !strings.Contains(err.Error(), "unknown status") {
		t.Errorf("error = %q, want 'unknown status'", err)
	}
}

// ── NextSeq ───────────────────────────────────────────────────────────────────

func TestNextSeqEmpty(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	seq, err := task.NextSeq(ws, "g-empty")
	if err != nil {
		t.Fatalf("NextSeq: %v", err)
	}
	if seq != 1 {
		t.Errorf("NextSeq = %d, want 1 for goal with no tasks", seq)
	}
}

func TestNextSeqWithTasks(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	for _, seq := range []int{1, 2, 5} {
		tk := &task.Task{
			GoalID: "g-seq",
			Title:  fmt.Sprintf("task-%d", seq),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    seq,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create seq=%d: %v", seq, err)
		}
	}
	seq, err := task.NextSeq(ws, "g-seq")
	if err != nil {
		t.Fatalf("NextSeq: %v", err)
	}
	if seq != 6 {
		t.Errorf("NextSeq = %d, want 6 (max=5 → next=6)", seq)
	}
}

// ── listDir with malformed / non-JSON files ───────────────────────────────────

func TestListDirMalformedJSONSkipped(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a valid task.
	good := makeTask(t, ws, "g-001", "good task")

	// Inject a malformed JSON file into pending/.
	bad := filepath.Join(ws.PendingDir(), "t-bad.json")
	if err := os.WriteFile(bad, []byte("{broken"), 0644); err != nil {
		t.Fatalf("WriteFile bad: %v", err)
	}

	tasks, err := task.ListByStatus(ws, task.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	// Only the valid task should be returned.
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (malformed should be skipped)", len(tasks))
	}
	if tasks[0].ID != good.ID {
		t.Errorf("got task %s, want %s", tasks[0].ID, good.ID)
	}
}

func TestListDirNonJSONFileSkipped(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	makeTask(t, ws, "g-001", "valid task")

	// Write a non-.json file and a subdirectory into pending/.
	if err := os.WriteFile(filepath.Join(ws.PendingDir(), "not-a-task.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(filepath.Join(ws.PendingDir(), "subdir"), 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	tasks, err := task.ListByStatus(ws, task.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (non-json files/dirs should be skipped)", len(tasks))
	}
}

// ── Create with already-met dependencies ─────────────────────────────────────

func TestCreateWithMetDepsGoesToPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create and complete a dep task so it lands in done/.
	dep := makeTask(t, ws, "g-001", "dep")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Done(ws, dep.ID, "dep done", ""); err != nil {
		t.Fatalf("Done dep: %v", err)
	}

	// Create a task whose dep is already done — it should go straight to pending/.
	tk := &task.Task{
		GoalID:     "g-001",
		Title:      "dependent",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep.ID},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending (dep already done)", status)
	}
}

// ── Block transition ──────────────────────────────────────────────────────────

func TestBlockHappyPath(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "will be blocked")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := task.Block(ws, tk.ID, "waiting for human input"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusBlocked {
		t.Errorf("status = %s, want blocked", status)
	}
	if got.ErrorMsg != "waiting for human input" {
		t.Errorf("ErrorMsg = %q, want 'waiting for human input'", got.ErrorMsg)
	}
}

func TestBlockNotRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "pending task")
	// Task is still pending, not running.
	err := task.Block(ws, tk.ID, "reason")
	if err == nil {
		t.Fatal("expected error blocking a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want 'not running'", err)
	}
}

func TestBlockNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.Block(ws, "t-nonexistent", "reason")
	if err == nil {
		t.Fatal("expected error blocking a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

func TestBlockClearsHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "block clears hb")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.WriteHeartbeat(ws, tk.ID); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if err := task.Block(ws, tk.ID, "blocked"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	// Heartbeat file should be gone.
	if ts := task.ReadHeartbeat(ws, tk.ID); ts != 0 {
		t.Error("heartbeat should be cleared after Block")
	}
}

// ── Done error paths ──────────────────────────────────────────────────────────

func TestDoneNotRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	// Task is still pending.
	err := task.Done(ws, tk.ID, "summary", "branch")
	if err == nil {
		t.Fatal("expected error calling Done on a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want 'not running'", err)
	}
}

func TestDoneNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.Done(ws, "t-nonexistent", "summary", "branch")
	if err == nil {
		t.Fatal("expected error calling Done on a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

// ── Fail error paths ──────────────────────────────────────────────────────────

func TestFailNotRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	err := task.Fail(ws, tk.ID, "reason")
	if err == nil {
		t.Fatal("expected error calling Fail on a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want 'not running'", err)
	}
}

func TestFailNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.Fail(ws, "t-nonexistent", "reason")
	if err == nil {
		t.Fatal("expected error calling Fail on a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

// ── Cancel edge cases ─────────────────────────────────────────────────────────

func TestCancelBlockedTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a task with unmet deps so it lands in blocked/.
	dep := task.NewID() // non-existent dep
	tk := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked task",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, status, _ := task.Get(ws, tk.ID)
	if status != task.StatusBlocked {
		t.Fatalf("expected task to be blocked, got %s", status)
	}

	if err := task.Cancel(ws, tk.ID); err != nil {
		t.Fatalf("Cancel blocked task: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get after cancel: %v", err)
	}
	if status != task.StatusCancelled {
		t.Errorf("status = %s, want cancelled", status)
	}
}

func TestCancelRunningTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	// Running task cannot be cancelled.
	err := task.Cancel(ws, tk.ID)
	if err == nil {
		t.Fatal("expected error cancelling a running task, got nil")
	}
	if !strings.Contains(err.Error(), "can only cancel") {
		t.Errorf("error = %q, want 'can only cancel'", err)
	}
}

func TestCancelNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.Cancel(ws, "t-nonexistent")
	if err == nil {
		t.Fatal("expected error cancelling a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

// ── Retry edge cases ──────────────────────────────────────────────────────────

func TestRetryBlockedTaskWithMetDeps(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create dep and complete it.
	dep := makeTask(t, ws, "g-001", "dep")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext dep: %v", err)
	}
	if err := task.Done(ws, dep.ID, "dep done", ""); err != nil {
		t.Fatalf("Done dep: %v", err)
	}

	// Create task that depends on dep (already done), claim and block it.
	tk := &task.Task{
		GoalID:     "g-001",
		Title:      "to be blocked",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep.ID},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// task goes to pending since dep is done. Claim and then block it.
	if _, err := task.ClaimNext(ws, "w-002"); err != nil {
		t.Fatalf("ClaimNext tk: %v", err)
	}
	if err := task.Block(ws, tk.ID, "manual block"); err != nil {
		t.Fatalf("Block: %v", err)
	}

	// Retry: dep is done so task should go to pending.
	if err := task.Retry(ws, tk.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending (dep is done)", status)
	}
}

func TestRetryBlockedTaskWithUnmetDeps(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	dep := task.NewID() // non-existent dep
	tk := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked with unmet dep",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Retry a blocked task whose deps are still unmet → stays blocked.
	if err := task.Retry(ws, tk.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusBlocked {
		t.Errorf("status = %s, want blocked (deps still unmet)", status)
	}
}

func TestRetryInvalidState(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "pending task")
	// Cannot retry a pending task.
	err := task.Retry(ws, tk.ID)
	if err == nil {
		t.Fatal("expected error retrying a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "can only retry") {
		t.Errorf("error = %q, want 'can only retry'", err)
	}
}

func TestRetryNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.Retry(ws, "t-nonexistent")
	if err == nil {
		t.Fatal("expected error retrying a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

// ── ResetToQueue edge cases ───────────────────────────────────────────────────

func TestResetToQueueNotRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "pending task")
	err := task.ResetToQueue(ws, tk.ID)
	if err == nil {
		t.Fatal("expected error resetting a pending task, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want 'not running'", err)
	}
}

func TestResetToQueueNotFound(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	err := task.ResetToQueue(ws, "t-nonexistent")
	if err == nil {
		t.Fatal("expected error resetting a non-existent task, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err)
	}
}

func TestResetToQueueWithUnmetDeps(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create a task with a dep that won't be done.
	dep := task.NewID()
	tk := &task.Task{
		GoalID:     "g-001",
		Title:      "has unmet dep",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep},
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Manually move the task from blocked/ to running/ to simulate recovery scenario.
	blockedData, err := os.ReadFile(ws.BlockedPath(tk.ID))
	if err != nil {
		t.Fatalf("ReadFile blocked: %v", err)
	}
	if err := os.WriteFile(ws.RunningPath(tk.ID), blockedData, 0644); err != nil {
		t.Fatalf("WriteFile running: %v", err)
	}
	os.Remove(ws.BlockedPath(tk.ID))

	if err := task.ResetToQueue(ws, tk.ID); err != nil {
		t.Fatalf("ResetToQueue: %v", err)
	}
	// Deps still unmet → task goes to blocked/.
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusBlocked {
		t.Errorf("status = %s, want blocked (deps unmet)", status)
	}
}

// ── Heartbeat edge case ───────────────────────────────────────────────────────

func TestReadHeartbeatMalformedContent(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Write non-integer content to the heartbeat file.
	if err := os.WriteFile(ws.HeartbeatPath("t-bad-hb"), []byte("not-a-number"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ts := task.ReadHeartbeat(ws, "t-bad-hb")
	if ts != 0 {
		t.Errorf("ReadHeartbeat with malformed content = %d, want 0", ts)
	}
}

// ── ClaimNext edge: empty pending directory ───────────────────────────────────

func TestClaimNextEmptyPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	_, err := task.ClaimNext(ws, "w-001")
	if err != task.ErrNoTask {
		t.Errorf("ClaimNext on empty workspace = %v, want ErrNoTask", err)
	}
}

// ── UnblockReady: blocked task with met deps moves to pending ─────────────────

func TestUnblockReadyMovesTaskToPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	t1 := makeTask(t, ws, "g-001", "first")
	t2 := &task.Task{
		GoalID:     "g-001",
		Title:      "second (blocked)",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{t1.ID},
	}
	if err := task.Create(ws, t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}

	// Verify t2 starts as blocked.
	_, status, _ := task.Get(ws, t2.ID)
	if status != task.StatusBlocked {
		t.Fatalf("t2 should be blocked before dep is done, got %s", status)
	}

	// Complete t1.
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext t1: %v", err)
	}
	if err := task.Done(ws, t1.ID, "done", ""); err != nil {
		t.Fatalf("Done t1: %v", err)
	}

	// t2 should now be in pending.
	_, status, err := task.Get(ws, t2.ID)
	if err != nil {
		t.Fatalf("Get t2: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("t2 status = %s, want pending after dep done", status)
	}
}

// ── ListAll: populated workspace ─────────────────────────────────────────────

func TestListAllWithMixedStatuses(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create tasks with explicit Seq to control claim order.
	tasks := []*task.Task{
		{GoalID: "g-001", Title: "will-be-done", Seq: 1, Agent: "default", Prompt: "p"},
		{GoalID: "g-001", Title: "will-be-failed", Seq: 2, Agent: "default", Prompt: "p"},
		{GoalID: "g-001", Title: "will-be-running", Seq: 3, Agent: "default", Prompt: "p"},
	}
	for _, tk := range tasks {
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create %s: %v", tk.Title, err)
		}
	}
	tkDone := tasks[0]
	tkFail := tasks[1]
	tkRun := tasks[2]

	// Claim and complete task-1 (seq=1 → done).
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext tkDone: %v", err)
	}
	if err := task.Done(ws, tkDone.ID, "done", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	// Claim and fail task-2 (seq=2 → failed).
	if _, err := task.ClaimNext(ws, "w-002"); err != nil {
		t.Fatalf("ClaimNext tkFail: %v", err)
	}
	if err := task.Fail(ws, tkFail.ID, "broken"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// Claim task-3 (seq=3 → running, leave running).
	if _, err := task.ClaimNext(ws, "w-003"); err != nil {
		t.Fatalf("ClaimNext tkRun: %v", err)
	}
	_ = tkRun

	// Create a pending task (no deps, not claimed).
	pendingTk := &task.Task{GoalID: "g-001", Title: "still-pending", Seq: 4, Agent: "default", Prompt: "p"}
	if err := task.Create(ws, pendingTk); err != nil {
		t.Fatalf("Create pending: %v", err)
	}

	// Create a cancelled task.
	cancelTk := &task.Task{GoalID: "g-001", Title: "will-be-cancelled", Seq: 5, Agent: "default", Prompt: "p"}
	if err := task.Create(ws, cancelTk); err != nil {
		t.Fatalf("Create cancel: %v", err)
	}
	if err := task.Cancel(ws, cancelTk.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// Create a blocked task.
	dep := task.NewID()
	blockedTk := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked task",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep},
	}
	if err := task.Create(ws, blockedTk); err != nil {
		t.Fatalf("Create blocked: %v", err)
	}

	all, err := task.ListAll(ws)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 6 {
		t.Errorf("ListAll = %d tasks, want 6", len(all))
	}
}

// ── ListForGoal: cross-goal isolation ────────────────────────────────────────

func TestListForGoalEmpty(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	makeTask(t, ws, "g-other", "other goal task")

	tasks, err := task.ListForGoal(ws, "g-empty")
	if err != nil {
		t.Fatalf("ListForGoal: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("ListForGoal = %d tasks, want 0", len(tasks))
	}
}

// ── Done clears heartbeat ─────────────────────────────────────────────────────

func TestDoneClearsHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test hb clear")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.WriteHeartbeat(ws, tk.ID); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if err := task.Done(ws, tk.ID, "done", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if ts := task.ReadHeartbeat(ws, tk.ID); ts != 0 {
		t.Error("heartbeat should be cleared after Done")
	}
}

// ── Fail clears heartbeat ─────────────────────────────────────────────────────

func TestFailClearsHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test hb clear fail")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.WriteHeartbeat(ws, tk.ID); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "broke"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if ts := task.ReadHeartbeat(ws, tk.ID); ts != 0 {
		t.Error("heartbeat should be cleared after Fail")
	}
}

// ── ResetToQueue clears heartbeat ────────────────────────────────────────────

func TestResetToQueueClearsHeartbeat(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test hb clear reset")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.WriteHeartbeat(ws, tk.ID); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	if err := task.ResetToQueue(ws, tk.ID); err != nil {
		t.Fatalf("ResetToQueue: %v", err)
	}
	if ts := task.ReadHeartbeat(ws, tk.ID); ts != 0 {
		t.Error("heartbeat should be cleared after ResetToQueue")
	}
}

// ── ClaimNext: sets WorkerID and ClaimedAt ────────────────────────────────────

func TestClaimNextSetsWorkerFields(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	makeTask(t, ws, "g-001", "test")
	before := time.Now().Unix()
	claimed, err := task.ClaimNext(ws, "w-xyz")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.WorkerID != "w-xyz" {
		t.Errorf("WorkerID = %q, want 'w-xyz'", claimed.WorkerID)
	}
	if claimed.ClaimedAt < before {
		t.Errorf("ClaimedAt = %d, should be >= %d", claimed.ClaimedAt, before)
	}
}

// ── Create: default agent assigned ───────────────────────────────────────────

func TestCreateDefaultAgent(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := &task.Task{
		GoalID: "g-001",
		Title:  "no agent set",
		Prompt: "do stuff",
	}
	if err := task.Create(ws, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Agent != "impl" {
		t.Errorf("Agent = %q, want 'impl'", got.Agent)
	}
}

// ── OS error paths (filesystem manipulation) ──────────────────────────────────

// replaceWithFile removes the directory at path and creates a regular file in
// its place to simulate a non-IsNotExist OS error on subsequent ReadDir/ReadFile.
func replaceWithFile(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("RemoveAll %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("not-a-directory"), 0644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestListDirNonNotExistError replaces pending/ with a regular file so that
// os.ReadDir returns ENOTDIR (not IsNotExist). This covers:
//   - listDir's "return nil, err" branch
//   - ListAll's "return nil, err" branch
//   - ListForGoal's "return nil, err" branch
//   - NextSeq's "return 1, err" branch
func TestListDirNonNotExistError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Replace the pending directory with a regular file.
	replaceWithFile(t, ws.PendingDir())

	// NextSeq calls ListForGoal → ListAll → ListByStatus("pending") → listDir → error.
	_, err := task.NextSeq(ws, "g-test")
	if err == nil {
		t.Fatal("NextSeq should return error when pending/ is not a directory, got nil")
	}
}

// TestListDirUnreadableFileSkipped replaces a task JSON file with a directory so
// that os.ReadFile on the "file" returns an error. listDir must skip it and
// return the valid remaining tasks.
func TestListDirUnreadableFileSkipped(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	good := makeTask(t, ws, "g-001", "valid task")

	// Inject a directory entry where a .json file is expected to be.
	badPath := filepath.Join(ws.PendingDir(), "t-unreadable.json")
	if err := os.Mkdir(badPath, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	tasks, err := task.ListByStatus(ws, task.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (unreadable entry should be skipped)", len(tasks))
	}
	if tasks[0].ID != good.ID {
		t.Errorf("got task %s, want %s", tasks[0].ID, good.ID)
	}
}

// TestGetByStatusReadFileError triggers a non-IsNotExist OS error inside
// GetByStatus by replacing the task JSON file with a directory.
func TestGetByStatusReadFileError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	id := "t-get-error"
	// Create a directory where the JSON file would be.
	dirPath := filepath.Join(ws.PendingDir(), id+".json")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	_, _, err := task.GetByStatus(ws, id, task.StatusPending)
	if err == nil {
		t.Fatal("expected error from GetByStatus when task file is a directory, got nil")
	}
}

// TestClaimNextReadDirError replaces the pending directory with a regular file
// so that os.ReadDir returns a non-IsNotExist error.
func TestClaimNextReadDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	replaceWithFile(t, ws.PendingDir())

	_, err := task.ClaimNext(ws, "w-001")
	if err == nil {
		t.Fatal("ClaimNext should return error when pending/ is not a directory, got nil")
	}
	if err == task.ErrNoTask {
		t.Fatal("ClaimNext should return OS error, not ErrNoTask")
	}
}

// TestClaimNextMalformedJSONInPending puts a malformed JSON file in pending/ to
// cover ClaimNext's unmarshal-error continue branch.
func TestClaimNextMalformedJSONInPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	good := makeTask(t, ws, "g-001", "good task")

	// Inject a malformed JSON file.
	badPath := filepath.Join(ws.PendingDir(), "t-badjson.json")
	if err := os.WriteFile(badPath, []byte("{not json}"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// ClaimNext should skip the malformed file and claim the valid task.
	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != good.ID {
		t.Errorf("claimed %s, want %s", claimed.ID, good.ID)
	}
}

// TestClaimNextSkipsNonJSONFilesInPending covers the continue branch in
// ClaimNext's initial scan when directory entries with non-.json extensions
// or directories are encountered in pending/.
func TestClaimNextSkipsNonJSONFilesInPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	good := makeTask(t, ws, "g-001", "good task")

	// Inject a non-.json file into pending/.
	txtPath := filepath.Join(ws.PendingDir(), "not-a-task.txt")
	if err := os.WriteFile(txtPath, []byte("some text"), 0644); err != nil {
		t.Fatalf("WriteFile txt: %v", err)
	}

	// Inject a subdirectory into pending/.
	subDir := filepath.Join(ws.PendingDir(), "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// ClaimNext must skip both non-json entries and claim the valid task.
	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != good.ID {
		t.Errorf("claimed %s, want %s", claimed.ID, good.ID)
	}
}

// TestDoneWriteToDirError claims a task and removes done/ so that
// writeToDir returns an error.
func TestDoneWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	// Remove done/ so writeToDir fails.
	if err := os.RemoveAll(ws.DoneDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.Done(ws, tk.ID, "summary", "branch")
	if err == nil {
		t.Fatal("Done should return error when done/ dir is gone, got nil")
	}
}

// TestFailWriteToDirError claims a task and removes failed/ so that
// writeToDir returns an error.
func TestFailWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := os.RemoveAll(ws.FailedDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.Fail(ws, tk.ID, "reason")
	if err == nil {
		t.Fatal("Fail should return error when failed/ dir is gone, got nil")
	}
}

// TestBlockWriteToDirError claims a task and removes blocked/ so that
// writeToDir returns an error.
func TestBlockWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := os.RemoveAll(ws.BlockedDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.Block(ws, tk.ID, "reason")
	if err == nil {
		t.Fatal("Block should return error when blocked/ dir is gone, got nil")
	}
}

// TestCancelWriteToDirError creates a pending task and removes cancelled/ so
// that writeToDir returns an error.
func TestCancelWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if err := os.RemoveAll(ws.CancelledDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.Cancel(ws, tk.ID)
	if err == nil {
		t.Fatal("Cancel should return error when cancelled/ dir is gone, got nil")
	}
}

// TestRetryWriteToDirError fails a task and removes pending/ so that
// Retry's writeToDir call fails.
func TestRetryWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "broken"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.Retry(ws, tk.ID)
	if err == nil {
		t.Fatal("Retry should return error when pending/ dir is gone, got nil")
	}
}

// TestResetToQueueWriteToDirError claims a task and removes pending/ so that
// ResetToQueue's writeToDir call fails.
func TestResetToQueueWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err := task.ResetToQueue(ws, tk.ID)
	if err == nil {
		t.Fatal("ResetToQueue should return error when pending/ dir is gone, got nil")
	}
}

// TestUnblockReadyListByStatusError replaces blocked/ with a regular file so
// that ListByStatus(blocked) fails inside UnblockReady.
func TestUnblockReadyListByStatusError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	replaceWithFile(t, ws.BlockedDir())

	err := task.UnblockReady(ws)
	if err == nil {
		t.Fatal("UnblockReady should return error when blocked/ is not a directory, got nil")
	}
}

// TestUnblockReadyWriteToDirError creates a blocked task whose dep is met, then
// removes pending/ so that writeToDir fails when UnblockReady tries to move it.
func TestUnblockReadyWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	// Create and complete the dep.
	dep := makeTask(t, ws, "g-001", "dep")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Done(ws, dep.ID, "done", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	// Create a task blocked on the now-done dep.
	blocked := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked on done dep",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep.ID},
	}
	// Dep is done, so Create puts it in pending, not blocked.
	// Manually place it in blocked to simulate the scenario.
	blocked.ID = task.NewID()
	if err := task.Create(ws, blocked); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Move to blocked manually (task is in pending, move to blocked to set up scenario).
	src := filepath.Join(ws.PendingDir(), blocked.ID+".json")
	dst := filepath.Join(ws.BlockedDir(), blocked.ID+".json")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("WriteFile to blocked: %v", err)
	}
	os.Remove(src)

	// Now remove pending/ so writeToDir fails.
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll pending: %v", err)
	}

	// UnblockReady sees the blocked task with a met dep and tries to move to pending/,
	// which now doesn't exist.
	err = task.UnblockReady(ws)
	if err == nil {
		t.Fatal("UnblockReady should return error when pending/ dir is gone, got nil")
	}
}

// TestDoneUnblockReadyError verifies that when UnblockReady fails inside Done,
// Done still returns nil (the error is intentionally discarded with //nolint:errcheck).
func TestDoneUnblockReadyError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Step 1: create and complete a dep task (lands in done/).
	dep := makeTask(t, ws, "g-001", "dep task")
	if _, err := task.ClaimNext(ws, "w-dep"); err != nil {
		t.Fatalf("ClaimNext dep: %v", err)
	}
	if err := task.Done(ws, dep.ID, "dep done", ""); err != nil {
		t.Fatalf("Done dep: %v", err)
	}

	// Step 2: create task2 whose dep is now done, so Create puts it in pending/.
	// Move it to blocked/ manually to simulate a blocked task with a met dep.
	task2 := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked with met dep",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep.ID},
	}
	if err := task.Create(ws, task2); err != nil {
		t.Fatalf("Create task2: %v", err)
	}
	src2 := filepath.Join(ws.PendingDir(), task2.ID+".json")
	dst2 := filepath.Join(ws.BlockedDir(), task2.ID+".json")
	data2, err := os.ReadFile(src2)
	if err != nil {
		t.Fatalf("ReadFile task2 from pending: %v", err)
	}
	if err := os.WriteFile(dst2, data2, 0644); err != nil {
		t.Fatalf("WriteFile task2 to blocked: %v", err)
	}
	os.Remove(src2)

	// Step 3: create the task we will call Done on, and claim it.
	tk := makeTask(t, ws, "g-001", "main task")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext main: %v", err)
	}

	// Step 4: remove pending/ so that UnblockReady fails when it tries to move
	// task2 (blocked, dep now met) into pending/.
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll pending: %v", err)
	}

	// Done should still succeed: writeToDir to done/ works,
	// UnblockReady error is silently discarded (//nolint:errcheck).
	if err := task.Done(ws, tk.ID, "summary", "branch"); err != nil {
		t.Errorf("Done returned unexpected error: %v", err)
	}
	// Verify the task actually moved to done/.
	_, status, err2 := task.Get(ws, tk.ID)
	if err2 != nil {
		t.Fatalf("Get: %v", err2)
	}
	if status != task.StatusDone {
		t.Errorf("status = %s, want done", status)
	}
}

// ── ClaimNext: pending dir does not exist ─────────────────────────────────────

func TestClaimNextPendingDirNotExist(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := os.RemoveAll(ws.PendingDir()); err != nil {
		t.Fatalf("RemoveAll pending: %v", err)
	}
	_, err := task.ClaimNext(ws, "w-001")
	if err != task.ErrNoTask {
		t.Errorf("ClaimNext on missing pending dir = %v, want ErrNoTask", err)
	}
}

// ── listDir: status directory does not exist ──────────────────────────────────

func TestListByStatusDirNotExist(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if err := os.RemoveAll(ws.RunningDir()); err != nil {
		t.Fatalf("RemoveAll running: %v", err)
	}
	tasks, err := task.ListByStatus(ws, task.StatusRunning)
	if err != nil {
		t.Fatalf("ListByStatus on nonexistent dir returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("ListByStatus = %d tasks, want 0", len(tasks))
	}
}

// ── ResetToQueue: writeToDir error at max-retries boundary ────────────────────

func TestResetToQueueMaxRetriesWriteToDirError(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "test")

	// Perform MaxResetRetries-1 resets to bring RetryCount to the threshold.
	for i := 1; i < task.MaxResetRetries; i++ {
		if _, err := task.ClaimNext(ws, fmt.Sprintf("w-%03d", i)); err != nil {
			t.Fatalf("ClaimNext %d: %v", i, err)
		}
		if err := task.ResetToQueue(ws, tk.ID); err != nil {
			t.Fatalf("ResetToQueue %d: %v", i, err)
		}
	}

	// Claim again for the final reset that would cross MaxResetRetries.
	if _, err := task.ClaimNext(ws, "w-final"); err != nil {
		t.Fatalf("ClaimNext final: %v", err)
	}

	// Remove failed/ so writeToDir(failed/) returns an error at the boundary.
	if err := os.RemoveAll(ws.FailedDir()); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	err := task.ResetToQueue(ws, tk.ID)
	if err == nil {
		t.Fatal("expected error when failed/ dir is gone at max retries, got nil")
	}
}

// TestClaimNextInitialScanReadFileFails covers the continue branch in ClaimNext's
// initial scan (os.ReadFile returns EACCES for a mode-0000 pending file).
func TestClaimNextInitialScanReadFileFails(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	good := makeTask(t, ws, "g-001", "good task")

	// Write a .json file with no read permissions so ReadFile returns EACCES.
	badPath := filepath.Join(ws.PendingDir(), "t-00000bad.json")
	if err := os.WriteFile(badPath, []byte(`{"id":"t-00000bad","seq":0}`), 0000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.ReadFile(badPath); err == nil {
		t.Skip("skipping: ReadFile succeeds on mode-0000 file (likely running as root)")
	}

	// ClaimNext must skip the unreadable file and successfully claim the valid task.
	claimed, err := task.ClaimNext(ws, "w-001")
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != good.ID {
		t.Errorf("claimed %s, want %s", claimed.ID, good.ID)
	}
}

// TestListByStatusReadFileErrorSkipped covers the continue branch in listDir
// when os.ReadFile returns a non-IsNotExist error for a listed file entry.
func TestListByStatusReadFileErrorSkipped(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	good := makeTask(t, ws, "g-001", "good task")

	badPath := filepath.Join(ws.PendingDir(), "t-00000bad.json")
	if err := os.WriteFile(badPath, []byte(`{"id":"t-00000bad"}`), 0000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.ReadFile(badPath); err == nil {
		t.Skip("skipping: ReadFile succeeds on mode-0000 file (likely running as root)")
	}

	tasks, err := task.ListByStatus(ws, task.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (unreadable entry must be skipped)", len(tasks))
	}
	if tasks[0].ID != good.ID {
		t.Errorf("got task %s, want %s", tasks[0].ID, good.ID)
	}
}

// ─── Tests from main branch ─────────────────────────────────────────────────

// ─── Benchmarks ──────────────────────────────────────────────────────────────

// BenchmarkClaimNext measures the throughput of the atomic task claiming path.
// This is the hottest operation in a multi-worker deployment.
func BenchmarkClaimNext(b *testing.B) {
	ws, err := workspace.Init(b.TempDir(), nil)
	if err != nil {
		b.Fatalf("workspace.Init: %v", err)
	}

	// Pre-populate enough tasks to avoid ErrNoTask during the benchmark.
	for i := 0; i < b.N; i++ {
		tk := &task.Task{
			GoalID: "g-bench",
			Title:  fmt.Sprintf("bench-task-%d", i),
			Agent:  "default",
			Prompt: "bench",
			Seq:    i,
		}
		if err := task.Create(ws, tk); err != nil {
			b.Fatalf("Create: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		claimed, err := task.ClaimNext(ws, "w-bench")
		if err != nil {
			b.Fatalf("ClaimNext: %v", err)
		}
		if err := task.Done(ws, claimed.ID, "bench done", ""); err != nil {
			b.Fatalf("Done: %v", err)
		}
	}
}

// BenchmarkListByStatus measures the cost of listing all pending tasks.
func BenchmarkListByStatus(b *testing.B) {
	ws, err := workspace.Init(b.TempDir(), nil)
	if err != nil {
		b.Fatalf("workspace.Init: %v", err)
	}
	// Create 50 tasks to simulate a realistic pending queue.
	for i := 0; i < 50; i++ {
		tk := &task.Task{
			GoalID: "g-bench",
			Title:  fmt.Sprintf("task-%d", i),
			Agent:  "default",
			Prompt: "bench",
			Seq:    i,
		}
		if err := task.Create(ws, tk); err != nil {
			b.Fatalf("Create: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := task.ListByStatus(ws, task.StatusPending)
		if err != nil {
			b.Fatalf("ListByStatus: %v", err)
		}
		_ = tasks
	}
}

// BenchmarkStatusTransitions measures the round-trip cost of a full
// pending → running → done transition cycle.
func BenchmarkStatusTransitions(b *testing.B) {
	ws, err := workspace.Init(b.TempDir(), nil)
	if err != nil {
		b.Fatalf("workspace.Init: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tk := &task.Task{
			GoalID: "g-bench",
			Title:  fmt.Sprintf("bench-transition-%d", i),
			Agent:  "default",
			Prompt: "bench",
		}
		if err := task.Create(ws, tk); err != nil {
			b.Fatalf("Create: %v", err)
		}
		b.StartTimer()

		claimed, err := task.ClaimNext(ws, "w-bench")
		if err != nil {
			b.Fatalf("ClaimNext: %v", err)
		}
		if err := task.Done(ws, claimed.ID, "bench done", ""); err != nil {
			b.Fatalf("Done: %v", err)
		}
	}
}

// TestBlockTransition verifies that Block moves a running task to blocked
// with the provided error message.
func TestBlockTransition(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "to be blocked")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	if err := task.Block(ws, tk.ID, "waiting for dependency"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusBlocked {
		t.Errorf("status = %s, want blocked", status)
	}
	if got.ErrorMsg != "waiting for dependency" {
		t.Errorf("ErrorMsg = %q, want %q", got.ErrorMsg, "waiting for dependency")
	}
}

// TestBlockTransitionRequiresRunning verifies that Block returns an error
// when called on a task that is not in running status.
func TestBlockTransitionRequiresRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "pending task")

	// Task is still pending — Block must fail.
	if err := task.Block(ws, tk.ID, "reason"); err == nil {
		t.Error("Block on pending task should return error")
	}
}

// TestCancelTransitionErrors is a table-driven test for invalid Cancel inputs.
func TestCancelTransitionErrors(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	tk := makeTask(t, ws, "g-001", "test task for cancel errors")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	// Mark running, then done.
	if err := task.Done(ws, tk.ID, "done", ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	tests := []struct {
		name    string
		taskID  string
		wantErr bool
	}{
		{
			name:    "cancel done task returns error",
			taskID:  tk.ID,
			wantErr: true,
		},
		{
			name:    "cancel non-existent task returns error",
			taskID:  "t-does-not-exist",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := task.Cancel(ws, tc.taskID)
			if (err != nil) != tc.wantErr {
				t.Errorf("Cancel(%q) error = %v, wantErr=%v", tc.taskID, err, tc.wantErr)
			}
		})
	}
}

// TestDoneTransitionRequiresRunning verifies that Done returns an error when
// the task is not in running status.
func TestDoneTransitionRequiresRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "not yet running")

	// Task is still pending — Done must fail.
	if err := task.Done(ws, tk.ID, "summary", ""); err == nil {
		t.Error("Done on pending task should return error")
	}
}

// TestFailTransitionRequiresRunning verifies that Fail returns an error when
// the task is not in running status.
func TestFailTransitionRequiresRunning(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "not yet running")

	// Task is still pending — Fail must fail.
	if err := task.Fail(ws, tk.ID, "reason"); err == nil {
		t.Error("Fail on pending task should return error")
	}
}

// TestGetByStatusExplicit verifies that GetByStatus with an explicit status
// finds the task in the correct directory and returns an error for other
// directories.
func TestGetByStatusExplicit(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "status check task")

	// Task is pending; searching pending should succeed.
	got, status, err := task.GetByStatus(ws, tk.ID, task.StatusPending)
	if err != nil {
		t.Fatalf("GetByStatus(pending): %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if got.ID != tk.ID {
		t.Errorf("got.ID = %s, want %s", got.ID, tk.ID)
	}

	// Searching running should return an error (task is not there).
	if _, _, err := task.GetByStatus(ws, tk.ID, task.StatusRunning); err == nil {
		t.Error("GetByStatus(running) should fail when task is pending")
	}
}

// TestGetByStatusUnknown verifies that GetByStatus returns an error for an
// unrecognised status string.
func TestGetByStatusUnknown(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if _, _, err := task.GetByStatus(ws, "t-any", "bogus-status"); err == nil {
		t.Error("GetByStatus with unknown status should return error")
	}
}

// TestListByStatusUnknown verifies that ListByStatus returns an error for an
// unknown status string.
func TestListByStatusUnknown(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	if _, err := task.ListByStatus(ws, "invalid-status"); err == nil {
		t.Error("ListByStatus with invalid status should return error")
	}
}

// TestNextSeq verifies that NextSeq returns 1 for a new goal and increments
// correctly as tasks are created.
func TestNextSeq(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	goalID := "g-seq-001"

	// Empty goal: first seq should be 1.
	seq, err := task.NextSeq(ws, goalID)
	if err != nil {
		t.Fatalf("NextSeq on empty goal: %v", err)
	}
	if seq != 1 {
		t.Errorf("NextSeq for empty goal = %d, want 1", seq)
	}

	// Create two tasks with explicit seq values.
	for _, s := range []int{1, 2} {
		tk := &task.Task{
			GoalID: goalID,
			Title:  fmt.Sprintf("task-seq-%d", s),
			Agent:  "default",
			Prompt: "do stuff",
			Seq:    s,
		}
		if err := task.Create(ws, tk); err != nil {
			t.Fatalf("Create seq=%d: %v", s, err)
		}
	}

	seq, err = task.NextSeq(ws, goalID)
	if err != nil {
		t.Fatalf("NextSeq after 2 tasks: %v", err)
	}
	if seq != 3 {
		t.Errorf("NextSeq after 2 tasks = %d, want 3", seq)
	}
}

// TestRetryBlockedTaskGoesToPending verifies that Retry on a blocked task
// whose dependencies are all done moves it to pending (not back to blocked).
func TestRetryBlockedTaskGoesToPending(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "to be retried from blocked")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Block(ws, tk.ID, "manual block"); err != nil {
		t.Fatalf("Block: %v", err)
	}

	// Retry should move it back to pending (no deps).
	if err := task.Retry(ws, tk.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	_, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending after Retry from blocked", status)
	}
}

// ─── RetryWithContext & Skip Tests ───────────────────────────────────────────

// TestRetryWithContext verifies that RetryWithContext moves a failed task back
// to pending, increments ExplicitFailCount, appends to FailureHistory, and
// injects the previous error into the task prompt.
func TestRetryWithContext(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "retry-with-context")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "compilation error"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if err := task.RetryWithContext(ws, tk.ID, "compilation error"); err != nil {
		t.Fatalf("RetryWithContext: %v", err)
	}

	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending", status)
	}
	if got.ExplicitFailCount != 1 {
		t.Errorf("ExplicitFailCount = %d, want 1", got.ExplicitFailCount)
	}
	if len(got.FailureHistory) != 1 {
		t.Errorf("len(FailureHistory) = %d, want 1", len(got.FailureHistory))
	} else if got.FailureHistory[0] != "compilation error" {
		t.Errorf("FailureHistory[0] = %q, want %q", got.FailureHistory[0], "compilation error")
	}
	if !strings.Contains(got.Prompt, "compilation error") {
		t.Errorf("Prompt does not contain previous error; Prompt = %q", got.Prompt)
	}
}

// TestRetryWithContextExceedsMax verifies that once ExplicitFailCount reaches
// MaxExplicitFailRetries, the next RetryWithContext call returns an error and
// the task remains in failed status.
func TestRetryWithContextExceedsMax(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "retry-exceeds-max")

	// Exhaust all allowed retries (MaxExplicitFailRetries == 2).
	for attempt := 1; attempt <= task.MaxExplicitFailRetries; attempt++ {
		if _, err := task.ClaimNext(ws, "w-001"); err != nil {
			t.Fatalf("ClaimNext attempt %d: %v", attempt, err)
		}
		if err := task.Fail(ws, tk.ID, "error"); err != nil {
			t.Fatalf("Fail attempt %d: %v", attempt, err)
		}
		if err := task.RetryWithContext(ws, tk.ID, "error"); err != nil {
			t.Fatalf("RetryWithContext attempt %d: %v", attempt, err)
		}
	}

	// One more fail — the task is now pending with ExplicitFailCount == MaxExplicitFailRetries.
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext final: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "final error"); err != nil {
		t.Fatalf("Fail final: %v", err)
	}

	// This RetryWithContext call must return an error.
	err := task.RetryWithContext(ws, tk.ID, "final error")
	if err == nil {
		t.Fatal("RetryWithContext should return error when ExplicitFailCount >= MaxExplicitFailRetries")
	}

	// Task must remain failed.
	_, status, getErr := task.Get(ws, tk.ID)
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if status != task.StatusFailed {
		t.Errorf("status = %s, want failed after max retries exceeded", status)
	}
}

// TestRetryWithContextOnNonFailedTask verifies that RetryWithContext returns an
// error when called on a task that is not in failed (or running) status.
func TestRetryWithContextOnNonFailedTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "pending-retry-context")

	// Task is still pending — RetryWithContext must fail.
	err := task.RetryWithContext(ws, tk.ID, "some error")
	if err == nil {
		t.Error("RetryWithContext on pending task should return error")
	}
}

// TestSkipBlockedTask verifies that Skip moves a task in blocked status to done.
func TestSkipBlockedTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// Create a dependency that is not yet done, so t2 will land in blocked/.
	dep := makeTask(t, ws, "g-001", "blocker-dep")
	blocked := &task.Task{
		GoalID:     "g-001",
		Title:      "blocked-task",
		Agent:      "default",
		Prompt:     "do stuff",
		DepTaskIDs: []string{dep.ID},
	}
	if err := task.Create(ws, blocked); err != nil {
		t.Fatalf("Create blocked task: %v", err)
	}

	// Confirm task is in blocked status.
	_, status, err := task.Get(ws, blocked.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusBlocked {
		t.Fatalf("expected blocked, got %s", status)
	}

	if err := task.Skip(ws, blocked.ID, "dep will never complete"); err != nil {
		t.Fatalf("Skip: %v", err)
	}

	_, status, err = task.Get(ws, blocked.ID)
	if err != nil {
		t.Fatalf("Get after Skip: %v", err)
	}
	if status != task.StatusDone {
		t.Errorf("status = %s, want done", status)
	}
}

// TestSkipFailedTask verifies that Skip moves a failed task to done and sets
// the Result summary with the [SKIPPED] prefix.
func TestSkipFailedTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "to-be-skipped-failed")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := task.Fail(ws, tk.ID, "unrecoverable error"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if err := task.Skip(ws, tk.ID, "manually skipped by operator"); err != nil {
		t.Fatalf("Skip: %v", err)
	}

	got, status, err := task.Get(ws, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != task.StatusDone {
		t.Errorf("status = %s, want done", status)
	}
	if got.Result == nil {
		t.Fatal("Result is nil after Skip")
	}
	if !strings.HasPrefix(got.Result.Summary, "[SKIPPED]") {
		t.Errorf("Result.Summary = %q, want [SKIPPED] prefix", got.Result.Summary)
	}
	if !strings.Contains(got.Result.Summary, "manually skipped by operator") {
		t.Errorf("Result.Summary = %q, want reason in summary", got.Result.Summary)
	}
}

// TestSkipNonFailedBlockedTask verifies that Skip returns an error when the
// task is in a status other than failed or blocked (e.g., running).
func TestSkipNonFailedBlockedTask(t *testing.T) {
	t.Parallel()
	ws := newWS(t)
	tk := makeTask(t, ws, "g-001", "running-task")
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Task is running — Skip must return an error.
	err := task.Skip(ws, tk.ID, "trying to skip a running task")
	if err == nil {
		t.Error("Skip on running task should return error")
	}
}

// TestSkipUnblocksDownstream verifies that Skip calls UnblockReady so tasks
// that depend only on the skipped task become pending.
func TestSkipUnblocksDownstream(t *testing.T) {
	t.Parallel()
	ws := newWS(t)

	// t1 is the task we will fail then skip.
	t1 := makeTask(t, ws, "g-001", "main-task")

	// t2 depends on t1 and will be created in blocked/ because t1 is not done.
	t2 := &task.Task{
		GoalID:     "g-001",
		Title:      "downstream-task",
		Agent:      "default",
		Prompt:     "do stuff after t1",
		DepTaskIDs: []string{t1.ID},
	}
	if err := task.Create(ws, t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}

	// Fail t1 so it lands in failed/.
	if _, err := task.ClaimNext(ws, "w-001"); err != nil {
		t.Fatalf("ClaimNext t1: %v", err)
	}
	if err := task.Fail(ws, t1.ID, "unexpected failure"); err != nil {
		t.Fatalf("Fail t1: %v", err)
	}

	// t2 should still be blocked at this point.
	_, t2Status, err := task.Get(ws, t2.ID)
	if err != nil {
		t.Fatalf("Get t2 before Skip: %v", err)
	}
	if t2Status != task.StatusBlocked {
		t.Fatalf("t2 should be blocked before Skip, got %s", t2Status)
	}

	// Skip t1 — this moves t1 to done and calls UnblockReady.
	if err := task.Skip(ws, t1.ID, "skipping to unblock downstream"); err != nil {
		t.Fatalf("Skip t1: %v", err)
	}

	// t2 should now be pending because its only dep (t1) is done.
	_, t2Status, err = task.Get(ws, t2.ID)
	if err != nil {
		t.Fatalf("Get t2 after Skip: %v", err)
	}
	if t2Status != task.StatusPending {
		t.Errorf("t2 status = %s, want pending after t1 was skipped", t2Status)
	}
}
